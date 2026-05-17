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
)

// StaleAfter is how long a `running` session may go without any new event
// before the dashboard flags it as probably-crashed. Only applied to running;
// idle/waiting_input are already "nothing happening" states.
const StaleAfter = 15 * time.Minute

// EndedFooterMaxAge drops ended-session rows older than this from the
// "ended (last N)" footer so the dashboard doesn't keep day-old corpses
// in view. The full history is still preserved in events.jsonl.
const EndedFooterMaxAge = 24 * time.Hour

// Render produces the dashboard frame for st as of now (single-workspace
// view: no WS column).
func Render(st state.State, workspaceName string, now time.Time) string {
	var b strings.Builder
	b.WriteString(renderHeader(st, workspaceName))
	b.WriteString("\n\n")
	b.WriteString(renderActiveTable(st, now))
	if footer := renderEndedFooter(st, now); footer != "" {
		b.WriteString("\n\n")
		b.WriteString(footer)
	}
	b.WriteString("\n\n")
	b.WriteString(renderCommandsFooter())
	return b.String()
}

// RenderMulti renders aggregated samples for `watch` mode. Adds a WS column
// and a header that summarizes across all workspaces.
func RenderMulti(samples []TaggedState, title string, now time.Time) string {
	var b strings.Builder
	b.WriteString(renderMultiHeader(samples, title))
	b.WriteString("\n\n")
	b.WriteString(renderMultiActiveTable(samples, now))
	if footer := renderMultiEndedFooter(samples, now); footer != "" {
		b.WriteString("\n\n")
		b.WriteString(footer)
	}
	b.WriteString("\n\n")
	b.WriteString(renderWatchFooter())
	return b.String()
}

func renderHeader(st state.State, ws string) string {
	var active, running, waiting, idle, ended int
	for _, s := range st.Sessions {
		switch s.Status {
		case state.StatusRunning:
			running++
			active++
		case state.StatusWaitingInput:
			waiting++
			active++
		case state.StatusIdle:
			idle++
			active++
		case state.StatusEnded:
			ended++
		}
	}
	h := fmt.Sprintf("── %s ──  active=%d  ▶%d ●%d ◯%d  ended=%d ──",
		truncRunes(ws, 16), active, running, waiting, idle, ended)
	if st.DroppedEvents > 0 {
		h += fmt.Sprintf("\n⚠ %d malformed events skipped", st.DroppedEvents)
	}
	return h
}

func renderMultiHeader(samples []TaggedState, title string) string {
	var active, running, waiting, idle, ended, dropped int
	for _, s := range samples {
		for _, sess := range s.State.Sessions {
			switch sess.Status {
			case state.StatusRunning:
				running++
				active++
			case state.StatusWaitingInput:
				waiting++
				active++
			case state.StatusIdle:
				idle++
				active++
			case state.StatusEnded:
				ended++
			}
		}
		dropped += s.State.DroppedEvents
	}
	h := fmt.Sprintf("── %s ──  active=%d  ▶%d ●%d ◯%d  ended=%d ──",
		truncRunes(title, 32), active, running, waiting, idle, ended)
	if dropped > 0 {
		h += fmt.Sprintf("\n⚠ %d malformed events skipped", dropped)
	}
	return h
}

func renderActiveTable(st state.State, now time.Time) string {
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
	sort.Slice(active, func(i, j int) bool {
		return active[i].sess.StartedAt < active[j].sess.StartedAt
	})
	if len(active) == 0 {
		return "  (no active sessions — start [<repo>] <task>)"
	}

	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tSID\tREPO\tTASK\tACT")
	for _, r := range active {
		s := r.sess
		// Caps chosen so a worst-case row fits an 80-col dashboard pane after
		// tabwriter padding: status(9) + sid(8) + repo(18) + task(30) + act(5)
		// + 4×2 padding = 78, with breathing room for typical content.
		repo := truncRunes(sessionRepoLabel(s), 18)
		task := truncRunes(jsonRawString(s.TaskName, "—"), 30)
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\n",
			glyph(s.Status), shortStatusWithStale(s, now),
			shortSID(r.sid),
			repo,
			task,
			activitySince(s.LastActivity, now, false),
		)
	}
	_ = tw.Flush()
	return strings.TrimRight(b.String(), "\n")
}

func renderMultiActiveTable(samples []TaggedState, now time.Time) string {
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
		return "  (no active sessions across any workspace)"
	}

	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tSID\tWS\tREPO\tTASK\tACT")
	for _, r := range active {
		s := r.sess
		ws := truncRunes(r.ws, 12)
		repo := truncRunes(sessionRepoLabel(s), 16)
		task := truncRunes(jsonRawString(s.TaskName, "—"), 26)
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\t%s\n",
			glyph(s.Status), shortStatusWithStale(s, now),
			shortSID(r.sid),
			ws,
			repo,
			task,
			activitySince(s.LastActivity, now, false),
		)
	}
	_ = tw.Flush()
	return strings.TrimRight(b.String(), "\n")
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
	sort.Slice(ended, func(i, j int) bool { return ended[i].sortKey > ended[j].sortKey })
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
	sort.Slice(ended, func(i, j int) bool { return ended[i].sortKey > ended[j].sortKey })
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
// viewer with no control pane, so the help text differs.
func renderWatchFooter() string {
	return strings.Join([]string{
		"─── watch (read-only) ───",
		"  shows every cc-cockpit workspace at once. `?` = no activity 15m+ (possibly crashed).",
		"  Ctrl-C to exit. Use `cc-cockpit open` in a workspace to interact.",
	}, "\n")
}

func glyph(status string) string {
	switch status {
	case state.StatusRunning:
		return "▶"
	case state.StatusWaitingInput:
		return "●"
	case state.StatusIdle:
		return "◯"
	case state.StatusEnded:
		return "◼"
	}
	return "?"
}

// shortStatus is the table-display name (waiting_input → waiting, since the
// underscore-form spills past 60 cols once tabwriter pads the column).
func shortStatus(s string) string {
	if s == state.StatusWaitingInput {
		return "waiting"
	}
	return s
}

// shortStatusWithStale appends `?` to running sessions that have gone
// quiet for StaleAfter. The reducer never sees this — it's a render-time
// derivation only.
func shortStatusWithStale(s *state.Session, now time.Time) string {
	if isStale(s, now) {
		return shortStatus(s.Status) + "?"
	}
	return shortStatus(s.Status)
}

// isStale reports whether a running session looks dead. Only `running`
// gets the stale treatment — idle/waiting_input are quiet by design, so
// flagging them adds no signal.
func isStale(s *state.Session, now time.Time) bool {
	if s.Status != state.StatusRunning {
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
