package dashboard

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

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
	if !strings.Contains(first, "▶1") || !strings.Contains(first, "●1") || !strings.Contains(first, "◯1") {
		t.Errorf("header per-status glyphs: %q", first)
	}
	if !strings.Contains(first, "ended=1") {
		t.Errorf("header ended: %q", first)
	}
	if !strings.Contains(frame, "⚠ 2 malformed events skipped") {
		t.Errorf("dropped warning line: %q", frame)
	}
	if w := utf8.RuneCountInString(first); w > 80 {
		t.Errorf("header should fit in 80 cols, got %d: %q", w, first)
	}
}

func TestRender_AllLinesFitDashboardPaneWidth(t *testing.T) {
	const paneWidth = 80
	st := state.State{
		Sessions: map[string]*state.Session{
			"abcdef0123456": {
				Status:               state.StatusWaitingInput,
				StartedAt:            "2026-04-20T15:00:00Z",
				LastActivity:         "2026-04-20T15:00:00Z",
				PrimaryRepo:          json.RawMessage(`"infrastructure"`),
				TaskName:             json.RawMessage(`"refactor a really long task name that exceeds the cap"`),
				DeclaredRelatedRepos: json.RawMessage("[]"),
				Cwd:                  json.RawMessage("null"),
				PaneID:               json.RawMessage("null"),
				LastPromptPreview:    json.RawMessage("null"),
			},
		},
		DroppedEvents: 12345,
	}
	frame := Render(st, "very-long-workspace-name", time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC))
	for i, line := range strings.Split(frame, "\n") {
		if w := utf8.RuneCountInString(line); w > paneWidth {
			t.Errorf("line %d (%d cols): %q", i, w, line)
		}
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

func TestRender_EndedFooter_DropsAncient(t *testing.T) {
	// Three sessions ended: one 2h ago (fresh), one 23h ago (still in window),
	// one 25h ago (past EndedFooterMaxAge → must drop). Expected footer rows: 2.
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	st := state.State{Sessions: map[string]*state.Session{}}
	cases := []struct {
		key string
		ts  string
	}{
		{"a", "2026-04-21T10:00:00Z"}, // 2h ago
		{"b", "2026-04-20T13:00:00Z"}, // 23h ago
		{"c", "2026-04-20T11:00:00Z"}, // 25h ago → drop
	}
	for _, c := range cases {
		s := sessAt("ended", c.ts, c.ts, "r", "t")
		s.EndedAt = json.RawMessage(`"` + c.ts + `"`)
		st.Sessions[c.key] = s
	}
	frame := Render(st, "ws", now)
	if !strings.Contains(frame, "ended (last 2)") {
		t.Errorf("expected 'ended (last 2)' (one drop), got: %q", frame)
	}
	if got := strings.Count(frame, "◼"); got != 2 {
		t.Errorf("expected 2 ended-footer rows, got %d", got)
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

func TestRender_CommandsFooter_AlwaysVisible(t *testing.T) {
	// The cheatsheet should appear whether the workspace has sessions or not.
	empty := state.State{Sessions: map[string]*state.Session{}}
	withSession := state.State{
		Sessions: map[string]*state.Session{
			"x": sessAt("running", "2026-04-20T15:00:00Z", "2026-04-20T15:00:00Z", "api", "task"),
		},
	}
	now := time.Date(2026, 4, 20, 15, 0, 5, 0, time.UTC)
	for label, st := range map[string]state.State{"empty": empty, "with-session": withSession} {
		frame := Render(st, "ws", now)
		if !strings.Contains(frame, "start [<repo>] <task>") {
			t.Errorf("[%s] cheatsheet missing the start example: %q", label, frame)
		}
		if !strings.Contains(frame, "start-fleet <repo>") {
			t.Errorf("[%s] cheatsheet missing the start-fleet example: %q", label, frame)
		}
		if !strings.Contains(frame, "end <prefix>") {
			t.Errorf("[%s] cheatsheet missing the end example: %q", label, frame)
		}
		if !strings.Contains(frame, "control") {
			t.Errorf("[%s] cheatsheet should reference the control pane: %q", label, frame)
		}
	}
}

func TestRender_StaleFlag_OnRunningOnly(t *testing.T) {
	now := time.Date(2026, 4, 20, 16, 0, 0, 0, time.UTC)
	// 20 minutes ago — past StaleAfter (15m).
	staleTS := "2026-04-20T15:40:00Z"
	// 5 minutes ago — under threshold.
	freshTS := "2026-04-20T15:55:00Z"

	cases := []struct {
		name        string
		status      string
		lastAct     string
		wantStaleQ  bool
	}{
		{"running + old", state.StatusRunning, staleTS, true},
		{"running + fresh", state.StatusRunning, freshTS, false},
		{"waiting_input + old", state.StatusWaitingInput, staleTS, false},
		{"idle + old", state.StatusIdle, staleTS, false},
	}
	for _, c := range cases {
		s := sessAt(c.status, "2026-04-20T14:00:00Z", c.lastAct, "api", "fix bug")
		got := shortStatusWithStale(s, now)
		hasQ := strings.HasSuffix(got, "?")
		if hasQ != c.wantStaleQ {
			t.Errorf("%s: shortStatusWithStale=%q, hasQ=%v, want=%v", c.name, got, hasQ, c.wantStaleQ)
		}
	}
}

func TestRenderMulti_IncludesWSColumn(t *testing.T) {
	now := time.Date(2026, 4, 20, 15, 0, 30, 0, time.UTC)
	samples := []TaggedState{
		{
			Name: "ws-alpha",
			State: state.State{
				Sessions: map[string]*state.Session{
					"sid-a": sessAt("running", "2026-04-20T15:00:00Z", "2026-04-20T15:00:00Z", "api", "fix"),
				},
			},
		},
		{
			Name: "ws-beta",
			State: state.State{
				Sessions: map[string]*state.Session{
					"sid-b": sessAt("waiting_input", "2026-04-20T15:00:10Z", "2026-04-20T15:00:10Z", "web", "ship"),
				},
			},
		},
	}
	frame := RenderMulti(samples, "watch · 2 workspace(s)", now)
	if !strings.Contains(frame, "STATUS") || !strings.Contains(frame, "WS") {
		t.Errorf("expected WS column header in multi render, got:\n%s", frame)
	}
	if !strings.Contains(frame, "ws-alpha") || !strings.Contains(frame, "ws-beta") {
		t.Errorf("expected both workspace names in rows, got:\n%s", frame)
	}
	if !strings.Contains(frame, "watch · 2 workspace(s)") {
		t.Errorf("expected aggregate title in header, got:\n%s", frame)
	}
	// header active count: 1 running + 1 waiting = 2.
	if !strings.Contains(frame, "active=2") {
		t.Errorf("expected active=2 in header, got:\n%s", frame)
	}
}

func TestRenderMulti_NoSessions(t *testing.T) {
	frame := RenderMulti(nil, "watch · 0 workspace(s)", time.Now())
	if !strings.Contains(frame, "no active sessions across any workspace") {
		t.Errorf("expected friendly empty message, got:\n%s", frame)
	}
}

func TestRender_SingleWorkspaceHasNoWSColumn(t *testing.T) {
	// Sanity check: the existing single-workspace render must not regress
	// by accidentally adding the WS column from the multi path.
	st := state.State{
		Sessions: map[string]*state.Session{
			"sid-a": sessAt("running", "2026-04-20T15:00:00Z", "2026-04-20T15:00:00Z", "api", "fix"),
		},
	}
	frame := Render(st, "ws", time.Date(2026, 4, 20, 15, 0, 5, 0, time.UTC))
	// Header row in single mode: STATUS  SID  REPO  TASK  ACT (no WS).
	headerLine := strings.Split(frame, "\n")[2]
	if strings.Contains(headerLine, "\tWS\t") || strings.Contains(headerLine, " WS ") {
		t.Errorf("single Render should not include WS column, got header: %q", headerLine)
	}
}

func TestEndedFooter_StableSortAcrossEqualEndedAt(t *testing.T) {
	// Two ended sessions share the same EndedAt (e.g. both reaped in the
	// same wall-clock second). Without the sid tiebreaker, map iteration
	// randomness would flip their order between renders. Test asserts
	// deterministic ordering by sid.
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	tEq := "2026-05-17T11:50:00Z" // 10m ago
	st := state.State{Sessions: map[string]*state.Session{}}
	for _, sid := range []string{"zzz999", "aaa111", "mmm555"} {
		s := sessAt("ended", tEq, tEq, "repo", "task-"+sid)
		s.EndedAt = json.RawMessage(`"` + tEq + `"`)
		st.Sessions[sid] = s
	}
	// Render 10 times; collect SID column order from each frame's ended rows.
	var firstSeen []string
	for i := 0; i < 10; i++ {
		frame := Render(st, "ws", now)
		var order []string
		for _, line := range strings.Split(frame, "\n") {
			if strings.HasPrefix(line, "  ◼ ") {
				// "  ◼ zzz999..." → split on whitespace, take field 1 (the sid).
				parts := strings.Fields(line)
				if len(parts) > 1 {
					order = append(order, parts[1])
				}
			}
		}
		if i == 0 {
			firstSeen = order
			continue
		}
		if !reflect.DeepEqual(order, firstSeen) {
			t.Errorf("ended order drifted on tick %d: first=%v this=%v", i, firstSeen, order)
		}
	}
	// Confirm the deterministic order is sid-ascending (aaa, mmm, zzz).
	want := []string{"aaa111", "mmm555", "zzz999"}
	if !reflect.DeepEqual(firstSeen, want) {
		t.Errorf("expected sid-asc tiebreaker order %v, got %v", want, firstSeen)
	}
}

func TestMultiEndedFooter_StableSortAcrossEqualEndedAt(t *testing.T) {
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	tEq := "2026-05-17T11:50:00Z"
	mk := func(sid string) *state.Session {
		s := sessAt("ended", tEq, tEq, "repo", "t")
		s.EndedAt = json.RawMessage(`"` + tEq + `"`)
		return s
	}
	samples := []TaggedState{
		{
			Name: "alpha",
			State: state.State{
				Sessions: map[string]*state.Session{
					"zzz999": mk("zzz999"),
					"aaa111": mk("aaa111"),
				},
			},
		},
	}
	var firstSeen string
	for i := 0; i < 10; i++ {
		frame := RenderMulti(samples, "watch", now)
		if i == 0 {
			firstSeen = frame
			continue
		}
		if frame != firstSeen {
			t.Errorf("multi ended-footer frame drifted on tick %d", i)
		}
	}
}

func TestWatchFooter_HasExpectedCheatsheetEntries(t *testing.T) {
	// The watch footer doubles as the operator's cheatsheet. Assert each
	// listed verb is present so renames have to update the test (and the
	// docs in lockstep).
	footer := renderWatchFooter()
	wants := []string{
		"end <prefix>",
		"end all-non-ended --yes",
		"reap [--older-than DUR]",
		"open ",
		"close <ws> --yes",
		"Ctrl-C",
		"legend: `?` after status",
	}
	for _, w := range wants {
		if !strings.Contains(footer, w) {
			t.Errorf("watch footer missing %q\nFooter:\n%s", w, footer)
		}
	}
	// Every line ≤ 80 cols (dashboard pane width contract).
	for i, line := range strings.Split(footer, "\n") {
		if len(line) > 80 {
			t.Errorf("watch footer line %d > 80 cols (%d): %q", i, len(line), line)
		}
	}
}

func TestEndedAgo_MinutePrecisionAndLessThanOneMinute(t *testing.T) {
	now := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		iso  string
		want string
	}{
		{"2026-04-21T11:59:59Z", "<1m ago"}, // 1s
		{"2026-04-21T11:59:30Z", "<1m ago"}, // 30s
		{"2026-04-21T11:59:00Z", "1m ago"},  // exactly 1m
		{"2026-04-21T11:55:00Z", "5m ago"},
		{"2026-04-21T11:00:00Z", "1h ago"},
		{"2026-04-20T12:00:00Z", "24h ago"},
		{"", "—"},
		{"not-a-date", "—"},
	}
	for _, c := range cases {
		got := endedAgo(c.iso, now)
		if got != c.want {
			t.Errorf("endedAgo(%q) = %q, want %q", c.iso, got, c.want)
		}
	}
}

func TestEndedFooter_StableAcrossSubMinuteTicks(t *testing.T) {
	// Two ticks 500ms apart on an ended-15s-ago session must produce the
	// same frame — that's the whole point of this fix (no per-second
	// repaints triggered by ended rows).
	t0 := time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC)
	st := state.State{Sessions: map[string]*state.Session{}}
	s := sessAt("ended", "2026-04-21T11:59:45Z", "2026-04-21T11:59:45Z", "api", "task")
	s.EndedAt = json.RawMessage(`"2026-04-21T11:59:45Z"`)
	st.Sessions["a"] = s

	f0 := Render(st, "ws", t0)
	f1 := Render(st, "ws", t0.Add(500*time.Millisecond))
	if f0 != f1 {
		t.Errorf("ended footer should be stable within a minute; diff:\n--- t0 ---\n%s\n--- t0+500ms ---\n%s", f0, f1)
	}
}

func TestSessionRepoLabel_PrimaryRepoWins(t *testing.T) {
	s := &state.Session{
		PrimaryRepo: json.RawMessage(`"api"`),
		Cwd:         json.RawMessage(`"/home/u/elsewhere"`),
	}
	if got := sessionRepoLabel(s); got != "api" {
		t.Errorf("got %q, want api", got)
	}
}

func TestSessionRepoLabel_FallsBackToCwdBasename(t *testing.T) {
	s := &state.Session{
		PrimaryRepo: json.RawMessage(`""`),
		Cwd:         json.RawMessage(`"/home/u/projects/api"`),
	}
	if got := sessionRepoLabel(s); got != "api" {
		t.Errorf("got %q, want api (from cwd basename)", got)
	}
}

func TestSessionRepoLabel_NullEverything_ReturnsDash(t *testing.T) {
	s := &state.Session{
		PrimaryRepo: json.RawMessage("null"),
		Cwd:         json.RawMessage("null"),
	}
	if got := sessionRepoLabel(s); got != "—" {
		t.Errorf("got %q, want em-dash", got)
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
