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

// RenderMulti renders aggregated samples for `watch` mode. Adds a WS column
// and a header that summarizes across all workspaces. metas (/rename, /color),
// recaps (native Claude Code away_summary), and agents (subagent rollups) are
// all optional — pass nil to skip any of them.
func RenderMulti(samples []TaggedState, title string, now time.Time, metas map[string]SessionMeta, recaps map[string]string, agents map[string]subagent.Rollup) string {
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
		truncRunes(title, 48), active, busy, wait, idle, ended)
	if dropped > 0 {
		h += fmt.Sprintf("\n⚠ %d malformed events skipped", dropped)
	}
	return h
}

type activeRow struct {
	sid  string
	ws   string
	home string // state dir, for resolving the session's window sidecar
	sess *state.Session
}

// activeRowsOrdered returns the non-ended sessions across all samples in the
// same order the active table renders them. Shared by the renderer and the
// interactive selector so a row index means the same thing in both.
func activeRowsOrdered(samples []TaggedState, now time.Time) []activeRow {
	var active []activeRow
	for _, s := range samples {
		for sid, sess := range s.State.Sessions {
			if sess.Status != state.StatusEnded {
				active = append(active, activeRow{sid: sid, ws: s.Name, home: s.StateHome, sess: sess})
			}
		}
	}
	sortActiveRows(active, now)
	return active
}

func renderMultiActiveTable(samples []TaggedState, now time.Time, metas map[string]SessionMeta, recaps map[string]string, agents map[string]subagent.Rollup) string {
	active := activeRowsOrdered(samples, now)
	if len(active) == 0 {
		// Distinguish "hooks installed, just no sessions yet" from "no state
		// dirs at all" (the latter usually means hooks haven't been installed,
		// so the user gets no events and never sees anything appear). The
		// first-time hint avoids the "I ran watch and it's empty forever"
		// failure mode.
		if len(samples) == 0 {
			return "─── active (0) ───\n" +
				"  (no Claude sessions seen yet — if hooks aren't installed,\n" +
				"   run `cc-cockpit install` in another terminal, then start `claude`.)"
		}
		return "─── active (0) ───\n  (no active sessions across any workspace)"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "─── active (%d) ───\n", len(active))
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "  STATUS\tSID\tWS\tREPO\tTASK\tACT")
	sids := make([]string, len(active))
	recapSids := make([]string, len(active))
	sessions := make(map[string]*state.Session, len(active))
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
		sessions[r.sid] = r.sess
		if effectiveStatus(s, now) == state.StatusIdle {
			recapSids[i] = r.sid
		}
	}
	_ = tw.Flush()
	table := markSelectedRow(b.String(), sids, 2)
	table = colorizeDataRowsWithHeader(table, sids, metas)
	return insertSessionHints(table, sids, sessions, now, recaps, agents, recapSids, 2)
}

// markSelectedRow swaps the two-space indent of the row matching Selected for a
// "▸ " cursor. Done before colorize/hints (so indices still line up with sids)
// and width-neutral: "▸ " is two runes, exactly like "  ", so tabwriter's
// alignment is preserved.
func markSelectedRow(table string, sids []string, preambleLines int) string {
	if Selected == "" {
		return table
	}
	lines := strings.Split(table, "\n")
	for i, sid := range sids {
		if sid != Selected {
			continue
		}
		idx := i + preambleLines
		if idx >= 0 && idx < len(lines) && strings.HasPrefix(lines[idx], "  ") {
			lines[idx] = "▸ " + lines[idx][2:]
		}
		break
	}
	return strings.Join(lines, "\n")
}

func sortActiveRows(rows []activeRow, now time.Time) {
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		switch ActiveSort {
		case SortActivity:
			if a.sess.LastActivity != b.sess.LastActivity {
				return a.sess.LastActivity > b.sess.LastActivity
			}
		case SortAttention:
			ai, bi := attentionRank(a.sess, now), attentionRank(b.sess, now)
			if ai != bi {
				return ai < bi
			}
			if a.sess.LastActivity != b.sess.LastActivity {
				return a.sess.LastActivity > b.sess.LastActivity
			}
		default:
			if a.sess.StartedAt != b.sess.StartedAt {
				return a.sess.StartedAt < b.sess.StartedAt
			}
		}
		if a.ws != b.ws {
			return a.ws < b.ws
		}
		return a.sid < b.sid
	})
}

