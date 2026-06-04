// Package state holds cc-cockpit's event types, reducer, and append helper.
package state

import "encoding/json"

// Event types emitted by Claude Code hooks.
const (
	EventSessionStart       = "SessionStart"
	EventUserPromptSubmit   = "UserPromptSubmit"
	EventPermissionRequest  = "PermissionRequest"
	EventNotification       = "Notification"
	EventPreToolUse         = "PreToolUse"
	EventPostToolUse        = "PostToolUse"
	EventPostToolUseFailure = "PostToolUseFailure"
	EventPostToolBatch      = "PostToolBatch"
	EventStop               = "Stop"
	EventStopFailure        = "StopFailure"
	EventSessionEnd         = "SessionEnd"
)

// Reduced session statuses. Granular states derived from the event sequence:
//
//	idle        → SessionStart, no UserPromptSubmit yet (fresh boot)
//	thinking    → UserPromptSubmit received, Claude is reasoning
//	running     → PreToolUse seen, tool currently executing (see CurrentTool)
//	processing  → PostToolUse seen, Claude is reading the result
//	waiting_input → Notification (idle_prompt|permission_prompt) or PermissionRequest
//	completed   → Stop event, Claude finished its turn
//	ended       → SessionEnd (real or synthetic)
//
// `idle` as a long-quiet-completed state is a render-time derivation
// (StatusCompleted + LastActivity older than IdleAfterCompleted), not a
// reducer state — keeping the reducer pure-from-events.
const (
	StatusRunning      = "running"
	StatusThinking     = "thinking"
	StatusProcessing   = "processing"
	StatusWaitingInput = "waiting_input"
	StatusCompleted    = "completed"
	StatusIdle         = "idle"
	StatusEnded        = "ended"
)

// Event is one envelope from events.jsonl. Payload stays as RawMessage so the
// reducer can copy per-event-type fields without re-marshaling.
type Event struct {
	Seq              int64           `json:"seq"`
	WallClockISO8601 string          `json:"wall_clock_iso8601"`
	EventType        string          `json:"event_type"`
	SessionID        string          `json:"session_id"`
	Payload          json.RawMessage `json:"payload,omitempty"`
}

// Session mirrors the per-session object the reducer builds. Field order is
// the order jq inserts them, and omitempty controls whether late-added fields
// (resumed_at, ended_at, dismissed, revived_at) appear before they're set.
type Session struct {
	SessionID         string          `json:"session_id"`
	Cwd               json.RawMessage `json:"cwd"`
	TranscriptPath    json.RawMessage `json:"transcript_path"`
	Status            string          `json:"status"`
	StartedAt         string          `json:"started_at"`
	LastActivity      string          `json:"last_activity"`
	LastPromptPreview json.RawMessage `json:"last_prompt_preview"`
	CurrentTool       string          `json:"current_tool,omitempty"`
	ToolCounts        map[string]int  `json:"tool_counts,omitempty"`
	LastTool          string          `json:"last_tool,omitempty"`
	LastToolAt        string          `json:"last_tool_at,omitempty"`
	FailureCount      int             `json:"failure_count,omitempty"`
	LastFailureTool   string          `json:"last_failure_tool,omitempty"`
	LastFailureAt     string          `json:"last_failure_at,omitempty"`
	LastFailure       string          `json:"last_failure,omitempty"`
	ResumedAt         string          `json:"resumed_at,omitempty"`
	EndedAt           json.RawMessage `json:"ended_at,omitempty"`
	Dismissed         *bool           `json:"dismissed,omitempty"`
	RevivedAt         string          `json:"revived_at,omitempty"`
}

type State struct {
	Sessions      map[string]*Session `json:"sessions"`
	LastSeq       int64               `json:"last_seq"`
	DroppedEvents int                 `json:"dropped_events"`
}

var jsonNull = json.RawMessage("null")
