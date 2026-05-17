package dashboard

import (
	"encoding/json"

	"github.com/elesiann/cc-cockpit/internal/state"
	"github.com/elesiann/cc-cockpit/internal/tmux"
)

// statusToBorderColor maps a session status to a tmux color name. Returns
// "" for unknown status (caller skips emit). green / yellow / grays chosen
// to give "this needs my attention" pop without screaming the whole grid.
func statusToBorderColor(status string) string {
	switch status {
	case state.StatusRunning:
		return "green"
	case state.StatusWaitingInput:
		return "yellow"
	case state.StatusIdle:
		return "colour244"
	case state.StatusEnded:
		return "colour240"
	}
	return ""
}

// applyPaneBorderColors issues `select-pane -P fg=<color>` for each session
// whose pane border color changed since the last tick. The prev map caches
// last-emitted color per pane so steady-state ticks issue zero tmux calls.
// Sessions with null pane_id (fleet/bg agents) are skipped — they don't own
// a single pane to color.
func applyPaneBorderColors(st state.State, prev map[string]string) {
	for _, sess := range st.Sessions {
		var paneID string
		if err := json.Unmarshal(sess.PaneID, &paneID); err != nil || paneID == "" {
			continue
		}
		color := statusToBorderColor(sess.Status)
		if color == "" {
			continue
		}
		if prev[paneID] == color {
			continue
		}
		_ = tmux.SetPaneBorderColor(paneID, color)
		prev[paneID] = color
	}
}
