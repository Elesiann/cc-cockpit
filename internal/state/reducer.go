package state

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"time"
)

// Reduce consumes events.jsonl from r and returns the reduced state. Skips
// empty lines, drops events that fail validation, and counts dropped lines
// in State.DroppedEvents.
func Reduce(r io.Reader) State {
	state := State{Sessions: make(map[string]*Session)}

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	var totalLines, validCount int
	var events []Event

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		totalLines++

		var ev Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		if !isValidEvent(&ev) {
			continue
		}
		if len(ev.Payload) == 0 || bytes.Equal(ev.Payload, jsonNull) {
			ev.Payload = json.RawMessage("{}")
		}
		events = append(events, ev)
		validCount++
	}

	state.DroppedEvents = totalLines - validCount

	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Seq < events[j].Seq
	})

	for i := range events {
		applyEvent(&state, &events[i])
	}
	return state
}

func isValidEvent(ev *Event) bool {
	if ev.WallClockISO8601 == "" {
		return false
	}
	if _, err := time.Parse(time.RFC3339, ev.WallClockISO8601); err != nil {
		// jq's fromdateiso8601 also accepts fractional seconds.
		if _, err2 := time.Parse(time.RFC3339Nano, ev.WallClockISO8601); err2 != nil {
			return false
		}
	}
	if ev.EventType == "" || ev.SessionID == "" {
		return false
	}
	if len(ev.Payload) > 0 && !bytes.Equal(ev.Payload, jsonNull) {
		trimmed := bytes.TrimLeft(ev.Payload, " \t\n\r")
		if len(trimmed) == 0 || trimmed[0] != '{' {
			return false
		}
	}
	return true
}

