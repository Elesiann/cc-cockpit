package state

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// Snapshot returns the full contents of stateDir/events.jsonl, holding a
// shared flock on stateDir/events.lock for the duration of the read so
// concurrent writers (state.Append, which takes an exclusive flock) cannot
// leave a torn last line in the reader's view.
//
// This is the read side of the lock pair that Append writes under. The
// watch dashboard, the end command, and the reap command all funnel
// through here so they observe the same consistency guarantees.
//
// Returns (nil, nil) when the log doesn't exist yet — a freshly initialized
// state dir is a normal state, not an error. Other I/O errors propagate.
func Snapshot(stateDir string) ([]byte, error) {
	lockPath := filepath.Join(stateDir, "events.lock")
	logPath := filepath.Join(stateDir, "events.jsonl")

	fd, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	defer fd.Close()
	if err := unix.Flock(int(fd.Fd()), unix.LOCK_SH); err != nil {
		return nil, err
	}
	defer unix.Flock(int(fd.Fd()), unix.LOCK_UN)

	data, err := os.ReadFile(logPath)
	if err != nil && os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}
