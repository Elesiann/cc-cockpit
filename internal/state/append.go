package state

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// Append writes ev as one JSONL line to stateDir/events.jsonl, assigning a
// fresh seq and wall_clock_iso8601 timestamp inside an exclusive flock on
// stateDir/events.lock. Concurrent appends from any cc-cockpit process (Go
// or bash) serialize correctly through the same lock.
//
// seq is computed as max(counter, max-seq-in-log) + 1. The seq.counter file
// is kept in sync with the bash binary for the duration of the migration.
// Computing from the log too means a stale or missing counter still produces
// monotonic seqs — the seq-gap bug from the bash version is closed.
func Append(stateDir string, ev map[string]any) error {
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return err
	}

	lockPath := filepath.Join(stateDir, "events.lock")
	logPath := filepath.Join(stateDir, "events.jsonl")
	counterPath := filepath.Join(stateDir, "seq.counter")

	lockFd, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer lockFd.Close()
	if err := unix.Flock(int(lockFd.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(lockFd.Fd()), unix.LOCK_UN)

	counter := readCounter(counterPath)
	logMax := scanMaxSeq(logPath)
	next := counter
	if logMax > next {
		next = logMax
	}
	next++

	ev["seq"] = next
	ev["wall_clock_iso8601"] = time.Now().UTC().Format("2006-01-02T15:04:05Z")

	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	line = append(line, '\n')

	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(line); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	// Counter update is best-effort: if it fails, the next call recovers from logMax.
	_ = os.WriteFile(counterPath, []byte(strconv.FormatInt(next, 10)+"\n"), 0o644)
	return nil
}

func readCounter(path string) int64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// scanMaxSeq returns the largest seq found in events.jsonl. Tolerates
// malformed lines (skipped silently).
func scanMaxSeq(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var maxSeq int64
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev struct {
			Seq int64 `json:"seq"`
		}
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if ev.Seq > maxSeq {
			maxSeq = ev.Seq
		}
	}
	return maxSeq
}