func applyEvent(state *State, ev *Event) {
	state.LastSeq = ev.Seq

	// Pre-revive: a dismissed session that emits any non-SessionEnd event
	// comes back as running. The switch below refines the status from the
	// event's semantics (e.g. UserPromptSubmit → thinking) — running is
	// just the generic "alive again" default.
	if sess, ok := state.Sessions[ev.SessionID]; ok && isDismissed(sess) && ev.EventType != EventSessionEnd {
		f := false
		sess.Status = StatusRunning
		sess.CurrentTool = ""
		sess.Dismissed = &f
		sess.RevivedAt = ev.WallClockISO8601
		sess.EndedAt = jsonNull
	}

	switch ev.EventType {
	case EventSessionStart:
		sess, ok := state.Sessions[ev.SessionID]
		if ok {
			startedAt := sess.StartedAt
			fresh := newSessionFromStart(ev)
			fresh.StartedAt = startedAt
			fresh.ResumedAt = ev.WallClockISO8601
			fresh.EndedAt = jsonNull
			f := false
			fresh.Dismissed = &f
			state.Sessions[ev.SessionID] = fresh
			return
		}
		state.Sessions[ev.SessionID] = newSessionFromStart(ev)

	case EventUserPromptSubmit:
		sess, ok := state.Sessions[ev.SessionID]
		if !ok || sess.Status == StatusEnded {
			return
		}
		sess.Status = StatusThinking
		sess.CurrentTool = ""
		sess.LastActivity = ev.WallClockISO8601
		var p struct {
			PromptPreview json.RawMessage `json:"prompt_preview"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		if len(p.PromptPreview) == 0 {
			p.PromptPreview = jsonNull
		}
		sess.LastPromptPreview = p.PromptPreview

	case EventPermissionRequest, EventNotification:
		sess, ok := state.Sessions[ev.SessionID]
		if !ok || sess.Status == StatusEnded {
			return
		}
		sess.Status = StatusWaitingInput
		sess.LastActivity = ev.WallClockISO8601

	case EventPreToolUse:
		sess, ok := state.Sessions[ev.SessionID]
		if !ok || sess.Status == StatusEnded {
			return
		}
		var p struct {
			ToolName string `json:"tool_name"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		// Defensive: don't promote a never-used or already-finished session to
		// running on a stray PreToolUse. Mid-turn states (thinking/processing/
		// waiting_input/running) all legitimately transition to running.
		if sess.Status != StatusIdle && sess.Status != StatusCompleted {
			sess.Status = StatusRunning
			sess.CurrentTool = p.ToolName
		}
		rememberTool(sess, p.ToolName, ev.WallClockISO8601, false)
		sess.LastActivity = ev.WallClockISO8601

	case EventPostToolUse:
		sess, ok := state.Sessions[ev.SessionID]
		if !ok {
			return
		}
		// Same defensive rule as PreToolUse — boot-time PostToolUse on a fresh
		// idle session (see TestReducer_PostToolUseUpdatesActivityWithoutResettingIdle)
		// shouldn't fake a processing state.
		if sess.Status != StatusIdle && sess.Status != StatusCompleted && sess.Status != StatusEnded {
			sess.Status = StatusProcessing
			sess.CurrentTool = ""
		}
		var p struct {
			ToolName string `json:"tool_name"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		rememberTool(sess, p.ToolName, ev.WallClockISO8601, true)
		sess.LastActivity = ev.WallClockISO8601

	case EventPostToolUseFailure:
		sess, ok := state.Sessions[ev.SessionID]
		if !ok {
			return
		}
		var p struct {
			ToolName string `json:"tool_name"`
			Error    string `json:"error"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		if sess.Status != StatusIdle && sess.Status != StatusCompleted && sess.Status != StatusEnded {
			sess.Status = StatusProcessing
			sess.CurrentTool = ""
		}
		rememberTool(sess, p.ToolName, ev.WallClockISO8601, true)
		rememberFailure(sess, p.ToolName, p.Error, ev.WallClockISO8601)
		sess.LastActivity = ev.WallClockISO8601

	case EventPostToolBatch:
		sess, ok := state.Sessions[ev.SessionID]
		if !ok || sess.Status == StatusEnded {
			return
		}
		sess.LastActivity = ev.WallClockISO8601

	case EventStop:
		sess, ok := state.Sessions[ev.SessionID]
		if !ok || sess.Status == StatusEnded {
			return
		}
		sess.Status = StatusCompleted
		sess.CurrentTool = ""
		sess.LastActivity = ev.WallClockISO8601

	case EventStopFailure:
		sess, ok := state.Sessions[ev.SessionID]
		if !ok || sess.Status == StatusEnded {
			return
		}
		var p struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		sess.Status = StatusCompleted
		sess.CurrentTool = ""
		rememberFailure(sess, "", p.Error, ev.WallClockISO8601)
		sess.LastActivity = ev.WallClockISO8601

	case EventSessionEnd:
		sess, ok := state.Sessions[ev.SessionID]
		if !ok {
			return
		}
		ts, _ := json.Marshal(ev.WallClockISO8601)
		sess.Status = StatusEnded
		sess.CurrentTool = ""
		sess.EndedAt = ts
		sess.LastActivity = ev.WallClockISO8601

		var p struct {
			Synthetic bool `json:"synthetic"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		synth := p.Synthetic
		sess.Dismissed = &synth
	}
}

func rememberTool(sess *Session, name, at string, count bool) {
	if name == "" {
		return
	}
	sess.LastTool = name
	sess.LastToolAt = at
	if !count {
		return
	}
	if sess.ToolCounts == nil {
		sess.ToolCounts = make(map[string]int)
	}
	sess.ToolCounts[name]++
}

func rememberFailure(sess *Session, tool, msg, at string) {
	sess.FailureCount++
	sess.LastFailureTool = tool
	sess.LastFailureAt = at
	sess.LastFailure = truncFailure(msg)
}

func truncFailure(s string) string {
	const maxRunes = 120
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes])
}

func isDismissed(s *Session) bool {
	return s.Dismissed != nil && *s.Dismissed
}

// newSessionFromStart copies the SessionStart payload fields into a new
// Session. Missing fields default to JSON null (matches jq's `payload.foo`
// on a missing key yielding null).
func newSessionFromStart(ev *Event) *Session {
	var p struct {
		Cwd            json.RawMessage `json:"cwd"`
		TranscriptPath json.RawMessage `json:"transcript_path"`
	}
	_ = json.Unmarshal(ev.Payload, &p)

	defaultNull := func(b json.RawMessage) json.RawMessage {
		if len(b) == 0 {
			return jsonNull
		}
		return b
	}

	return &Session{
		SessionID:      ev.SessionID,
		Cwd:            defaultNull(p.Cwd),
		TranscriptPath: defaultNull(p.TranscriptPath),
		// A freshly-started Claude session is showing its prompt and
		// waiting for the user to type the first message. Nothing is
		// being processed yet, so "idle" is the truthful state. The
		// first UserPromptSubmit flips it to "running".
		Status:            StatusIdle,
		StartedAt:         ev.WallClockISO8601,
		LastActivity:      ev.WallClockISO8601,
		LastPromptPreview: jsonNull,
	}
}
