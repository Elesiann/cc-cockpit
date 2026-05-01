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
)

// Run drives the dashboard loop until SIGINT/SIGTERM/SIGHUP/SIGQUIT. The
// terminal enters the alt-screen on entry and is restored on any exit
// (including panics — wrapped in defer) so a crashed dashboard never leaves
// the terminal stuck.
func Run(stateHome, workspaceName string) error {
	if err := os.MkdirAll(stateHome, 0o755); err != nil {
		return err
	}

	logPath := filepath.Join(stateHome, "events.jsonl")
	bellPath := filepath.Join(stateHome, "last_bell_seq")
	currentPath := filepath.Join(stateHome, "current.json")

	// Touch the log so the first snapshot doesn't fail if no hooks have fired yet.
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		_ = f.Close()
	}

	lastBellSeq := loadBellSeq(bellPath, stateHome)

	// alt-screen + cursor hide; restore on any exit.
	fmt.Print("\033[?1049h\033[?25l")
	defer fmt.Print("\033[?25h\033[?1049l")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	defer signal.Stop(sigCh)

	var prevFrame string
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
			if buf, mErr := marshalCurrent(st); mErr == nil {
				writeAtomic(currentPath, buf)
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
		}

		select {
		case <-sigCh:
			return nil
		case <-ticker.C:
		}
	}
}

// snapshot reads events.jsonl into memory under a shared flock on events.lock.
// Concurrent appends (which take an exclusive lock) will block briefly until
// the read finishes; this is the same shared-vs-exclusive dance the bash
// dashboard does.
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

// loadBellSeq reads the persisted bell-seq checkpoint; on first boot, it
// initializes to the current log's max seq so historical attention events
// don't replay.
func loadBellSeq(bellPath, stateHome string) int64 {
	if raw, err := os.ReadFile(bellPath); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64); err == nil {
			return n
		}
	}
	// First boot: seed from current max log seq.
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

// computeBell scans the snapshot for new Notification/PermissionRequest
// events with seq > lastBellSeq, and tracks the new max seq for checkpointing.
// Tolerates malformed lines.
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
		if ev.Seq > lastBellSeq && (ev.EventType == "Notification" || ev.EventType == "PermissionRequest") {
			info.NewAttention++
		}
	}
	return info
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
