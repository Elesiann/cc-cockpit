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

	"golang.org/x/sys/unix"

	"github.com/elesiann/cc-cockpit/internal/state"
	"github.com/elesiann/cc-cockpit/internal/tmux"
)

// Run drives the dashboard loop until SIGINT/SIGTERM/SIGHUP/SIGQUIT.
func Run(stateHome, workspaceName string) error {
	if err := os.MkdirAll(stateHome, 0o755); err != nil {
		return err
	}

	logPath := filepath.Join(stateHome, "events.jsonl")
	bellPath := filepath.Join(stateHome, "last_bell_seq")
	currentPath := filepath.Join(stateHome, "current.json")

	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		_ = f.Close()
	}

	lastBellSeq := loadBellSeq(bellPath, stateHome)

	fmt.Print("\033[?1049h\033[?25l")
	defer fmt.Print("\033[?25h\033[?1049l")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sigCh)

	var prevFrame string
	var prevCurrentJSON []byte
	prevPaneColor := make(map[string]string)
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		var stageErr string
		var st state.State
		data, err := snapshot(stateHome)
		if err != nil {
			stageErr = "snapshot: " + err.Error()
		} else {
			st = state.Reduce(bytes.NewReader(data))
			if buf, mErr := marshalCurrent(st); mErr == nil && !bytes.Equal(buf, prevCurrentJSON) {
				writeAtomic(currentPath, buf)
				prevCurrentJSON = buf
			}
		}

		body := Render(st, workspaceName, time.Now())
		frame := body
		if stageErr != "" {
			frame = "⚠ DASHBOARD STAGE FAILED: " + stageErr + " — displayed state may be stale.\n" +
				"────────────────────────────────────────────────────────────────\n" + body
		}

		if frame != prevFrame {
			fmt.Print("\033[H")
			for _, line := range strings.Split(frame, "\n") {
				fmt.Print(line + "\033[K\n")
			}
			fmt.Print("\033[J")
			prevFrame = frame
		}

		if data != nil {
			info := computeBell(data, lastBellSeq)
			if info.NewAttention > 0 {
				fmt.Print("\a")
			}
			if info.MaxSeq > lastBellSeq {
				lastBellSeq = info.MaxSeq
				writeAtomic(bellPath, []byte(strconv.FormatInt(lastBellSeq, 10)+"\n"))
			}
			applyPaneBorderColors(st, prevPaneColor)
		}

		select {
		case <-sigCh:
			return nil
		case <-ticker.C:
		}
	}
}

// snapshot reads events.jsonl into memory under a shared flock on
// events.lock, so concurrent writers (which hold an exclusive lock) get
// serialized correctly.
func snapshot(stateHome string) ([]byte, error) {
	lockPath := filepath.Join(stateHome, "events.lock")
	logPath := filepath.Join(stateHome, "events.jsonl")
	fd, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	defer fd.Close()
	if err := unix.Flock(int(fd.Fd()), unix.LOCK_SH); err != nil {
		return nil, err
	}
	defer unix.Flock(int(fd.Fd()), unix.LOCK_UN)
	return os.ReadFile(logPath)
}

// loadBellSeq seeds from disk on subsequent boots; on first boot, primes
// from the current max log seq so historical events don't replay.
func loadBellSeq(bellPath, stateHome string) int64 {
	if raw, err := os.ReadFile(bellPath); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil {
			return n
		}
	}
	data, err := snapshot(stateHome)
	if err != nil {
		return 0
	}
	maxSeq := computeBell(data, 0).MaxSeq
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

// statusToBorderColor maps a session status to a tmux color name. Returns
// "" for unknown status (caller skips emit). green / yellow / cinzas chosen
// to give "this needs my attention" pop without screaming the whole grid.
func statusToBorderColor(status string) string {
	switch status {
	case state.StatusRunning:
		return "green"
	case state.StatusWaitingInput:
		return "yellow"
	case state.StatusIdle:
		return "colour244"
	case state.StatusEnded:
		return "colour240"
	}
	return ""
}

// applyPaneBorderColors issues `select-pane -P fg=<color>` for each session
// whose pane border color changed since the last tick. The prev map caches
// last-emitted color per pane so steady-state ticks issue zero tmux calls.
// Sessions with null pane_id (fleet/bg agents) are skipped — they don't own
// a single pane to color.
func applyPaneBorderColors(st state.State, prev map[string]string) {
	for _, sess := range st.Sessions {
		var paneID string
		if err := json.Unmarshal(sess.PaneID, &paneID); err != nil || paneID == "" {
			continue
		}
		color := statusToBorderColor(sess.Status)
		if color == "" {
			continue
		}
		if prev[paneID] == color {
			continue
		}
		_ = tmux.SetPaneBorderColor(paneID, color)
		prev[paneID] = color
	}
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
