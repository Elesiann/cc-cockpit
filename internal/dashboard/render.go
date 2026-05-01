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
	h := fmt.Sprintf("─── cc-cockpit · %s ───  active=%d (running=%d waiting=%d idle=%d)  ended=%d",
		ws, active, running, waiting, idle, ended)
	if st.DroppedEvents > 0 {
		h += fmt.Sprintf("  ⚠ dropped=%d", st.DroppedEvents)
	}
	return h + " ───"
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
		return "  (no active sessions — start one: cc-cockpit start <repo> <task...>)"
	}

	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tSID\tREPO\tTASK\tACT")
	for _, r := range active {
		s := r.sess
		task := jsonRawString(s.TaskName, "—")
		if len(task) > 40 {
			task = task[:40]
		}
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\n",
			glyph(s.Status), s.Status,
			shortSID(r.sid),
			jsonRawString(s.PrimaryRepo, "—"),
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

func shortSID(sid string) string {
	if len(sid) > 8 {
		return sid[:8]
	}
	return sid
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
