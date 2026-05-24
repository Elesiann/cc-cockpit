package dashboard

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/sys/unix"

	"github.com/elesiann/cc-cockpit/internal/state"
)

// TaggedState pairs a reduced state with the human name of the workspace it
// came from. Multi-source renders use Name as the "WS" column.
type TaggedState struct {
	Name      string
	StateHome string
	State     state.State
	// Raw is the unreduced events.jsonl bytes from this source. The bell
	// logic re-scans it cheaply with computeBell, which only needs seq +
	// event_type per line.
	Raw []byte
}

// Source is the read side of events.jsonl. Each tick the runtime calls Sample
// once and renders/bell-rings from the result. Implementations must be
// concurrency-safe with the writer using a shared flock on events.lock.
type Source interface {
	// Sample returns the current reduced state per workspace. Returns the
	// slice as-is on partial failure (a missing file in one workspace
	// doesn't block the others) and an error only when no workspace at all
	// could be read.
	Sample() ([]TaggedState, error)
	// HeaderName is what gets shown in the title bar.
	HeaderName(samples []TaggedState) string
}

// AggregateSource is `cc-cockpit watch`'s view: every state dir under the
// cc-cockpit state root that has an events.jsonl. Per-source Reduce keeps each
// workspace's seq monotonicity intact instead of interleaving them.
type AggregateSource struct {
	StateRoot string // e.g. ~/.local/state/cc-cockpit
}

func (a AggregateSource) Sample() ([]TaggedState, error) {
	matches, err := filepath.Glob(filepath.Join(a.StateRoot, "*", "events.jsonl"))
	if err != nil {
		return nil, err
	}
	// Stable order so the table doesn't shuffle between ticks when two
	// workspaces have equally-recent activity. Glob is already
	// lexicographic but be explicit.
	sort.Strings(matches)

	out := make([]TaggedState, 0, len(matches))
	for _, evPath := range matches {
		stateHome := filepath.Dir(evPath)
		name := filepath.Base(stateHome)
		raw, err := snapshotBytes(stateHome)
		if err != nil {
			// One unreadable workspace shouldn't blank the whole watch
			// view. Skip it and keep going.
			continue
		}
		st := state.Reduce(bytes.NewReader(raw))
		out = append(out, TaggedState{
			Name:      name,
			StateHome: stateHome,
			State:     st,
			Raw:       raw,
		})
	}
	return out, nil
}

func (a AggregateSource) HeaderName(samples []TaggedState) string {
	return fmt.Sprintf("watch · %d workspace(s)", len(samples))
}

// snapshotBytes is the shared flock-protected read of events.jsonl.
func snapshotBytes(stateHome string) ([]byte, error) {
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
	data, err := os.ReadFile(logPath)
	if err != nil && os.IsNotExist(err) {
		return nil, nil
	}
	return data, err
}

// DefaultStateRoot returns the cc-cockpit state root (parent of all per-
// workspace state dirs). Mirrors hook.ComputeStateHome's path formula
// without appending a workspace name.
func DefaultStateRoot(homeDir string, getenv func(string) string) string {
	if v := getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "cc-cockpit")
	}
	return filepath.Join(homeDir, ".local", "state", "cc-cockpit")
}
