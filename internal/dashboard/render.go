package dashboard

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/elesiann/cc-cockpit/internal/state"
	"github.com/elesiann/cc-cockpit/internal/subagent"
)

// StaleAfter is how long a mid-turn session (running / thinking / processing)
// may go without any new event before the dashboard flags it as
// probably-crashed. waiting_input / completed / idle are quiet-by-design
// states, so flagging them adds no signal.
const StaleAfter = 15 * time.Minute

// IdleAfterCompleted is how long a `completed` session can stay quiet before
// the dashboard re-labels it as `idle` (purely a render-time derivation —
// the reducer never writes "idle" except on a fresh SessionStart). Picked
// at 10 min so a Claude waiting for the next prompt loses the green "✅"
// signal once it's clearly settled, but still shows "completed" for a turn
// the user just wrapped up.
const IdleAfterCompleted = 10 * time.Minute

// EndedFooterMaxAge drops ended-session rows older than this from the
// "ended (last N)" footer so the dashboard doesn't keep day-old corpses
// in view. The full history is still preserved in events.jsonl.
const EndedFooterMaxAge = 24 * time.Hour

// Render produces the dashboard frame for st as of now (single-workspace
// view: no WS column). Uses default rendering (no /rename or /color
// metadata). Callers that want those should use RenderWithMetas.
func Render(st state.State, workspaceName string, now time.Time) string {
	return RenderWithMetas(st, workspaceName, now, nil)
}

// RenderWithMetas is Render plus a sessionId → SessionMeta lookup. When the
// map carries a Name for a session, it overrides the TASK column. When it
// carries a Color, the row is wrapped in the matching ANSI escape so the
// user's /color choice surfaces visually.
func RenderWithMetas(st state.State, workspaceName string, now time.Time, metas map[string]SessionMeta) string {
	return RenderWithMetasAndRecaps(st, workspaceName, now, metas, nil)
}

// RenderWithMetasAndRecaps is RenderWithMetas plus optional native Claude Code
// recaps keyed by session_id. Missing recaps are omitted — no placeholder — so
// the dashboard only spends vertical space on signal that already exists.
func RenderWithMetasAndRecaps(st state.State, workspaceName string, now time.Time, metas map[string]SessionMeta, recaps map[string]string) string {
	return RenderWithMetasRecapsAndAgents(st, workspaceName, now, metas, recaps, nil)
}

// RenderWithMetasRecapsAndAgents is RenderWithMetasAndRecaps plus optional
// parent session_id → subagent rollup summaries.
func RenderWithMetasRecapsAndAgents(st state.State, workspaceName string, now time.Time, metas map[string]SessionMeta, recaps map[string]string, agents map[string]subagent.Rollup) string {
	var b strings.Builder
	b.WriteString(renderHeader(st, workspaceName))
	b.WriteString("\n\n")
	b.WriteString(renderActiveTable(st, now, metas, recaps, agents))
	if footer := renderEndedFooter(st, now); footer != "" {
		b.WriteString("\n\n")
		b.WriteString(footer)
	}
	b.WriteString("\n\n")
	b.WriteString(renderCommandsFooter())
	return b.String()
}

// RenderMulti renders aggregated samples for `watch` mode. Adds a WS column
// and a header that summarizes across all workspaces. Same metas behavior
// as Render (no metas → default rendering).
func RenderMulti(samples []TaggedState, title string, now time.Time) string {
	return RenderMultiWithMetas(samples, title, now, nil)
}

// RenderMultiWithMetas is RenderMulti plus /rename + /color metadata.
func RenderMultiWithMetas(samples []TaggedState, title string, now time.Time, metas map[string]SessionMeta) string {
	return RenderMultiWithMetasAndRecaps(samples, title, now, metas, nil)
}

// RenderMultiWithMetasAndRecaps is RenderMultiWithMetas plus optional native
// Claude Code recaps keyed by session_id.
func RenderMultiWithMetasAndRecaps(samples []TaggedState, title string, now time.Time, metas map[string]SessionMeta, recaps map[string]string) string {
	return RenderMultiWithMetasRecapsAndAgents(samples, title, now, metas, recaps, nil)
}

