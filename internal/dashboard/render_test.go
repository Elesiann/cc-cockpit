package dashboard

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/elesiann/cc-cockpit/internal/state"
	"github.com/elesiann/cc-cockpit/internal/subagent"
)

// sessAt builds a fixture Session. The `repo` argument becomes the basename
// of Cwd so sessionRepoLabel produces it. `task` is kept in the signature
// for callsite readability but is no longer stored — the dashboard sources
// task labels from /rename metadata, not from the Session itself.
func sessAt(status, started, lastAct string, repo, task string) *state.Session {
	_ = task
	return &state.Session{
		Status:            status,
		StartedAt:         started,
		LastActivity:      lastAct,
		Cwd:               json.RawMessage(`"/fixture/` + repo + `"`),
		LastPromptPreview: json.RawMessage("null"),
	}
}

func renderOne(st state.State, now time.Time) string {
	return RenderMulti([]TaggedState{{Name: "ws", State: st}}, "watch · 1 workspace(s)", now, nil, nil, nil)
}

func renderOneWithMetas(st state.State, now time.Time, metas map[string]SessionMeta) string {
	return RenderMulti([]TaggedState{{Name: "ws", State: st}}, "watch · 1 workspace(s)", now, metas, nil, nil)
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
	frame := renderOne(st, time.Date(2026, 4, 20, 15, 0, 30, 0, time.UTC))
	first := strings.SplitN(frame, "\n", 2)[0]
	if !strings.Contains(first, "watch · 1 workspace(s)") {
		t.Errorf("header missing aggregate title: %q", first)
	}
	if !strings.Contains(first, "active=3") {
		t.Errorf("header active count: %q", first)
	}
	// Header consolidates the 7 reducer statuses into 3 buckets: busy / wait /
	// idle (see statusBucket). Fixture has 1 running + 1 waiting_input + 1 idle
	// → expect 1 in each bucket emoji.
	if !strings.Contains(first, "🔧 1") || !strings.Contains(first, "⏸️ 1") || !strings.Contains(first, "💤 1") {
		t.Errorf("header per-bucket emoji (expected `🔧 1 ⏸️ 1 💤 1`): %q", first)
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
				Status:            state.StatusWaitingInput,
				StartedAt:         "2026-04-20T15:00:00Z",
				LastActivity:      "2026-04-20T15:00:00Z",
				Cwd:               json.RawMessage(`"/fixture/infrastructure"`),
				LastPromptPreview: json.RawMessage("null"),
			},
		},
		DroppedEvents: 12345,
	}
	frame := renderOne(st, time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC))
	for i, line := range strings.Split(frame, "\n") {
		if w := utf8.RuneCountInString(line); w > paneWidth {
			t.Errorf("line %d (%d cols): %q", i, w, line)
		}
	}
}

func TestRender_NoActive_ShowsHelpfulMessage(t *testing.T) {
	st := state.State{Sessions: map[string]*state.Session{}}
	frame := renderOne(st, time.Now())
	if !strings.Contains(frame, "no active sessions") {
		t.Errorf("expected helpful message when no active, got %q", frame)
	}
	if !strings.Contains(frame, "─── active (0) ───") {
		t.Errorf("expected section marker even when empty, got %q", frame)
	}
}

func TestRender_ActiveSectionMarker_ShowsCount(t *testing.T) {
	st := state.State{
		Sessions: map[string]*state.Session{
			"sid-a": sessAt("running", "2026-05-17T11:59:00Z", "2026-05-17T11:59:30Z", "api", "task-a"),
			"sid-b": sessAt("idle", "2026-05-17T11:59:01Z", "2026-05-17T11:59:31Z", "web", "task-b"),
		},
	}
	frame := renderOne(st, time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC))
	if !strings.Contains(frame, "─── active (2) ───") {
		t.Errorf("expected active section marker with count, got:\n%s", frame)
	}
	// Data rows should be indented (consistent with ended footer style).
	// Status uses emoji glyphs now: 🔧 (running) or 💤 (idle).
	if !strings.Contains(frame, "  🔧 ") && !strings.Contains(frame, "  💤 ") {
		t.Errorf("expected indented data rows with emoji glyph, got:\n%s", frame)
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
	frame := renderOne(st, now)
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
	frame := renderOne(st, now)
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
		frame := renderOne(st, now)
		if !strings.Contains(frame, "end <prefix>") {
			t.Errorf("[%s] cheatsheet missing the end example: %q", label, frame)
		}
		if !strings.Contains(frame, "reap [--older-than DUR]") {
			t.Errorf("[%s] cheatsheet missing the reap example: %q", label, frame)
		}
		if strings.Contains(frame, "control") || strings.Contains(frame, "start-fleet") {
			t.Errorf("[%s] cheatsheet should not reference removed tmux controls: %q", label, frame)
		}
	}
}

