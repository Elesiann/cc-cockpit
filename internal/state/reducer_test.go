package state

import (
	"strings"
	"testing"
)

const (
	fixtureMalformed = `{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"s1","payload":{"cwd":"/x"}}
THIS LINE IS NOT JSON
{"seq":99,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","payload":{}}
{"seq":100,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"bad","payload":[]}
{"seq":101,"wall_clock_iso8601":"not-a-date","event_type":"SessionStart","session_id":"badtime","payload":{}}
{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"Stop","session_id":"s1","payload":{}}`

	fixtureTransient = `{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"s1","payload":{}}
{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"Notification","session_id":"s1","payload":{"notification_type":"idle_prompt"}}
{"seq":3,"wall_clock_iso8601":"2026-04-20T15:00:02Z","event_type":"PostToolUse","session_id":"s1","payload":{"tool_name":"W"}}`

	fixtureDismiss = `{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"a","payload":{}}
{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"SessionStart","session_id":"b","payload":{}}
{"seq":3,"wall_clock_iso8601":"2026-04-20T15:00:02Z","event_type":"SessionEnd","session_id":"a","payload":{"synthetic":true}}
{"seq":4,"wall_clock_iso8601":"2026-04-20T15:00:03Z","event_type":"SessionEnd","session_id":"b","payload":{}}
{"seq":5,"wall_clock_iso8601":"2026-04-20T15:00:04Z","event_type":"UserPromptSubmit","session_id":"a","payload":{"prompt_preview":"alive"}}
{"seq":6,"wall_clock_iso8601":"2026-04-20T15:00:05Z","event_type":"UserPromptSubmit","session_id":"b","payload":{"prompt_preview":"zombie"}}`
)

func TestReducer_TolerateMalformed(t *testing.T) {
	// smoke.sh §[9]: dropped=4 (not-json + missing-session_id + array-payload + bad-date),
	// s1 ends up completed (Stop after SessionStart — under the new granular
	// state machine, Stop sets `completed`; the render layer derives `idle`
	// after IdleAfterCompleted has elapsed).
	st := Reduce(strings.NewReader(fixtureMalformed))
	if st.DroppedEvents != 4 {
		t.Errorf("dropped_events: got %d, want 4", st.DroppedEvents)
	}
	s1, ok := st.Sessions["s1"]
	if !ok {
		t.Fatalf("session s1 missing")
	}
	if s1.Status != StatusCompleted {
		t.Errorf("s1.status: got %q, want completed", s1.Status)
	}
	if _, ok := st.Sessions["bad"]; ok {
		t.Errorf("session 'bad' should have been dropped (array payload)")
	}
	if _, ok := st.Sessions["badtime"]; ok {
		t.Errorf("session 'badtime' should have been dropped (bad date)")
	}
}

func TestReducer_TransientNotificationCollapses(t *testing.T) {
	// smoke.sh §[12]: Notification then PostToolUse within one tick collapses
	// back to a non-waiting state. Under the granular state machine, that's
	// `processing` (PostToolUse from waiting_input → processing). Bell-on-delta
	// still fires (tested in dashboard package).
	st := Reduce(strings.NewReader(fixtureTransient))
	s1, ok := st.Sessions["s1"]
	if !ok {
		t.Fatalf("session s1 missing")
	}
	if s1.Status != StatusProcessing {
		t.Errorf("s1.status: got %q, want processing", s1.Status)
	}
}

func TestReducer_ResumeAfterNaturalEndReopensSession(t *testing.T) {
	input := `{"seq":1,"wall_clock_iso8601":"2026-05-19T23:00:00Z","event_type":"SessionStart","session_id":"s1","payload":{"cwd":"/old","source":"startup"}}
{"seq":2,"wall_clock_iso8601":"2026-05-19T23:10:00Z","event_type":"SessionEnd","session_id":"s1","payload":{}}
{"seq":3,"wall_clock_iso8601":"2026-05-20T12:00:00Z","event_type":"SessionStart","session_id":"s1","payload":{"cwd":"/new","source":"resume"}}
{"seq":4,"wall_clock_iso8601":"2026-05-20T12:01:00Z","event_type":"UserPromptSubmit","session_id":"s1","payload":{"prompt_preview":"back at it"}}`

	st := Reduce(strings.NewReader(input))
	s := st.Sessions["s1"]
	if s.Status != StatusThinking {
		t.Fatalf("status: got %q, want %q after resume + prompt", s.Status, StatusThinking)
	}
	if s.LastActivity != "2026-05-20T12:01:00Z" {
		t.Fatalf("last_activity: got %q", s.LastActivity)
	}
	if s.ResumedAt != "2026-05-20T12:00:00Z" {
		t.Fatalf("resumed_at: got %q", s.ResumedAt)
	}
	if string(s.EndedAt) != "null" {
		t.Fatalf("ended_at: got %s, want null after resume", s.EndedAt)
	}
	if got := string(s.Cwd); got != `"/new"` {
		t.Fatalf("cwd: got %s, want resumed cwd", got)
	}
}