func attentionRank(s *state.Session, now time.Time) int {
	if s.Status == state.StatusWaitingInput {
		return 0
	}
	if isStale(s, now) {
		return 1
	}
	switch effectiveStatus(s, now) {
	case state.StatusRunning, state.StatusThinking, state.StatusProcessing:
		return 2
	default:
		return 3
	}
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
			// Drop ancient endings. Parseable timestamps older than the cap go;
			// unparseable timestamps fall through (better visible than silenced).
			if t, err := time.Parse(time.RFC3339, key); err == nil {
				if now.Sub(t) > EndedFooterMaxAge {
					continue
				}
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
			sessionRepoLabel(r.sess),
			sessionTaskLabel(r.sess, SessionMeta{}),
			endedAgo(when, now),
		)
	}
	return strings.TrimRight(b.String(), "\n")
}

// renderWatchFooter is the cheatsheet for `cc-cockpit watch`. Lists the verbs
// that work from any terminal plus the exit hint and the `?` legend.
func renderWatchFooter() string {
	lines := []string{
		"─── commands ─── (in any terminal, prefix `cc-cockpit`)",
		"  end <prefix>               end a session in dashboard state",
		"  end all-non-ended --yes    nuke every non-ended session",
		"  reap [--older-than DUR]    sweep stale sessions (default: idle > 1h)",
		"  Ctrl-C                     exit watch",
		"  legend: 🔧 tool 🤔 think ⏳ proc ⏸️ wait ✅ done 💤 idle  ? = stale 15m+",
	}
	// Selected is set only in interactive mode; surface the key bindings then.
	if Selected != "" {
		lines = append(lines, "  ↑/↓ select · Enter focus window · q quit")
	}
	if StatusLine != "" {
		lines = append(lines, "  "+StatusLine)
	}
	return strings.Join(lines, "\n")
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

// sessionTaskLabel returns the TASK column value. Uses Claude Code's
// `/rename` value from meta when set; otherwise "—". Watch-only mode has
// no other source of a per-session task label.
func sessionTaskLabel(_ *state.Session, meta SessionMeta) string {
	if meta.Name != "" {
		return meta.Name
	}
	return "—"
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

func insertSessionHints(table string, sids []string, sessions map[string]*state.Session, now time.Time, recaps map[string]string, agents map[string]subagent.Rollup, recapSids []string, preambleLines int) string {
	if len(recaps) == 0 && len(agents) == 0 && len(sessions) == 0 {
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
		if sess := sessions[sid]; sess != nil {
			insert = append(insert, wrapToolRollup(sess, now, 78)...)
			insert = append(insert, wrapFailureRollup(sess, now, 78)...)
		}
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

func wrapToolRollup(s *state.Session, now time.Time, width int) []string {
	if len(s.ToolCounts) == 0 && s.LastTool == "" {
		return nil
	}
	const prefix = "    ↳ tools: "
	const ansiGray = "\033[90m"
	parts := topToolParts(s.ToolCounts, 3)
	if s.LastTool != "" && s.LastToolAt != "" {
		parts = append(parts, "last "+truncRunes(s.LastTool, 16)+" "+activitySince(s.LastToolAt, now, false))
	}
	if len(parts) == 0 {
		return nil
	}
	line := truncRunesWithEllipsis(prefix+strings.Join(parts, " · "), width)
	if NoColor {
		return []string{line}
	}
	return []string{ansiGray + line + ansiReset}
}

func topToolParts(counts map[string]int, limit int) []string {
	type item struct {
		name  string
		count int
	}
	items := make([]item, 0, len(counts))
	for name, count := range counts {
		if name == "" || count <= 0 {
			continue
		}
		items = append(items, item{name: name, count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count != items[j].count {
			return items[i].count > items[j].count
		}
		return items[i].name < items[j].name
	})
	if len(items) > limit {
		items = items[:limit]
	}
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("%s %d", truncRunes(it.name, 16), it.count))
	}
	return parts
}

func wrapFailureRollup(s *state.Session, now time.Time, width int) []string {
	if s.FailureCount == 0 || s.LastFailureAt == "" {
		return nil
	}
	const prefix = "    ↳ failures: "
	const ansiGray = "\033[90m"
	subject := "turn"
	if s.LastFailureTool != "" {
		subject = truncRunes(s.LastFailureTool, 16)
	}
	line := prefix + subject + " failed " + activitySince(s.LastFailureAt, now, true)
	if s.FailureCount > 1 {
		line += fmt.Sprintf(" · %d total", s.FailureCount)
	}
	line = truncRunesWithEllipsis(line, width)
	if NoColor {
		return []string{line}
	}
	return []string{ansiGray + line + ansiReset}
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
	if NoColor {
		return []string{line}
	}
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
	if NoColor {
		return []string{line}
	}
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

// sessionRepoLabel returns the display label for a session's REPO column:
// the basename of the session's cwd, or "—" when cwd is missing/root.
func sessionRepoLabel(s *state.Session) string {
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