// RenderMultiWithMetasRecapsAndAgents is RenderMultiWithMetasAndRecaps plus
// optional parent session_id → subagent rollup summaries.
func RenderMultiWithMetasRecapsAndAgents(samples []TaggedState, title string, now time.Time, metas map[string]SessionMeta, recaps map[string]string, agents map[string]subagent.Rollup) string {
	var b strings.Builder
	b.WriteString(renderMultiHeader(samples, title))
	b.WriteString("\n\n")
	b.WriteString(renderMultiActiveTable(samples, now, metas, recaps, agents))
	if footer := renderMultiEndedFooter(samples, now); footer != "" {
		b.WriteString("\n\n")
		b.WriteString(footer)
	}
	b.WriteString("\n\n")
	b.WriteString(renderWatchFooter())
	return b.String()
}

// statusBucket maps a granular reducer status (running / thinking /
// processing / waiting / completed / idle) into one of three header buckets:
//
//	busy → running / thinking / processing  (Claude is actively working)
//	wait → waiting_input                    (needs operator attention)
//	idle → completed / idle                 (settled, awaiting next user move)
//
// Keeps the header at 3 emoji counters even though the per-row STATUS column
// shows the full granularity — 7 separate counters would cramp 80-col WSL.
func statusBucket(s string) (busy, wait, idle bool) {
	switch s {
	case state.StatusRunning, state.StatusThinking, state.StatusProcessing:
		return true, false, false
	case state.StatusWaitingInput:
		return false, true, false
	case state.StatusCompleted, state.StatusIdle:
		return false, false, true
	}
	return false, false, false
}

func renderHeader(st state.State, ws string) string {
	var active, busy, wait, idle, ended int
	for _, s := range st.Sessions {
		if s.Status == state.StatusEnded {
			ended++
			continue
		}
		b, w, i := statusBucket(s.Status)
		if b {
			busy++
			active++
		}
		if w {
			wait++
			active++
		}
		if i {
			idle++
			active++
		}
	}
	h := fmt.Sprintf("── %s ──  active=%d  🔧 %d  ⏸️ %d  💤 %d  ended=%d ──",
		truncRunes(ws, 16), active, busy, wait, idle, ended)
	if st.DroppedEvents > 0 {
		h += fmt.Sprintf("\n⚠ %d malformed events skipped", st.DroppedEvents)
	}
	return h
}

func renderMultiHeader(samples []TaggedState, title string) string {
	var active, busy, wait, idle, ended, dropped int
	for _, s := range samples {
		for _, sess := range s.State.Sessions {
			if sess.Status == state.StatusEnded {
				ended++
				continue
			}
			b, w, i := statusBucket(sess.Status)
			if b {
				busy++
				active++
			}
			if w {
				wait++
				active++
			}
			if i {
				idle++
				active++
			}
		}
		dropped += s.State.DroppedEvents
	}
	h := fmt.Sprintf("── %s ──  active=%d  🔧 %d  ⏸️ %d  💤 %d  ended=%d ──",
		truncRunes(title, 32), active, busy, wait, idle, ended)
	if dropped > 0 {
		h += fmt.Sprintf("\n⚠ %d malformed events skipped", dropped)
	}
	return h
}

func renderActiveTable(st state.State, now time.Time, metas map[string]SessionMeta, recaps map[string]string, agents map[string]subagent.Rollup) string {
	type row struct {
		sid  string
		sess *state.Session
	}
	var active []row
	for sid, s := range st.Sessions {
		if s.Status != state.StatusEnded {
			active = append(active, row{sid, s})
		}
	}
	// Tiebreaker on sid so two sessions started in the same second don't
	// swap places between ticks (Go map iteration is randomized; sort.Slice
	// is not stable).
	sort.Slice(active, func(i, j int) bool {
		if active[i].sess.StartedAt != active[j].sess.StartedAt {
			return active[i].sess.StartedAt < active[j].sess.StartedAt
		}
		return active[i].sid < active[j].sid
	})
	if len(active) == 0 {
		return "─── active (0) ───\n  (no active sessions — start [<repo>] <task>)"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "─── active (%d) ───\n", len(active))
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  STATUS\tSID\tREPO\tTASK\tACT")
	sids := make([]string, len(active))
	recapSids := make([]string, len(active))
	for i, r := range active {
		s := r.sess
		// Caps chosen so a worst-case row fits an 80-col dashboard pane after
		// tabwriter padding: indent(2) + status(9) + sid(8) + repo(18) + task(30) +
		// act(5) + 4×2 padding = 80, with breathing room for typical content.
		repo := truncRunes(sessionRepoLabel(s), 18)
		task := truncRunes(sessionTaskLabel(s, metas[r.sid]), 30)
		fmt.Fprintf(tw, "  %s %s\t%s\t%s\t%s\t%s\n",
			glyph(effectiveStatus(s, now)), shortStatusWithStale(s, now),
			shortSID(r.sid),
			repo,
			task,
			activitySince(s.LastActivity, now, false),
		)
		sids[i] = r.sid
		if effectiveStatus(s, now) == state.StatusIdle {
			recapSids[i] = r.sid
		}
	}
	_ = tw.Flush()
	table := colorizeDataRowsWithHeader(b.String(), sids, metas)
	return insertSessionHints(table, sids, recaps, agents, recapSids, 2)
}

