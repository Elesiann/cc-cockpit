package dashboard

import (
	"encoding/json"
	"testing"

	"github.com/elesiann/cc-cockpit/internal/state"
)

func TestPickAttentionLabels_PrefersMostRecent(t *testing.T) {
	// Two waiting sessions. The one with the later LastActivity should win
	// regardless of map iteration order.
	st := state.State{
		Sessions: map[string]*state.Session{
			"older-sid": {
				Status:       state.StatusWaitingInput,
				LastActivity: "2026-05-20T10:00:00Z",
				Cwd:          json.RawMessage(`"/repos/older"`),
			},
			"newer-sid": {
				Status:       state.StatusWaitingInput,
				LastActivity: "2026-05-20T11:00:00Z",
				Cwd:          json.RawMessage(`"/repos/newer"`),
			},
		},
	}
	// Run several times — Go map iteration randomizes order, so a buggy
	// implementation would eventually flip the answer. The fixed version
	// must return the same label every time.
	for i := 0; i < 50; i++ {
		repo, _ := pickAttentionLabels(st, nil)
		if repo != "newer" {
			t.Fatalf("iter %d: got repo=%q, want %q (most-recent waiting session)", i, repo, "newer")
		}
	}
}

func TestPickAttentionLabels_TiebreaksBySid(t *testing.T) {
	// Equal LastActivity → smallest sid wins. Determinism matters more than
	// which one we pick, so the test pins sid-asc as the contract.
	st := state.State{
		Sessions: map[string]*state.Session{
			"sid-b": {
				Status:       state.StatusWaitingInput,
				LastActivity: "2026-05-20T11:00:00Z",
				Cwd:          json.RawMessage(`"/repos/b"`),
			},
			"sid-a": {
				Status:       state.StatusWaitingInput,
				LastActivity: "2026-05-20T11:00:00Z",
				Cwd:          json.RawMessage(`"/repos/a"`),
			},
		},
	}
	for i := 0; i < 50; i++ {
		repo, _ := pickAttentionLabels(st, nil)
		if repo != "a" {
			t.Fatalf("iter %d: got repo=%q, want %q (sid-asc tiebreaker)", i, repo, "a")
		}
	}
}

func TestPickAttentionLabels_NoneWaiting_ReturnsEmpty(t *testing.T) {
	st := state.State{
		Sessions: map[string]*state.Session{
			"sid-1": {Status: state.StatusRunning, Cwd: json.RawMessage(`"/repos/x"`)},
		},
	}
	repo, task := pickAttentionLabels(st, nil)
	if repo != "" || task != "" {
		t.Errorf("got (%q,%q), want both empty", repo, task)
	}
}

func TestPickAttentionLabels_UsesRenameMeta(t *testing.T) {
	st := state.State{
		Sessions: map[string]*state.Session{
			"sid-1": {
				Status:       state.StatusWaitingInput,
				LastActivity: "2026-05-20T11:00:00Z",
				Cwd:          json.RawMessage(`"/repos/api"`),
			},
		},
	}
	metas := map[string]SessionMeta{"sid-1": {Name: "fix auth bug"}}
	repo, task := pickAttentionLabels(st, metas)
	if repo != "api" || task != "fix auth bug" {
		t.Errorf("got (%q,%q), want (api, fix auth bug)", repo, task)
	}
}
