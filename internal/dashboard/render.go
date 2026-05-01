// Package dashboard implements the cc-cockpit dashboard pane: a polling
// loop that snapshots events.jsonl, reduces it, renders a frame, and rings
// the terminal bell on new attention events.
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

// Render produces the dashboard frame for st as of now. Pure function so
// tests can pin behavior without driving a real terminal.
//
// Output mirrors render.sh: header line with status counts, an active-session
// table aligned by tabwriter, and an ended-sessions footer (last 3).
func Render(st state.State, workspaceName string, now time.Time) string {
	var b strings.Builder
	b.WriteString(renderHeader(st, workspaceName))
	b.WriteString("\n\n")
	b.WriteString(renderActiveTable(st, now))
	footer := renderEndedFooter(st, now)
	if footer != "" {
		b.WriteString("\n\n")
		b.WriteString(footer)
	}
	return b.String()
}

func renderHeader(st state.State, ws string) string {
	var active, running, waiting, idle, ended int
	for _, s := range st.Sessions {
		switch s.Status {
		case "running":
			running++
			active++
		case "waiting_input":
			waiting++
			active++
		case "idle":
			idle++
			active++
		case "ended":
			ended++
		}
	}
	h := fmt.Sprintf("─── cc-cockpit · %s ───  active=%d (running=%d waiting=%d idle=%d)  ended=%d",
		ws, active, running, waiting, idle, ended)
	if st.DroppedEvents > 0 {
		h += fmt.Sprintf("  ⚠ dropped=%d", st.DroppedEvents)
	}
	h += " ───"
	return h
}

func renderActiveTable(st state.State, now time.Time) string {
	type row struct {
		sid  string
		sess *state.Session
	}
	var active []row
	for sid, s := range st.Sessions {
		if s.Status != "ended" {
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
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\n",
			glyph(s.Status), s.Status,
			shortSID(r.sid),
			jsonRawString(s.PrimaryRepo, "—"),
			truncate(jsonRawString(s.TaskName, "—"), 40),
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
		if s.Status != "ended" {
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
	case "running":
		return "▶"
	case "waiting_input":
		return "●"
	case "idle":
		return "◯"
	case "ended":
		return "◼"
	}
	return "?"
}

func shortSID(sid string) string {
	if len(sid) <= 8 {
		return sid
	}
	return sid[:8]
}

// jsonRawString unwraps a json.RawMessage that's either a string or null
// (matches how the reducer stores per-session fields). Returns fallback
// when the value is null, missing, or non-string.
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// activitySince formats time-since-iso as Ns/Nm/Nh; with suffix=true,
// appends " ago" (used in the ended footer).
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