func TestRender_StaleFlag_OnMidTurnStatesOnly(t *testing.T) {
	now := time.Date(2026, 4, 20, 16, 0, 0, 0, time.UTC)
	// 20 minutes ago — past StaleAfter (15m).
	staleTS := "2026-04-20T15:40:00Z"
	// 5 minutes ago — under threshold.
	freshTS := "2026-04-20T15:55:00Z"

	cases := []struct {
		name       string
		status     string
		lastAct    string
		wantStaleQ bool
	}{
		// Mid-turn states (we expect events to keep flowing) — quiet = suspect.
		{"running + old", state.StatusRunning, staleTS, true},
		{"running + fresh", state.StatusRunning, freshTS, false},
		{"thinking + old", state.StatusThinking, staleTS, true},
		{"processing + old", state.StatusProcessing, staleTS, true},
		// Stable / steady-state — quiet is by design, no flag.
		{"waiting_input + old", state.StatusWaitingInput, staleTS, false},
		{"completed + old", state.StatusCompleted, staleTS, false},
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

func TestRender_CompletedDecaysToIdleAfter10Min(t *testing.T) {
	// `completed` is a reducer status; the render layer rewrites it to `idle`
	// once LastActivity is older than IdleAfterCompleted (10m). The reducer
	// itself stays event-pure — only the display changes.
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	fresh := sessAt(state.StatusCompleted, "2026-05-17T11:55:00Z", "2026-05-17T11:55:00Z", "api", "t") // 5m
	old := sessAt(state.StatusCompleted, "2026-05-17T11:45:00Z", "2026-05-17T11:45:00Z", "api", "t")   // 15m

	if got := effectiveStatus(fresh, now); got != state.StatusCompleted {
		t.Errorf("fresh completed (5m): got %q, want completed (still within 10m grace)", got)
	}
	if got := effectiveStatus(old, now); got != state.StatusIdle {
		t.Errorf("aged completed (15m): got %q, want idle (past IdleAfterCompleted)", got)
	}
	// Glyph follows the effective status — aged-completed gets 💤, not ✅.
	if g := glyph(effectiveStatus(old, now)); g != "💤" {
		t.Errorf("aged completed glyph: got %q, want 💤", g)
	}
}

func TestRender_RunningShowsCurrentToolInstead(t *testing.T) {
	// When a tool is mid-execution the STATUS column shows the tool name
	// (the 🔧 glyph already conveys "running tool"), e.g. `🔧 Bash`.
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	s := sessAt(state.StatusRunning, "2026-05-17T11:59:55Z", "2026-05-17T11:59:55Z", "api", "task")
	s.CurrentTool = "Bash"

	st := state.State{Sessions: map[string]*state.Session{"sid-a": s}}
	frame := renderOne(st, now)
	if !strings.Contains(frame, "🔧 Bash") {
		t.Errorf("expected `🔧 Bash` in frame for running+tool session, got:\n%s", frame)
	}
	// And the generic `running` word should NOT appear in that row.
	if strings.Contains(frame, "🔧 running") {
		t.Errorf("running+tool row should not say `running` — tool name replaces it:\n%s", frame)
	}
}

func TestRender_RunningWithoutToolFallsBackToGenericLabel(t *testing.T) {
	// Edge case: status=running but CurrentTool empty (e.g. session revived
	// from a synthetic SessionEnd before any PreToolUse fired). The STATUS
	// column shows the generic word so the row isn't bare.
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	s := sessAt(state.StatusRunning, "2026-05-17T11:59:55Z", "2026-05-17T11:59:55Z", "api", "task")
	st := state.State{Sessions: map[string]*state.Session{"sid-a": s}}
	frame := renderOne(st, now)
	if !strings.Contains(frame, "🔧 running") {
		t.Errorf("expected `🔧 running` fallback when CurrentTool is empty, got:\n%s", frame)
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
	frame := RenderMulti(samples, "watch · 2 workspace(s)", now, nil, nil, nil)
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

func TestRenderMulti_NoWorkspaces_ShowsFirstTimeInstallHint(t *testing.T) {
	// nil samples means we didn't find any state dirs at all — typically a
	// brand-new install where Claude Code hooks haven't been wired up. The
	// hint must mention `cc-cockpit install` so the user knows the next
	// step (without making the running watch command itself feel broken).
	frame := RenderMulti(nil, "watch · 0 workspace(s)", time.Now(), nil, nil, nil)
	if !strings.Contains(frame, "cc-cockpit install") {
		t.Errorf("expected first-time install hint, got:\n%s", frame)
	}
	if strings.Contains(frame, "no active sessions across any workspace") {
		t.Errorf("first-time message should differ from steady-state empty message:\n%s", frame)
	}
}

func TestRenderMulti_WorkspacesPresentButNoSessions_ShowsSteadyStateMessage(t *testing.T) {
	// At least one state dir exists (Claude has been observed before) but
	// nothing is live right now. Keep the original message — it would be
	// misleading to claim hooks aren't installed.
	samples := []TaggedState{{Name: "ws-1", State: state.State{Sessions: map[string]*state.Session{}}}}
	frame := RenderMulti(samples, "watch · 1 workspace(s)", time.Now(), nil, nil, nil)
	if !strings.Contains(frame, "no active sessions across any workspace") {
		t.Errorf("expected steady-state empty message, got:\n%s", frame)
	}
	if strings.Contains(frame, "cc-cockpit install") {
		t.Errorf("install hint must NOT appear when workspaces exist:\n%s", frame)
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
		frame := renderOne(st, now)
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
		frame := RenderMulti(samples, "watch", now, nil, nil, nil)
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
		"Ctrl-C",
		"legend:",
		"? = stale",
	}
	for _, w := range wants {
		if !strings.Contains(footer, w) {
			t.Errorf("watch footer missing %q\nFooter:\n%s", w, footer)
		}
	}
	for _, removed := range []string{"open ", "close <ws> --yes", "start-fleet"} {
		if strings.Contains(footer, removed) {
			t.Errorf("watch footer still mentions removed command %q\nFooter:\n%s", removed, footer)
		}
	}
	// Every line ≤ 80 visible cols. utf8.RuneCountInString matches the rest
	// of this test file's convention for terminal width (and treats each
	// emoji as 1 rune — close enough since the legend lives below the table
	// where exact alignment doesn't matter).
	for i, line := range strings.Split(footer, "\n") {
		if w := utf8.RuneCountInString(line); w > 80 {
			t.Errorf("watch footer line %d > 80 cols (%d): %q", i, w, line)
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

	f0 := renderOne(st, t0)
	f1 := renderOne(st, t0.Add(500*time.Millisecond))
	if f0 != f1 {
		t.Errorf("ended footer should be stable within a minute; diff:\n--- t0 ---\n%s\n--- t0+500ms ---\n%s", f0, f1)
	}
}

func TestSessionTaskLabel_UsesRenameMeta(t *testing.T) {
	got := sessionTaskLabel(&state.Session{}, SessionMeta{Name: "my-rename"})
	if got != "my-rename" {
		t.Errorf("got %q, want my-rename", got)
	}
}

func TestSessionTaskLabel_NoMetaReturnsDash(t *testing.T) {
	got := sessionTaskLabel(&state.Session{}, SessionMeta{})
	if got != "—" {
		t.Errorf("got %q, want em-dash", got)
	}
}

func TestRenderMulti_AppliesNameAndColor(t *testing.T) {
	now := time.Date(2026, 5, 17, 12, 0, 0, 0, time.UTC)
	st := state.State{
		Sessions: map[string]*state.Session{
			"abcd1234": sessAt("running", "2026-05-17T11:59:50Z", "2026-05-17T11:59:55Z", "api", "old task"),
		},
	}
	metas := map[string]SessionMeta{
		"abcd1234": {Name: "renamed", Color: "red"},
	}
	frame := renderOneWithMetas(st, now, metas)
	if !strings.Contains(frame, "renamed") {
		t.Errorf("expected `renamed` in TASK column, got:\n%s", frame)
	}
	if strings.Contains(frame, "old task") {
		t.Errorf("/rename should be the only task source, raw task leaked:\n%s", frame)
	}
	if !strings.Contains(frame, "\033[31m") || !strings.Contains(frame, "\033[0m") {
		t.Errorf("expected ANSI red wrap on data row, got:\n%s", frame)
	}
}

func TestRenderMultiWithAgentRollups_ShowsOneSubtleLinePerParent(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	samples := []TaggedState{{
		Name: "gio",
		State: state.State{Sessions: map[string]*state.Session{
			"parent-session-1": sessAt(state.StatusWaitingInput, "2026-05-20T11:00:00Z", "2026-05-20T11:59:00Z", "api", "last-mile"),
			"parent-session-2": sessAt(state.StatusProcessing, "2026-05-20T11:01:00Z", "2026-05-20T11:59:30Z", "web", "review"),
		}},
	}}

	frame := RenderMulti(samples, "watch", now, nil, nil, map[string]subagent.Rollup{
		"parent-session-1": {Total: 3, Active: 1, Done: 2, LatestDescription: "Design duplicate-plugin detection"},
	})

	if !strings.Contains(frame, "↳ agents: 1 active · 2 done · latest: Design duplicate-plugin detection") {
		t.Fatalf("agent rollup missing or wrong:\n%s", frame)
	}
	if strings.Count(frame, "↳ agents:") != 1 {
		t.Fatalf("expected exactly one compact agent line, got frame:\n%s", frame)
	}
	if strings.Contains(frame, "Explore devportal-distro") {
		t.Fatalf("should not expand individual subagents in MVP:\n%s", frame)
	}
}

func TestRenderMultiWithRecaps_ShowsSubtleOneLineRecapOnlyForIdleSessions(t *testing.T) {
	now := time.Date(2026, 5, 20, 12, 0, 0, 0, time.UTC)
	samples := []TaggedState{{
		Name: "gio",
		State: state.State{Sessions: map[string]*state.Session{
			"idle1234":    sessAt(state.StatusIdle, "2026-05-20T11:40:00Z", "2026-05-20T11:45:00Z", "devportal-platform", "last mile"),
			"busy4567":    sessAt(state.StatusProcessing, "2026-05-20T11:50:00Z", "2026-05-20T11:59:00Z", "cc-cockpit", "render recap"),
			"waiting8901": sessAt(state.StatusWaitingInput, "2026-05-20T11:55:00Z", "2026-05-20T11:59:30Z", "api", "needs input"),
		}},
	}}
	recaps := map[string]string{
		"idle1234":    "Goal: clear migration pendencies. Done: matrix-sync PR #29 and Notion §7 results. Next: decide whether UPGRADING fixes fold into plugin-clarity.",
		"busy4567":    "Busy recap must not render.",
		"waiting8901": "Waiting recap must not render.",
	}

	frame := RenderMulti(samples, "watch", now, nil, recaps, nil)
	if !strings.Contains(frame, "\033[90m    ↳ recap: Goal: clear migration pendencies.") {
		t.Fatalf("expected subtle gray one-line recap under idle session, got:\n%s", frame)
	}
	if strings.Contains(frame, "  │ ") {
		t.Fatalf("recap should not expand into continuation lines:\n%s", frame)
	}
	if strings.Contains(frame, "Busy recap") || strings.Contains(frame, "Waiting recap") {
		t.Fatalf("recaps should render only for idle sessions, got:\n%s", frame)
	}
}

func TestSessionRepoLabel_UsesCwdBasename(t *testing.T) {
	s := &state.Session{Cwd: json.RawMessage(`"/home/u/projects/api"`)}
	if got := sessionRepoLabel(s); got != "api" {
		t.Errorf("got %q, want api (from cwd basename)", got)
	}
}

func TestSessionRepoLabel_NullCwd_ReturnsDash(t *testing.T) {
	s := &state.Session{Cwd: json.RawMessage("null")}
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
