package dashboard

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/elesiann/cc-cockpit/internal/state"
	"github.com/elesiann/cc-cockpit/internal/winfocus"
)

// Run drives the dashboard loop until SIGINT/SIGTERM/SIGHUP/SIGQUIT. The
// Source supplies one Sample per tick; the loop renders and rings the bell.
func Run(src Source) error {
	fmt.Print("\033[?1049h\033[?25l")
	defer fmt.Print("\033[?25h\033[?1049l")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sigCh)

	// Interactive selection (arrow keys → focus a session's window) is only
	// meaningful where window focus works: WSL + Windows Terminal. Elsewhere
	// watch stays exactly as it was — render-only. If stdin isn't a terminal,
	// enableCharInput fails and we silently fall back to non-interactive.
	var keyCh chan key
	if winfocus.Enabled() {
		if restore, err := enableCharInput(int(os.Stdin.Fd())); err == nil {
			defer restore()
			keyCh = make(chan key, 16)
			go readKeys(os.Stdin, keyCh)
		}
	}
	selIdx := 0

	home, _ := os.UserHomeDir()
	notify := resolveNotifier()
	if notify.describe != "" && notify.describe != "none" {
		// One-line stderr breadcrumb so the user can tell which backend
		// resolved without strace'ing the process. Goes to stderr (not
		// the alt-screen) so it doesn't smear the frame.
		fmt.Fprintf(os.Stderr, "cc-cockpit: desktop notifications via %s\n", notify.describe)
	}

	// Seed bell baselines from disk (or current max seq, if first run) so
	// historical events don't replay on boot. One entry per stateHome.
	lastBellSeq := make(map[string]int64)
	if samples, err := src.Sample(); err == nil {
		for _, s := range samples {
			lastBellSeq[s.StateHome] = loadBellSeq(s.StateHome, s.Raw)
		}
	}

	var prevFrame string
	recaps := newRecapCache()
	metas := NewSessionMetaLoader()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		var stageErr string
		samples, err := src.Sample()
		if err != nil {
			stageErr = "snapshot: " + err.Error()
		}

		// Seed bell baselines for workspaces that appeared after Run started.
		// Without this we'd ring on the historical backlog.
		for _, s := range samples {
			if _, seen := lastBellSeq[s.StateHome]; !seen {
				lastBellSeq[s.StateHome] = loadBellSeq(s.StateHome, s.Raw)
			}
		}

		now := time.Now()
		// Keep the selection in range as sessions come and go, and publish the
		// selected sid for the renderer to highlight.
		rows := activeRowsOrdered(samples, now)
		if selIdx >= len(rows) {
			selIdx = len(rows) - 1
		}
		if selIdx < 0 {
			selIdx = 0
		}
		if keyCh != nil && len(rows) > 0 {
			Selected = rows[selIdx].sid
		} else {
			Selected = ""
		}

		sessionMetas := metas.Load(home)
		recapTexts := recaps.load(samples)
		agentRollups := loadSubagentRollups(samples, now)
		frame := RenderMulti(samples, src.HeaderName(samples), now, sessionMetas, recapTexts, agentRollups)
		if stageErr != "" {
			frame = "⚠ DASHBOARD STAGE FAILED: " + stageErr + " — displayed state may be stale.\n" +
				"────────────────────────────────────────────────────────────────\n" + frame
		}

		if frame != prevFrame {
			fmt.Print("\033[H")
			for _, line := range strings.Split(frame, "\n") {
				fmt.Print(line + "\033[K\n")
			}
			fmt.Print("\033[J")
			prevFrame = frame
		}

		// Bell + desktop notification. Aggregate NewAttention across
		// sources so one toast covers a multi-workspace burst.
		totalAttention := 0
		var attentionRepo, attentionTask string
		for _, s := range samples {
			info := computeBell(s.Raw, lastBellSeq[s.StateHome])
			if info.NewAttention > 0 {
				totalAttention += info.NewAttention
				if attentionRepo == "" {
					attentionRepo, attentionTask = pickAttentionLabels(s.State, sessionMetas)
				}
			}
			if info.MaxSeq > lastBellSeq[s.StateHome] {
				lastBellSeq[s.StateHome] = info.MaxSeq
				writeAtomic(filepath.Join(s.StateHome, "last_bell_seq"),
					[]byte(strconv.FormatInt(info.MaxSeq, 10)+"\n"))
			}
		}
		if totalAttention > 0 {
			fmt.Print("\a")
			notify.Notify(buildNotifyMessage(totalAttention, attentionRepo, attentionTask))
		}

		select {
		case <-sigCh:
			return nil
		case k := <-keyCh: // nil channel when non-interactive: this case never fires
			switch k {
			case keyUp:
				if selIdx > 0 {
					selIdx--
				}
				StatusLine = ""
			case keyDown:
				if selIdx < len(rows)-1 {
					selIdx++
				}
				StatusLine = ""
			case keyEnter:
				if len(rows) > 0 {
					focusRow(rows[selIdx])
				}
			case keyQuit:
				return nil
			}
		case <-ticker.C:
		}
	}
}