func renderMultiActiveTable(samples []TaggedState, now time.Time, metas map[string]SessionMeta, recaps map[string]string, agents map[string]subagent.Rollup) string {
	type row struct {
		sid  string
		ws   string
		sess *state.Session
	}
	var active []row
	for _, s := range samples {
		for sid, sess := range s.State.Sessions {
			if sess.Status != state.StatusEnded {
				active = append(active, row{sid, s.Name, sess})
			}
		}
	}
	sort.Slice(active, func(i, j int) bool {
		if active[i].sess.StartedAt != active[j].sess.StartedAt {
			return active[i].sess.StartedAt < active[j].sess.StartedAt
		}
		return active[i].sid < active[j].sid
	})
	if len(active) == 0 {
		return "─── active (0) ───\n  (no active sessions across any workspace)"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "─── active (%d) ───\n", len(active))
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  STATUS\tSID\tWS\tREPO\tTASK\tACT")
	sids := make([]string, len(active))
	recapSids := make([]string, len(active))
	for i, r := range active {
		s := r.sess
		ws := truncRunes(r.ws, 12)
		repo := truncRunes(sessionRepoLabel(s), 16)
		task := truncRunes(sessionTaskLabel(s, metas[r.sid]), 26)
		fmt.Fprintf(tw, "  %s %s\t%s\t%s\t%s\t%s\t%s\n",
			glyph(effectiveStatus(s, now)), shortStatusWithStale(s, now),
			shortSID(r.sid),
			ws,
			repo,
			task,
			activitySince(s.LastActivity, now, false),
		)
		sids[i] = r.sid
		if effectiveStatus(s, now) == state.StatusIdle {
			recapSids[i] = r.sid
		}
	}
	_ = tw.Flush()
	table := colorizeDataRowsWithHeader(b.String(), sids, metas)
	return insertSessionHints(table, sids, recaps, agents, recapSids, 2)
}

func renderEndedFooter(st state.State, now time.Time) string {
	type row struct {
		sid     string
		sess    *state.Session
		sortKey string
	}
	var ended []row
	for sid, s := range st.Sessions {
		if s.Status != state.StatusEnded {
			continue
		}
		key := jsonRawString(s.EndedAt, "")
		if key == "" {
			key = s.LastActivity
		}
		// Drop ancient endings. Parseable timestamps older than the cap go;
		// unparseable timestamps fall through (better visible than silenced).
		if t, err := time.Parse(time.RFC3339, key); err == nil {
			if now.Sub(t) > EndedFooterMaxAge {
				continue
			}
		}
		ended = append(ended, row{sid, s, key})
	}
	if len(ended) == 0 {
		return ""
	}
	// Tiebreaker on sid for stable ordering when two sessions share EndedAt
	// (e.g. `reap` ending several in the same wall-clock second).
	sort.Slice(ended, func(i, j int) bool {
		if ended[i].sortKey != ended[j].sortKey {
			return ended[i].sortKey > ended[j].sortKey
		}
		return ended[i].sid < ended[j].sid
	})
	if len(ended) > 3 {
		ended = ended[:3]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "─── ended (last %d) ───\n", len(ended))
	for _, r := range ended {
		when := jsonRawString(r.sess.EndedAt, "")
		if when == "" {
			when = r.sess.LastActivity
		}
		fmt.Fprintf(&b, "  ◼ %s  %s  %s  (%s)\n",
			shortSID(r.sid),
			jsonRawString(r.sess.PrimaryRepo, "—"),
			jsonRawString(r.sess.TaskName, "—"),
			endedAgo(when, now),
		)
	}
	return strings.TrimRight(b.String(), "\n")
}

