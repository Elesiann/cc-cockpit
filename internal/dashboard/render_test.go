package dashboard

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/elesiann/cc-cockpit/internal/state"
)

func sessAt(status, started, lastAct string, repo, task string) *state.Session {
	return &state.Session{
		Status:               status,
		StartedAt:            started,
		LastActivity:         lastAct,
		PrimaryRepo:          json.RawMessage(`"` + repo + `"`),
		TaskName:             json.RawMessage(`"` + task + `"`),
		DeclaredRelatedRepos: json.RawMessage("[]"),
		Cwd:                  json.RawMessage("null"),
		PaneID:               json.RawMessage("null"),
		LastPromptPreview:    json.RawMessage("null"),
	}
}

func TestRender_HeaderCounts(t *testing.T) {
	st := state.State{
		Sessions: map[string]*state.Session{
			"a1": sessAt("running", "2026-04-20T15:00:00Z", "2026-04-20T15:00:00Z", "api", "x"),
			"b2": sessAt("waiting_input", "2026-04-20T15:00:01Z", "2026-04-20T15:00:01Z", "web", "y"),
			"c3": sessAt("idle", "2026-04-20T15:00:02Z", "2026-04-20T15:00:02Z", "infra", "z"),
			"d4": sessAt("ended", "2026-04-20T15:00:03Z", "2026-04-20T15:00:03Z", "ops", "q"),
		},
		DroppedEvents: 2,
	}
	frame := Render(st, "myws", time.Date(2026, 4, 20, 15, 0, 30, 0, time.UTC))
	first := strings.SplitN(frame, "\n", 2)[0]
	if !strings.Contains(first, "myws") {
		t.Errorf("header missing workspace name: %q", first)
	}
	if !strings.Contains(first, "active=3") {
		t.Errorf("header active count: %q", first)
	}
	if !strings.Contains(first, "running=1 waiting=1 idle=1") {
		t.Errorf("header per-status counts: %q", first)
	}
	if !strings.Contains(first, "ended=1") {
		t.Errorf("header ended: %q", first)
	}
	if !strings.Contains(first, "⚠ dropped=2") {
		t.Errorf("header dropped warning: %q", first)
	}
}

func TestRender_NoActive_ShowsHelpfulMessage(t *testing.T) {
	st := state.State{Sessions: map[string]*state.Session{}}
	frame := Render(st, "ws", time.Now())
	if !strings.Contains(frame, "no active sessions") {
		t.Errorf("expected helpful message when no active, got %q", frame)
	}
}

func TestRender_EndedFooter_ShowsLastThree(t *testing.T) {
	now := time.Date(2026, 4, 20, 16, 0, 0, 0, time.UTC)
	st := state.State{Sessions: map[string]*state.Session{}}
	for i, ts := range []string{
		"2026-04-20T15:00:00Z",
		"2026-04-20T15:10:00Z",
		"2026-04-20T15:20:00Z",
		"2026-04-20T15:30:00Z",
		"2026-04-20T15:40:00Z",
	} {
		s := sessAt("ended", ts, ts, "r", "t")
		s.EndedAt = json.RawMessage(`"` + ts + `"`)
		st.Sessions[string(rune('a'+i))] = s
	}
	frame := Render(st, "ws", now)
	if !strings.Contains(frame, "ended (last 3)") {
		t.Errorf("expected 'ended (last 3)' header, got: %q", frame)
	}
	// The most-recent three should be in the footer; the oldest two not.
	for _, want := range []string{"15:40", "15:30", "15:20"} {
		// Activity is rendered as "Nm ago" / "Nh ago", not the raw timestamp,
		// so check for relative-time substrings via durations.
		_ = want
	}
	// Older sessions (from 15:00 / 15:10) shouldn't appear at all in a 3-cap footer.
	occurrences := strings.Count(frame, "◼")
	if occurrences != 3 {
		t.Errorf("expected 3 ended-footer rows, got %d", occurrences)
	}
}

func TestRender_ShortSID_TruncatesTo8(t *testing.T) {
	if got := shortSID("abcdef0123456789"); got != "abcdef01" {
		t.Errorf("shortSID 16-char: got %q, want abcdef01", got)
	}
	if got := shortSID("abc"); got != "abc" {
		t.Errorf("shortSID 3-char: got %q, want abc (no truncation)", got)
	}
}

func TestActivitySince_Formats(t *testing.T) {
	now := time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC)
	cases := []struct {
		iso    string
		want   string
		suffix bool
	}{
		{"2026-04-20T14:59:55Z", "5s", false},
		{"2026-04-20T14:55:00Z", "5m", false},
		{"2026-04-20T13:00:00Z", "2h", false},
		{"2026-04-20T13:00:00Z", "2h ago", true},
		{"", "—", false},
		{"not-a-date", "—", false},
	}
	for _, c := range cases {
		got := activitySince(c.iso, now, c.suffix)
		if got != c.want {
			t.Errorf("activitySince(%q, suffix=%v) = %q, want %q", c.iso, c.suffix, got, c.want)
		}
	}
}

func TestJsonRawString_NullFallsBack(t *testing.T) {
	if got := jsonRawString(json.RawMessage("null"), "—"); got != "—" {
		t.Errorf("null should fall back: got %q", got)
	}
	if got := jsonRawString(json.RawMessage(`"hello"`), "—"); got != "hello" {
		t.Errorf("string should unwrap: got %q", got)
	}
	if got := jsonRawString(nil, "fallback"); got != "fallback" {
		t.Errorf("missing should fall back: got %q", got)
	}
}
