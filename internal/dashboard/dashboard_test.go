package dashboard

import (
	"encoding/json"
	"testing"

	"github.com/elesiann/cc-cockpit/internal/state"
)

func TestStatusToBorderColor(t *testing.T) {
	cases := []struct {
		status string
		want   string
	}{
		{state.StatusRunning, "green"},
		{state.StatusWaitingInput, "yellow"},
		{state.StatusIdle, "colour244"},
		{state.StatusEnded, "colour240"},
		{"unknown", ""},
		{"", ""},
	}
	for _, c := range cases {
		if got := statusToBorderColor(c.status); got != c.want {
			t.Errorf("statusToBorderColor(%q) = %q, want %q", c.status, got, c.want)
		}
	}
}

// applyPaneBorderColors is exercised end-to-end via the dashboard runtime;
// here we only verify the cache mutation contract (no tmux calls happen
// because the test sessions have null pane_id, which the function skips).
func TestApplyPaneBorderColors_SkipsNullPaneAndCachesEmits(t *testing.T) {
	st := state.State{
		Sessions: map[string]*state.Session{
			"a1": {
				Status: state.StatusRunning,
				PaneID: json.RawMessage("null"),
			},
		},
	}
	cache := map[string]string{}
	applyPaneBorderColors(st, cache)
	if len(cache) != 0 {
		t.Errorf("null pane_id should not populate cache, got %v", cache)
	}
}