func renderMultiEndedFooter(samples []TaggedState, now time.Time) string {
	type row struct {
		sid     string
		ws      string
		sess    *state.Session
		sortKey string
	}
	var ended []row
	for _, s := range samples {
		for sid, sess := range s.State.Sessions {
			if sess.Status != state.StatusEnded {
				continue
			}
			key := jsonRawString(sess.EndedAt, "")
			if key == "" {
				key = sess.LastActivity
			}
			ended = append(ended, row{sid, s.Name, sess, key})
		}
	}
	if len(ended) == 0 {
		return ""
	}
	// Tiebreaker on sid keeps order stable when two sessions share EndedAt.
	sort.Slice(ended, func(i, j int) bool {
		if ended[i].sortKey != ended[j].sortKey {
			return ended[i].sortKey > ended[j].sortKey
		}
		return ended[i].sid < ended[j].sid
	})
	if len(ended) > 3 {
		ended = ended[:3]
	}

	var b strings.Builder
	fmt.Fprintf(&b, "─── ended (last %d) ───\n", len(ended))
	for _, r := range ended {
		when := jsonRawString(r.sess.EndedAt, "")
		if when == "" {
			when = r.sess.LastActivity
		}
		fmt.Fprintf(&b, "  ◼ %s  [%s]  %s  %s  (%s)\n",
			shortSID(r.sid),
			truncRunes(r.ws, 12),
			jsonRawString(r.sess.PrimaryRepo, "—"),
			jsonRawString(r.sess.TaskName, "—"),
			endedAgo(when, now),
		)
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderCommandsFooter prints a stable cheatsheet so a first-time user
// knows the commands and where to run them (the "control" pane, not
// here). Kept short so it never pushes the table off screen on a normal
// terminal height. Command names are the bash aliases the control pane
// installs at open time (see controlBashrc in cmd/cc-cockpit/main.go) —
// type the short form, no `cc-cockpit` prefix needed.
func renderCommandsFooter() string {
	return strings.Join([]string{
		"─── commands ─── (run them in the \"control\" pane →)",
		"  start [<repo>] <task>      spawn a Claude session (repo auto-detected)",
		"  start-fleet <repo> [name]  open an Agent View pane (multi-agent)",
		"  end <prefix>               end a session and close its pane",
		"  Ctrl-b d                   detach (sessions persist)",
	}, "\n")
}

// renderWatchFooter is the cheatsheet for `cc-cockpit watch` — read-only
// viewer with no control pane. Lists the verbs that work from any terminal
// (no cockpit env required) plus the exit hint and the `?` legend.
func renderWatchFooter() string {
	return strings.Join([]string{
		"─── commands ─── (in any terminal, prefix `cc-cockpit`)",
		"  end <prefix>               end a session (any workspace)",
		"  end all-non-ended --yes    nuke every non-ended session",
		"  reap [--older-than DUR]    sweep stale sessions (default: idle > 1h)",
		"  open                       open the cockpit for cwd's workspace",
		"  close <ws> --yes           kill a workspace's cockpit",
		"  Ctrl-C                     exit watch",
		"  legend: 🔧 tool 🤔 think ⏳ proc ⏸️ wait ✅ done 💤 idle  ? = stale 15m+",
	}, "\n")
}

func glyph(status string) string {
	switch status {
	case state.StatusRunning:
		return "🔧"
	case state.StatusThinking:
		return "🤔"
	case state.StatusProcessing:
		return "⏳"
	case state.StatusWaitingInput:
		return "⏸️"
	case state.StatusCompleted:
		return "✅"
	case state.StatusIdle:
		return "💤"
	case state.StatusEnded:
		return "◼"
	}
	return "?"
}

// shortStatus is the table-display name. waiting_input collapses to "waiting"
// for column-width reasons; everything else renders as-is (all ≤ 10 chars).
func shortStatus(s string) string {
	if s == state.StatusWaitingInput {
		return "waiting"
	}
	return s
}

// effectiveStatus applies render-time derivations to the raw reducer status:
//   - completed + LastActivity older than IdleAfterCompleted → idle
//
// The reducer stays event-pure; long-quiet decay is a display concern.
func effectiveStatus(s *state.Session, now time.Time) string {
	if s.Status != state.StatusCompleted || s.LastActivity == "" {
		return s.Status
	}
	t, err := time.Parse(time.RFC3339, s.LastActivity)
	if err != nil {
		return s.Status
	}
	if now.Sub(t) > IdleAfterCompleted {
		return state.StatusIdle
	}
	return s.Status
}

// statusText is the text label for a session's STATUS column. When a tool
// is currently executing (StatusRunning + CurrentTool set), the tool name
// replaces the generic "running" word — the 🔧 glyph already conveys
// "running tool", so "🔧 Bash" reads cleanly without "running:" noise.
// Falls back to shortStatus(effectiveStatus(...)) otherwise.
func statusText(s *state.Session, now time.Time) string {
	eff := effectiveStatus(s, now)
	if eff == state.StatusRunning && s.CurrentTool != "" {
		return truncRunes(s.CurrentTool, 12)
	}
	return shortStatus(eff)
}

// shortStatusWithStale composes the table-cell text: status (with tool name
// or idle-decay applied) plus a trailing `?` when the session is mid-turn
// and has been quiet past StaleAfter.
func shortStatusWithStale(s *state.Session, now time.Time) string {
	txt := statusText(s, now)
	if isStale(s, now) {
		return txt + "?"
	}
	return txt
}

// isStale reports whether a mid-turn session looks dead. Mid-turn means
// running / thinking / processing — states where we expect events to keep
// flowing. waiting_input / completed / idle / ended are stable, so flagging
// them adds no signal.
func isStale(s *state.Session, now time.Time) bool {
	switch s.Status {
	case state.StatusRunning, state.StatusThinking, state.StatusProcessing:
	default:
		return false
	}
	if s.LastActivity == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, s.LastActivity)
	if err != nil {
		return false
	}
	return now.Sub(t) > StaleAfter
}

func shortSID(sid string) string {
	return truncRunes(sid, 8)
}

// truncRunes returns s clipped to at most n runes (so multi-byte chars don't
// get sliced mid-codepoint).
func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// jsonRawString unwraps a json.RawMessage of either string or null. The
// reducer stores per-session fields this way to faithfully copy whatever
// the payload had.
// sessionTaskLabel returns the TASK column value, honoring `/rename` when
// the user has set a name for this session. Falls back to the cc-cockpit
// task_name from SessionStart, then to "—".
func sessionTaskLabel(s *state.Session, meta SessionMeta) string {
	if meta.Name != "" {
		return meta.Name
	}
	return jsonRawString(s.TaskName, "—")
}

// colorizeDataRows post-processes the tabwriter-formatted table by wrapping
// each data row in the ANSI escape from /color. preambleLines is the count
// of non-data lines at the top (section markers + tabwriter header).
// sids must be in the same order as the rendered data rows.
//
// Coloring happens AFTER tabwriter so the escapes don't disturb column
// width calculations — tabwriter measures plain bytes, ANSI is invisible
// terminal-side, so injecting escapes around already-aligned lines keeps
// the layout intact.
func colorizeDataRows(table string, sids []string, metas map[string]SessionMeta, preambleLines int) string {
	if metas == nil {
		return strings.TrimRight(table, "\n")
	}
	lines := strings.Split(strings.TrimRight(table, "\n"), "\n")
	for i, sid := range sids {
		idx := i + preambleLines
		if idx >= len(lines) {
			break
		}
		if ansi := ansiForColor(metas[sid].Color); ansi != "" {
			lines[idx] = ansi + lines[idx] + ansiReset
		}
	}
	return strings.Join(lines, "\n")
}

// colorizeDataRowsWithHeader assumes a 2-line preamble: the section marker
// (`─── active (N) ───`) followed by the tabwriter column header.
func colorizeDataRowsWithHeader(table string, sids []string, metas map[string]SessionMeta) string {
	return colorizeDataRows(table, sids, metas, 2)
}

// insertRecapBlocks inserts optional recap blocks immediately below their
// matching session rows. Kept as a small wrapper for tests/callers that only
// care about recaps.
func insertRecapBlocks(table string, sids []string, recaps map[string]string, preambleLines int) string {
	return insertSessionHints(table, sids, recaps, nil, sids, preambleLines)
}

func insertSessionHints(table string, sids []string, recaps map[string]string, agents map[string]subagent.Rollup, recapSids []string, preambleLines int) string {
	if len(recaps) == 0 && len(agents) == 0 {
		return table
	}
	recapAllowed := make(map[string]bool, len(recapSids))
	for _, sid := range recapSids {
		if sid != "" {
			recapAllowed[sid] = true
		}
	}
	lines := strings.Split(strings.TrimRight(table, "\n"), "\n")
	for i := len(sids) - 1; i >= 0; i-- {
		sid := sids[i]
		var insert []string
		if rollup, ok := agents[sid]; ok && rollup.Total > 0 {
			insert = append(insert, wrapAgentRollup(rollup, 78)...)
		}
		if recapAllowed[sid] {
			text := strings.TrimSpace(recaps[sid])
			if text != "" {
				insert = append(insert, wrapRecap(text, 78)...)
			}
		}
		if len(insert) == 0 {
			continue
		}
		idx := preambleLines + i
		if idx < 0 || idx >= len(lines) {
			continue
		}
		lines = append(lines[:idx+1], append(insert, lines[idx+1:]...)...)
	}
	return strings.Join(lines, "\n")
}

func wrapAgentRollup(r subagent.Rollup, width int) []string {
	const prefix = "    ↳ agents: "
	const ansiGray = "\033[90m"
	parts := []string{}
	if r.Active > 0 {
		parts = append(parts, fmt.Sprintf("%d active", r.Active))
	}
	if r.Done > 0 {
		parts = append(parts, fmt.Sprintf("%d done", r.Done))
	}
	if len(parts) == 0 {
		parts = append(parts, fmt.Sprintf("%d total", r.Total))
	}
	line := prefix + strings.Join(parts, " · ")
	if desc := strings.TrimSpace(r.LatestDescription); desc != "" {
		line += " · latest: " + desc
	}
	line = truncRunesWithEllipsis(line, width)
	return []string{ansiGray + line + ansiReset}
}

// wrapRecap renders a recap as one subtle, low-hierarchy line. Recaps are
// context hints, not primary state: keep them gray, indented, and clipped so
// the active table remains scannable.
func wrapRecap(text string, width int) []string {
	const prefix = "    ↳ recap: "
	const ansiGray = "\033[90m"
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	line := prefix + strings.Join(words, " ")
	line = truncRunesWithEllipsis(line, width)
	return []string{ansiGray + line + ansiReset}
}

func truncRunesWithEllipsis(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

// sessionRepoLabel returns the display label for a session's REPO column.
// Explicit primary_repo wins (set by `cc-cockpit start`); otherwise we fall
// back to the basename of cwd so interactive `claude` sessions (no env, no
// task name) still get a meaningful identifier instead of "—".
func sessionRepoLabel(s *state.Session) string {
	if r := jsonRawString(s.PrimaryRepo, ""); r != "" {
		return r
	}
	if c := jsonRawString(s.Cwd, ""); c != "" {
		if base := filepath.Base(c); base != "" && base != "." && base != "/" {
			return base
		}
	}
	return "—"
}

func jsonRawString(raw json.RawMessage, fallback string) string {
	if len(raw) == 0 || string(raw) == "null" {
		return fallback
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return fallback
}

func activitySince(iso string, now time.Time, suffix bool) string {
	if iso == "" {
		return "—"
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return "—"
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	var s string
	switch {
	case d < time.Minute:
		s = fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		s = fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		s = fmt.Sprintf("%dh", int(d.Hours()))
	}
	if suffix {
		s += " ago"
	}
	return s
}

// endedAgo formats an "N ago" string for ended-session rows at minute
// precision. Ended sessions don't move, so re-rendering "30s ago → 31s ago"
// every dashboard tick is pure noise: it forces a full frame repaint each
// second without changing meaningful state. Snapping to whole minutes (and
// "<1m ago" for the first 60s) means an ended row only triggers a repaint
// when it crosses a minute boundary.
func endedAgo(iso string, now time.Time) string {
	if iso == "" {
		return "—"
	}
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return "—"
	}
	d := now.Sub(t)
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "<1m ago"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	default:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
}