func TestReducer_DismissalRevivable(t *testing.T) {
	// smoke.sh §[13]: synthetic SessionEnd is revivable; natural is terminal unless a real SessionStart(source=resume) arrives.
	st := Reduce(strings.NewReader(fixtureDismiss))
	a := st.Sessions["a"]
	b := st.Sessions["b"]
	if a == nil || b == nil {
		t.Fatalf("sessions missing: a=%v b=%v", a, b)
	}
	// Revival sets status=running as a generic "alive again" default, then the
	// event handler refines it. The reviving event here is UserPromptSubmit, so
	// the final status is `thinking`.
	if a.Status != StatusThinking {
		t.Errorf("a.status: got %q, want thinking (synthetic-end + UserPromptSubmit = revived → thinking)", a.Status)
	}
	if b.Status != "ended" {
		t.Errorf("b.status: got %q, want ended (natural-end is terminal)", b.Status)
	}
	if a.RevivedAt == "" {
		t.Errorf("a.revived_at should be set after revival")
	}
	if a.Dismissed == nil || *a.Dismissed {
		t.Errorf("a.dismissed should be false after revival, got %v", a.Dismissed)
	}
	if b.Dismissed == nil || *b.Dismissed {
		t.Errorf("b.dismissed should be false (natural end, payload.synthetic absent), got %v", b.Dismissed)
	}
}

func TestReducer_Determinism(t *testing.T) {
	// smoke.sh §[14]: byte-identical across two runs of the same input.
	a := Reduce(strings.NewReader(fixtureDismiss))
	b := Reduce(strings.NewReader(fixtureDismiss))
	if a.LastSeq != b.LastSeq || a.DroppedEvents != b.DroppedEvents {
		t.Errorf("top-level differs: a=%+v b=%+v", a, b)
	}
	if len(a.Sessions) != len(b.Sessions) {
		t.Errorf("session count differs: a=%d b=%d", len(a.Sessions), len(b.Sessions))
	}
	for k, av := range a.Sessions {
		bv := b.Sessions[k]
		if bv == nil || av.Status != bv.Status || av.LastActivity != bv.LastActivity {
			t.Errorf("session %s differs: a=%+v b=%+v", k, av, bv)
		}
	}
}

func TestReducer_EmptyInput(t *testing.T) {
	st := Reduce(strings.NewReader(""))
	if st.DroppedEvents != 0 {
		t.Errorf("empty input should drop 0 events, got %d", st.DroppedEvents)
	}
	if len(st.Sessions) != 0 {
		t.Errorf("empty input should produce 0 sessions, got %d", len(st.Sessions))
	}
	if st.LastSeq != 0 {
		t.Errorf("empty input should have last_seq=0, got %d", st.LastSeq)
	}
}

func TestReducer_OutOfOrderSeqs(t *testing.T) {
	// Events arriving out of order (e.g. due to flock contention) must be sorted by seq.
	input := `{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"Stop","session_id":"s1","payload":{}}
{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"s1","payload":{}}`
	st := Reduce(strings.NewReader(input))
	s1, ok := st.Sessions["s1"]
	if !ok {
		t.Fatalf("s1 missing")
	}
	if s1.Status != StatusCompleted {
		t.Errorf("s1.status: got %q, want completed (Stop must apply after SessionStart)", s1.Status)
	}
}

func TestReducer_PermissionRequestEntersWaiting(t *testing.T) {
	input := `{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"s1","payload":{}}
{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"PermissionRequest","session_id":"s1","payload":{}}`
	st := Reduce(strings.NewReader(input))
	if st.Sessions["s1"].Status != "waiting_input" {
		t.Errorf("PermissionRequest should set status=waiting_input, got %q", st.Sessions["s1"].Status)
	}
}

