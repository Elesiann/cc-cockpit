package dashboard

import (
	"bytes"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/elesiann/cc-cockpit/internal/state"
)

// NoColor suppresses every ANSI escape the dashboard emits: per-row
// /color background, gray subagent rollups, gray recaps. Set by the
// `watch --color=never` flag (or programmatically for log capture) before
// Run is invoked. Default false keeps the TUI behavior intact.
//
// This is a write-once flag set at startup; the render path is
// single-goroutine, so no synchronization is needed.
var NoColor bool

const (
	SortStarted   = "started"
	SortActivity  = "activity"
	SortAttention = "attention"
)

// ActiveSort controls active row ordering. The default preserves the previous
// watch behavior.
var ActiveSort = SortStarted

// Selected is the session id of the highlighted row in interactive watch, or ""
// when there is no selection (non-interactive mode, or no selectable rows).
// Written by the Run loop between ticks and read by the render path; both run
// on the same goroutine, so no synchronization is needed.
var Selected string

// StatusLine is a transient one-line message shown in the watch footer (e.g.
// the result of a focus attempt). Same single-goroutine contract as Selected.
var StatusLine string

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
// workspace's seq monotonicity intact instead of interleaving them. When
// AllowedWorkspaces is non-empty, only those workspace names are included
// (matched against the basename of each state dir).
type AggregateSource struct {
	StateRoot         string // e.g. ~/.local/state/cc-cockpit
	AllowedWorkspaces []string
	Sort              string
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

	var allowed map[string]bool
	if len(a.AllowedWorkspaces) > 0 {
		allowed = make(map[string]bool, len(a.AllowedWorkspaces))
		for _, name := range a.AllowedWorkspaces {
			allowed[name] = true
		}
	}

	out := make([]TaggedState, 0, len(matches))
	for _, evPath := range matches {
		stateHome := filepath.Dir(evPath)
		name := filepath.Base(stateHome)
		if allowed != nil && !allowed[name] {
			continue
		}
		raw, err := state.Snapshot(stateHome)
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
	sortSuffix := ""
	if a.Sort != "" && a.Sort != SortStarted {
		sortSuffix = " · sort=" + a.Sort
	}
	if len(a.AllowedWorkspaces) > 0 {
		return fmt.Sprintf("watch · %d/%s%s", len(samples), strings.Join(a.AllowedWorkspaces, ","), sortSuffix)
	}
	return fmt.Sprintf("watch · %d workspace(s)%s", len(samples), sortSuffix)
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
