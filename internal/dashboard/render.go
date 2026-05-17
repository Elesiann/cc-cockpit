package dashboard

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/elesiann/cc-cockpit/internal/state"
)

// Render produces the dashboard frame for st as of now.
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
		return "  (no active sessions — start <repo> <task>)"
	}

	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tSID\tREPO\tTASK\tACT")
	for _, r := range active {
		s := r.sess
		// Caps chosen so a worst-case row fits an 80-col dashboard pane after
		// tabwriter padding: status(9) + sid(8) + repo(18) + task(30) + act(5)
		// + 4×2 padding = 78, with breathing room for typical content.
		repo := truncRunes(jsonRawString(s.PrimaryRepo, "—"), 18)
		task := truncRunes(jsonRawString(s.TaskName, "—"), 30)
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\n",
			glyph(s.Status), shortStatus(s.Status),
			shortSID(r.sid),
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
			activitySince(when, now, true),
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
		"  start <repo> <task>        spawn a Claude session",
		"  start-fleet <repo> [name]  open an Agent View pane (multi-agent)",
		"  mark-ended <prefix>        dismiss a stuck session",
		"  Ctrl-b d                   detach (sessions persist)",
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
