package state

import (
	"strings"
	"testing"
)

// Fixtures lifted from test/smoke.sh §[9], [12], [13], [14] so behavior is
// locked to the same scenarios the bash reducer is tested against.
const (
	fixtureMalformed = `{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"s1","payload":{"primary_repo":"r","declared_related_repos":[],"task_name":"t","cwd":"/x"}}
THIS LINE IS NOT JSON
{"seq":99,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","payload":{}}
{"seq":100,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"bad","payload":[]}
{"seq":101,"wall_clock_iso8601":"not-a-date","event_type":"SessionStart","session_id":"badtime","payload":{}}
{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"Stop","session_id":"s1","payload":{}}`

	fixtureTransient = `{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"s1","payload":{}}
{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"Notification","session_id":"s1","payload":{"notification_type":"idle_prompt"}}
{"seq":3,"wall_clock_iso8601":"2026-04-20T15:00:02Z","event_type":"PostToolUse","session_id":"s1","payload":{"tool_name":"W"}}`

	fixtureDismiss = `{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"a","payload":{"primary_repo":"r","task_name":"ta","declared_related_repos":[]}}
{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"SessionStart","session_id":"b","payload":{"primary_repo":"r","task_name":"tb","declared_related_repos":[]}}
{"seq":3,"wall_clock_iso8601":"2026-04-20T15:00:02Z","event_type":"SessionEnd","session_id":"a","payload":{"synthetic":true}}
{"seq":4,"wall_clock_iso8601":"2026-04-20T15:00:03Z","event_type":"SessionEnd","session_id":"b","payload":{}}
{"seq":5,"wall_clock_iso8601":"2026-04-20T15:00:04Z","event_type":"UserPromptSubmit","session_id":"a","payload":{"prompt_preview":"alive"}}
{"seq":6,"wall_clock_iso8601":"2026-04-20T15:00:05Z","event_type":"UserPromptSubmit","session_id":"b","payload":{"prompt_preview":"zombie"}}`
)

func TestReducer_TolerateMalformed(t *testing.T) {
	// smoke.sh §[9]: dropped=4 (not-json + missing-session_id + array-payload + bad-date),
	// s1 ends up idle (Stop after SessionStart).
	st := Reduce(strings.NewReader(fixtureMalformed))
	if st.DroppedEvents != 4 {
		t.Errorf("dropped_events: got %d, want 4", st.DroppedEvents)
	}
	s1, ok := st.Sessions["s1"]
	if !ok {
		t.Fatalf("session s1 missing")
	}
	if s1.Status != "idle" {
		t.Errorf("s1.status: got %q, want idle", s1.Status)
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
	// back to running. Bell-on-delta still fires (tested in dashboard package);
	// here we just verify the reducer's collapsed state.
	st := Reduce(strings.NewReader(fixtureTransient))
	s1, ok := st.Sessions["s1"]
	if !ok {
		t.Fatalf("session s1 missing")
	}
	if s1.Status != "running" {
		t.Errorf("s1.status: got %q, want running", s1.Status)
	}
}

func TestReducer_DismissalRevivable(t *testing.T) {
	// smoke.sh §[13]: synthetic SessionEnd is revivable; natural is terminal.
	st := Reduce(strings.NewReader(fixtureDismiss))
	a := st.Sessions["a"]
	b := st.Sessions["b"]
	if a == nil || b == nil {
		t.Fatalf("sessions missing: a=%v b=%v", a, b)
	}
	if a.Status != "running" {
		t.Errorf("a.status: got %q, want running (synthetic-end + later event = revived)", a.Status)
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
	if s1.Status != "idle" {
		t.Errorf("s1.status: got %q, want idle (Stop must apply after SessionStart)", s1.Status)
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

func TestReducer_PostToolUseUpdatesActivityWithoutResettingIdle(t *testing.T) {
	// PostToolUse only switches status if currently waiting_input. From idle, it
	// just bumps last_activity (matches reduce-state.sh:101-104).
	input := `{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"s1","payload":{}}
{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"Stop","session_id":"s1","payload":{}}
{"seq":3,"wall_clock_iso8601":"2026-04-20T15:00:02Z","event_type":"PostToolUse","session_id":"s1","payload":{"tool_name":"W"}}`
	st := Reduce(strings.NewReader(input))
	if st.Sessions["s1"].Status != "idle" {
		t.Errorf("PostToolUse from idle must keep idle, got %q", st.Sessions["s1"].Status)
	}
	if st.Sessions["s1"].LastActivity != "2026-04-20T15:00:02Z" {
		t.Errorf("last_activity should advance to PostToolUse time, got %q", st.Sessions["s1"].LastActivity)
	}
}

func TestReducer_LegacyZellijPaneIDFieldStillRead(t *testing.T) {
	// Pre-v0.3 logs have zellij_pane_id in the SessionStart payload. The
	// reducer reads either field name and stores it in PaneID for back-compat.
	input := `{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"s1","payload":{"primary_repo":"r","task_name":"t","declared_related_repos":[],"zellij_pane_id":"%9"}}`
	st := Reduce(strings.NewReader(input))
	got := string(st.Sessions["s1"].PaneID)
	if got != `"%9"` {
		t.Errorf("PaneID from legacy zellij_pane_id: got %q, want \"%%9\"", got)
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
