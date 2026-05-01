package state

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"sort"
	"time"
)

// Reduce consumes events.jsonl from r and returns the reduced state.
//
// Mirrors reduce-state.sh exactly:
//   - Skips empty lines.
//   - Drops malformed JSON, validates required fields, drops events that fail.
//   - Counts non-empty-but-invalid lines into State.DroppedEvents.
//   - Sorts surviving events by seq (stable) then applies them in order.
func Reduce(r io.Reader) State {
	state := State{Sessions: make(map[string]*Session)}

	scanner := bufio.NewScanner(r)
	// Default buffer is 64KiB; events can include long prompt previews. Allow up to 16MiB per line.
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
		// Normalize payload: missing or null becomes {}.
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

// isValidEvent mirrors the jq validation block in reduce-state.sh.
func isValidEvent(ev *Event) bool {
	if ev.WallClockISO8601 == "" {
		return false
	}
	if _, err := time.Parse(time.RFC3339, ev.WallClockISO8601); err != nil {
		// jq's fromdateiso8601 also accepts fractional seconds; try RFC3339Nano as a fallback.
		if _, err2 := time.Parse(time.RFC3339Nano, ev.WallClockISO8601); err2 != nil {
			return false
		}
	}
	if ev.EventType == "" || ev.SessionID == "" {
		return false
	}
	// payload: missing OR null OR object
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

	// Pre-revive: a dismissed session receiving any non-SessionEnd event comes back as running.
	if sess, ok := state.Sessions[ev.SessionID]; ok && isDismissed(sess) && ev.EventType != "SessionEnd" {
		f := false
		sess.Status = "running"
		sess.Dismissed = &f
		sess.RevivedAt = ev.WallClockISO8601
		sess.EndedAt = jsonNull
	}

	switch ev.EventType {
	case "SessionStart":
		sess, ok := state.Sessions[ev.SessionID]
		if ok {
			sess.LastActivity = ev.WallClockISO8601
			if sess.ResumedAt == "" {
				sess.ResumedAt = ev.WallClockISO8601
			}
			return
		}
		state.Sessions[ev.SessionID] = newSessionFromStart(ev)

	case "UserPromptSubmit":
		sess, ok := state.Sessions[ev.SessionID]
		if !ok || sess.Status == "ended" {
			return
		}
		sess.Status = "running"
		sess.LastActivity = ev.WallClockISO8601
		var p struct {
			PromptPreview json.RawMessage `json:"prompt_preview"`
		}
		_ = json.Unmarshal(ev.Payload, &p)
		if len(p.PromptPreview) == 0 {
			p.PromptPreview = jsonNull
		}
		sess.LastPromptPreview = p.PromptPreview

	case "PermissionRequest", "Notification":
		sess, ok := state.Sessions[ev.SessionID]
		if !ok || sess.Status == "ended" {
			return
		}
		sess.Status = "waiting_input"
		sess.LastActivity = ev.WallClockISO8601

	case "PostToolUse":
		sess, ok := state.Sessions[ev.SessionID]
		if !ok {
			return
		}
		if sess.Status == "waiting_input" {
			sess.Status = "running"
		}
		sess.LastActivity = ev.WallClockISO8601

	case "Stop":
		sess, ok := state.Sessions[ev.SessionID]
		if !ok || sess.Status == "ended" {
			return
		}
		sess.Status = "idle"
		sess.LastActivity = ev.WallClockISO8601

	case "SessionEnd":
		sess, ok := state.Sessions[ev.SessionID]
		if !ok {
			return
		}
		ts, _ := json.Marshal(ev.WallClockISO8601)
		sess.Status = "ended"
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

func isDismissed(s *Session) bool {
	return s.Dismissed != nil && *s.Dismissed
}

// newSessionFromStart copies the SessionStart payload fields into a new
// Session with status=running. Missing fields default to JSON null —
// matching jq's behavior where `$e.payload.foo` on a missing key yields null.
func newSessionFromStart(ev *Event) *Session {
	var p struct {
		PrimaryRepo          json.RawMessage `json:"primary_repo"`
		DeclaredRelatedRepos json.RawMessage `json:"declared_related_repos"`
		TaskName             json.RawMessage `json:"task_name"`
		Cwd                  json.RawMessage `json:"cwd"`
		ZellijPaneID         json.RawMessage `json:"zellij_pane_id"`
	}
	_ = json.Unmarshal(ev.Payload, &p)

	defaultNull := func(b json.RawMessage) json.RawMessage {
		if len(b) == 0 {
			return jsonNull
		}
		return b
	}

	return &Session{
		SessionID:            ev.SessionID,
		PrimaryRepo:          defaultNull(p.PrimaryRepo),
		DeclaredRelatedRepos: defaultNull(p.DeclaredRelatedRepos),
		TaskName:             defaultNull(p.TaskName),
		Cwd:                  defaultNull(p.Cwd),
		ZellijPaneID:         defaultNull(p.ZellijPaneID),
		Status:               "running",
		StartedAt:            ev.WallClockISO8601,
		LastActivity:         ev.WallClockISO8601,
		LastPromptPreview:    jsonNull,
	}
}
