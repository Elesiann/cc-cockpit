// Package state implements cc-cockpit's event types and reducer.
//
// The reducer is a direct port of .cc-cockpit/reduce-state.sh — same
// validation rules, same session state transitions, same dismissal/revival
// semantics. Output JSON is semantically equivalent to the bash reducer's
// (the differential CI test canonicalizes both with `jq -S` before diffing
// rather than requiring byte-identical key ordering).
package state

import "encoding/json"

// Event is one envelope from events.jsonl.
//
// Payload is held as RawMessage so the reducer can faithfully copy
// per-event-type fields into the session without re-marshaling — this
// matches the bash reducer's "store whatever was in the payload" behavior.
type Event struct {
	Seq              int64           `json:"seq"`
	WallClockISO8601 string          `json:"wall_clock_iso8601"`
	EventType        string          `json:"event_type"`
	SessionID        string          `json:"session_id"`
	Payload          json.RawMessage `json:"payload,omitempty"`
}

// Session mirrors the per-session object the bash reducer builds.
//
// Field order matches jq's insertion order from reduce-state.sh, so output
// JSON usually matches without canonicalization. Fields appended later by
// SessionEnd / revival use omitempty so they don't appear before they're set.
//
// json.RawMessage is used for fields that may legitimately be string or null
// in the payload (cwd, zellij_pane_id, etc.) — matches jq's dynamic typing.
type Session struct {
	SessionID            string          `json:"session_id"`
	PrimaryRepo          json.RawMessage `json:"primary_repo"`
	DeclaredRelatedRepos json.RawMessage `json:"declared_related_repos"`
	TaskName             json.RawMessage `json:"task_name"`
	Cwd                  json.RawMessage `json:"cwd"`
	ZellijPaneID         json.RawMessage `json:"zellij_pane_id"`
	Status               string          `json:"status"`
	StartedAt            string          `json:"started_at"`
	LastActivity         string          `json:"last_activity"`
	LastPromptPreview    json.RawMessage `json:"last_prompt_preview"`

	// Appended after SessionStart by later events:
	ResumedAt string          `json:"resumed_at,omitempty"`
	EndedAt   json.RawMessage `json:"ended_at,omitempty"`
	Dismissed *bool           `json:"dismissed,omitempty"`
	RevivedAt string          `json:"revived_at,omitempty"`
}

// State is the reducer's output (current.json contents).
type State struct {
	Sessions      map[string]*Session `json:"sessions"`
	LastSeq       int64               `json:"last_seq"`
	DroppedEvents int                 `json:"dropped_events"`
}

// jsonNull is the JSON literal for null, reused throughout to avoid
// re-allocating short slices.
var jsonNull = json.RawMessage("null")
