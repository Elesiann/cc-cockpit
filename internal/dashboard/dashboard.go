package dashboard

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/elesiann/cc-cockpit/internal/state"
)

// Options tunes Run's behavior. Empty values are fine — Run picks safe
// defaults so most callers can pass dashboard.Options{}.
type Options struct {
	// ApplyTmuxBorderColors is true only for the in-cockpit dashboard,
	// where the dashboard pane is a sibling of the Claude panes and can
	// color their borders. `watch` runs outside tmux and must skip this.
	ApplyTmuxBorderColors bool
	// WriteCurrentJSON is true only for the in-cockpit dashboard. The
	// current.json file is a per-workspace artifact that lives next to
	// events.jsonl; aggregate mode skips it (no clear place to put it,
	// and nothing consumes it from the watch view).
	WriteCurrentJSON bool
}

// Run drives the dashboard loop until SIGINT/SIGTERM/SIGHUP/SIGQUIT. The
// Source supplies one Sample per tick; the loop renders, rings the bell, and
// (when configured) repaints tmux pane borders.
func Run(src Source, opts Options) error {
	fmt.Print("\033[?1049h\033[?25l")
	defer fmt.Print("\033[?25h\033[?1049l")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sigCh)

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
	prevCurrentJSON := make(map[string][]byte)
	prevPaneColor := make(map[string]string)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		var stageErr string
		samples, err := src.Sample()
		if err != nil {
			stageErr = "snapshot: " + err.Error()
		}

		// Seed bell baselines for workspaces that appeared after Run started
		// (a `cc-cockpit open` in another terminal creates a new state dir
		// mid-watch). Without this we'd ring on the historical backlog.
		for _, s := range samples {
			if _, seen := lastBellSeq[s.StateHome]; !seen {
				lastBellSeq[s.StateHome] = loadBellSeq(s.StateHome, s.Raw)
			}
		}

		if opts.WriteCurrentJSON {
			for _, s := range samples {
				if buf, mErr := marshalCurrent(s.State); mErr == nil {
					if !bytes.Equal(buf, prevCurrentJSON[s.StateHome]) {
						writeAtomic(filepath.Join(s.StateHome, "current.json"), buf)
						prevCurrentJSON[s.StateHome] = buf
					}
				}
			}
		}

		now := time.Now()
		metas := LoadSessionMetas(home)
		var frame string
		if src.IsMulti() {
			frame = RenderMultiWithMetas(samples, src.HeaderName(samples), now, metas)
		} else if len(samples) > 0 {
			frame = RenderWithMetas(samples[0].State, src.HeaderName(samples), now, metas)
		} else {
			frame = RenderWithMetas(state.State{}, src.HeaderName(samples), now, metas)
		}
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
					attentionRepo, attentionTask = pickAttentionLabels(s.State)
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

		if opts.ApplyTmuxBorderColors && len(samples) > 0 {
			applyPaneBorderColors(samples[0].State, prevPaneColor)
		}

		select {
		case <-sigCh:
			return nil
		case <-ticker.C:
		}
	}
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

// pickAttentionLabels returns repo/task for the first waiting_input session
// in st. Used to label the desktop notification. Best-effort: when nothing
// matches, returns ("", "") and the caller falls back to a generic message.
func pickAttentionLabels(st state.State) (repo, task string) {
	for _, s := range st.Sessions {
		if s.Status == state.StatusWaitingInput {
			return jsonRawString(s.PrimaryRepo, ""), jsonRawString(s.TaskName, "")
		}
	}
	return "", ""
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

func marshalCurrent(st state.State) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(st); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