// focusRow raises the Windows Terminal window bound to the selected session.
// The actual focus call is slow (cold powershell.exe), so it runs in a
// goroutine to keep the dashboard responsive; StatusLine reports the attempt.
func focusRow(r activeRow) {
	hwnd, ok := winfocus.ReadHWND(r.home, r.sid)
	if !ok {
		StatusLine = "no window bound for " + shortSID(r.sid) + " — start a fresh claude session under WSL+WT"
		return
	}
	StatusLine = "→ focusing " + sessionRepoLabel(r.sess) + " (" + shortSID(r.sid) + ")"
	go func() { _ = winfocus.Focus(hwnd) }()
}

// loadBellSeq returns the persisted bell baseline for stateHome, falling back
// to the current max seq in raw (so historical events don't replay on first
// run). raw may be nil; in that case the baseline is 0.
func loadBellSeq(stateHome string, raw []byte) int64 {
	bellPath := filepath.Join(stateHome, "last_bell_seq")
	if data, err := os.ReadFile(bellPath); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err == nil {
			return n
		}
	}
	maxSeq := computeBell(raw, 0).MaxSeq
	writeAtomic(bellPath, []byte(strconv.FormatInt(maxSeq, 10)+"\n"))
	return maxSeq
}

type bellInfo struct {
	NewAttention int
	MaxSeq       int64
}

func computeBell(data []byte, lastBellSeq int64) bellInfo {
	info := bellInfo{MaxSeq: lastBellSeq}
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev struct {
			Seq       int64  `json:"seq"`
			EventType string `json:"event_type"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Seq > info.MaxSeq {
			info.MaxSeq = ev.Seq
		}
		if ev.Seq > lastBellSeq && (ev.EventType == state.EventNotification || ev.EventType == state.EventPermissionRequest) {
			info.NewAttention++
		}
	}
	return info
}

// pickAttentionLabels returns repo/task for one waiting_input session in st.
// When several sessions need attention at once, the most-recently-active wins
// (sid asc as a tiebreaker so the choice is stable). Without that ordering,
// Go's randomized map iteration could flip the notification label between
// otherwise-identical ticks. Best-effort: returns ("", "") when no session
// is waiting and the caller falls back to a generic message. metas carries
// Claude Code's /rename values so notifications match what the user sees in
// the dashboard.
func pickAttentionLabels(st state.State, metas map[string]SessionMeta) (repo, task string) {
	type waitingSession struct {
		sid  string
		sess *state.Session
	}
	var waiting []waitingSession
	for sid, s := range st.Sessions {
		if s.Status == state.StatusWaitingInput {
			waiting = append(waiting, waitingSession{sid, s})
		}
	}
	if len(waiting) == 0 {
		return "", ""
	}
	sort.Slice(waiting, func(i, j int) bool {
		if waiting[i].sess.LastActivity != waiting[j].sess.LastActivity {
			return waiting[i].sess.LastActivity > waiting[j].sess.LastActivity
		}
		return waiting[i].sid < waiting[j].sid
	})
	w := waiting[0]
	repo = sessionRepoLabel(w.sess)
	task = sessionTaskLabel(w.sess, metas[w.sid])
	if repo == "—" {
		repo = ""
	}
	if task == "—" {
		task = ""
	}
	return repo, task
}

func buildNotifyMessage(count int, repo, task string) string {
	if count > 1 {
		return fmt.Sprintf("%d sessions waiting for input", count)
	}
	label := strings.TrimSpace(repo + " · " + task)
	label = strings.Trim(label, "· ")
	if label == "" {
		return "session waiting for input"
	}
	return label + " waiting for input"
}

func writeAtomic(path string, data []byte) {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}
