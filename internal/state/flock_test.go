package state

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// Regression for the reaper-flock race: state.Append takes FLOCK_EX while
// writing; the reader in cmd/cc-cockpit/main.go's collectEndTargets used
// to bypass coordination with a bare os.Open, so a concurrent write could
// leave a torn last line in the reader's view and `reap` would under-count
// last_activity.
//
// The fix is to route every read of events.jsonl through state.Snapshot,
// which takes FLOCK_SH for the duration of the read. This test asserts
// that Snapshot blocks while a writer holds FLOCK_EX and only returns
// once the writer releases — the structural guarantee that closes the
// race window.
func TestSnapshot_BlocksUntilExclusiveFlockReleased(t *testing.T) {
	dir := t.TempDir()

	// Seed an event so Snapshot has something to read.
	if err := Append(dir, map[string]any{
		"event_type": "SessionStart",
		"session_id": "seed",
		"payload":    map[string]any{"cwd": "/x"},
	}); err != nil {
		t.Fatalf("seed append: %v", err)
	}

	holdDuration := 400 * time.Millisecond
	writerStarted := make(chan struct{})
	writerReleased := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		lockPath := filepath.Join(dir, "events.lock")
		fd, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
		if err != nil {
			t.Errorf("writer open: %v", err)
			return
		}
		defer fd.Close()
		if err := unix.Flock(int(fd.Fd()), unix.LOCK_EX); err != nil {
			t.Errorf("writer flock: %v", err)
			return
		}
		close(writerStarted)
		time.Sleep(holdDuration)
		_ = unix.Flock(int(fd.Fd()), unix.LOCK_UN)
		close(writerReleased)
	}()

	<-writerStarted
	time.Sleep(20 * time.Millisecond) // give the kernel a moment to register the lock

	// Reader: the same path the reaper takes post-fix. Must block on the
	// writer's FLOCK_EX.
	readerStart := time.Now()
	_, err := Snapshot(dir)
	readerElapsed := time.Since(readerStart)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	t.Logf("writer held FLOCK_EX for %v", holdDuration)
	t.Logf("Snapshot (FLOCK_SH) returned in %v", readerElapsed)

	// Tolerance: a 50ms slack absorbs goroutine scheduling jitter while
	// still failing fast if the reader returns without coordination.
	const slack = 50 * time.Millisecond
	if readerElapsed < holdDuration-slack {
		t.Errorf("Snapshot returned in %v while writer held FLOCK_EX for %v — reader is not coordinated with the writer",
			readerElapsed, holdDuration)
	}
	if isChannelOpen(writerReleased) {
		t.Errorf("Snapshot returned before writer released its lock")
	}

	wg.Wait()
}

func isChannelOpen(ch chan struct{}) bool {
	select {
	case <-ch:
		return false
	default:
		return true
	}
}