func TestReducer_PostToolUseDoesNotPromoteCompleted(t *testing.T) {
	// Defensive: a stray PostToolUse arriving after Stop (Claude finished its
	// turn, then a delayed hook lands) must not promote the session back to
	// `processing`. We only update last_activity. Mirrors the pre-0.7 behavior
	// where PostToolUse from idle stayed idle.
	input := `{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"s1","payload":{}}
{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"Stop","session_id":"s1","payload":{}}
{"seq":3,"wall_clock_iso8601":"2026-04-20T15:00:02Z","event_type":"PostToolUse","session_id":"s1","payload":{"tool_name":"W"}}`
	st := Reduce(strings.NewReader(input))
	if st.Sessions["s1"].Status != StatusCompleted {
		t.Errorf("PostToolUse after Stop must keep completed, got %q", st.Sessions["s1"].Status)
	}
	if st.Sessions["s1"].LastActivity != "2026-04-20T15:00:02Z" {
		t.Errorf("last_activity should advance to PostToolUse time, got %q", st.Sessions["s1"].LastActivity)
	}
}

func TestReducer_PostToolUseFromIdleDoesNotPromote(t *testing.T) {
	// Defensive: PostToolUse arriving on a freshly-started session (no
	// UserPromptSubmit yet) must not falsely flip status to `processing`.
	// Claude Code 2.x occasionally emits boot-time tool events.
	input := `{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"s1","payload":{}}
{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"PostToolUse","session_id":"s1","payload":{"tool_name":"Read"}}`
	st := Reduce(strings.NewReader(input))
	if st.Sessions["s1"].Status != StatusIdle {
		t.Errorf("PostToolUse from idle must keep idle, got %q", st.Sessions["s1"].Status)
	}
}

func TestReducer_PreToolUseFromIdleDoesNotPromote(t *testing.T) {
	// Same defensive rule for PreToolUse — without a prior UserPromptSubmit,
	// a session shouldn't jump to `running` (Claude's mid-turn state).
	input := `{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"s1","payload":{}}
{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"PreToolUse","session_id":"s1","payload":{"tool_name":"Bash"}}`
	st := Reduce(strings.NewReader(input))
	s1 := st.Sessions["s1"]
	if s1.Status != StatusIdle {
		t.Errorf("PreToolUse from idle must keep idle, got %q", s1.Status)
	}
	if s1.CurrentTool != "" {
		t.Errorf("CurrentTool must stay empty when transition is suppressed, got %q", s1.CurrentTool)
	}
}

func TestReducer_FullTurnGranularStates(t *testing.T) {
	// Walks through one complete turn — every transition along the way.
	input := `{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"s1","payload":{}}
{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"UserPromptSubmit","session_id":"s1","payload":{"prompt_preview":"hi"}}
{"seq":3,"wall_clock_iso8601":"2026-04-20T15:00:02Z","event_type":"PreToolUse","session_id":"s1","payload":{"tool_name":"Bash"}}
{"seq":4,"wall_clock_iso8601":"2026-04-20T15:00:03Z","event_type":"PostToolUse","session_id":"s1","payload":{"tool_name":"Bash"}}
{"seq":5,"wall_clock_iso8601":"2026-04-20T15:00:04Z","event_type":"Stop","session_id":"s1","payload":{}}`
	st := Reduce(strings.NewReader(input))
	s1 := st.Sessions["s1"]
	if s1.Status != StatusCompleted {
		t.Errorf("final status: got %q, want completed", s1.Status)
	}
	if s1.CurrentTool != "" {
		t.Errorf("CurrentTool must be cleared after PostToolUse/Stop, got %q", s1.CurrentTool)
	}
}

func TestReducer_PreToolUseSetsCurrentTool(t *testing.T) {
	// Captures the tool name on the session so the dashboard can show e.g.
	// `🔧 Bash` instead of generic `running`.
	input := `{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"s1","payload":{}}
{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"UserPromptSubmit","session_id":"s1","payload":{"prompt_preview":"x"}}
{"seq":3,"wall_clock_iso8601":"2026-04-20T15:00:02Z","event_type":"PreToolUse","session_id":"s1","payload":{"tool_name":"WebFetch"}}`
	st := Reduce(strings.NewReader(input))
	s1 := st.Sessions["s1"]
	if s1.Status != StatusRunning {
		t.Errorf("status after PreToolUse: got %q, want running", s1.Status)
	}
	if s1.CurrentTool != "WebFetch" {
		t.Errorf("CurrentTool: got %q, want WebFetch", s1.CurrentTool)
	}
}

func TestReducer_EventForUnknownSessionIsIgnored(t *testing.T) {
	// Events for sessions we never saw a SessionStart for must be ignored
	// (except we still count last_seq).
	input := `{"seq":42,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"Stop","session_id":"nope","payload":{}}`
	st := Reduce(strings.NewReader(input))
	if len(st.Sessions) != 0 {
		t.Errorf("unknown-session event should not create a session, got %d sessions", len(st.Sessions))
	}
	if st.LastSeq != 42 {
		t.Errorf("last_seq should advance even for ignored events, got %d", st.LastSeq)
	}
}
