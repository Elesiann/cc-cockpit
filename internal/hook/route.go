package hook

import (
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/elesiann/cc-cockpit/internal/workspace"
)

// Resolver resolves a session_id to the events.jsonl sink (stateHome) and
// pane_id. All I/O is injected so the routing logic is unit-testable.
type Resolver struct {
	HomeDir   string
	Getenv    func(string) string
	ReadFile  func(string) ([]byte, error)
	EvalLinks func(string) (string, error)
	FindRoot  func(string) string
	LoadWS    func(string) (*workspace.Workspace, error)
	Sleep     func(time.Duration)
}

// Resolve returns ("", "") to silently drop. Otherwise stateHome is the
// events.jsonl parent dir and paneID is the TMUX_PANE for cockpit-spawned
// sessions, "" for Agent View (no tmux pane).
func (r *Resolver) Resolve(sid string) (stateHome, paneID string) {
	// Agent View detection takes precedence over the env-var fast path.
	// If a user runs `claude --bg` from inside a cockpit pane, the
	// supervisor inherits COCKPIT_SESSION_ACTIVE / COCKPIT_STATE_HOME and
	// leaks them to later sessions dispatched from anywhere; checking
	// state.json first anchors routing to the session's own cwd.
	statePath := filepath.Join(r.HomeDir, ".claude", "jobs", sid, "state.json")
	var data []byte
	for i := 0; i < 3; i++ {
		d, err := r.ReadFile(statePath)
		if err == nil {
			data = d
			break
		}
		r.Sleep(20 * time.Millisecond)
	}
	if len(data) > 0 {
		var js struct {
			OriginCwd string `json:"originCwd"`
			Cwd       string `json:"cwd"`
		}
		if json.Unmarshal(data, &js) == nil {
			cwd := js.OriginCwd
			if cwd == "" {
				cwd = js.Cwd
			}
			if cwd != "" {
				if real, err := r.EvalLinks(cwd); err == nil {
					cwd = real
				}
				if root := r.FindRoot(cwd); root != "" {
					if ws, err := r.LoadWS(root); err == nil && ws.Name != "" {
						return ComputeStateHome(r.HomeDir, r.Getenv, ws.Name), ""
					}
				}
			}
		}
		return "", ""
	}

	// Env-var fast path: a true cockpit-spawned `claude` session.
	if r.Getenv("COCKPIT_SESSION_ACTIVE") == "1" {
		if sh := r.Getenv("COCKPIT_STATE_HOME"); sh != "" {
			return sh, r.Getenv("TMUX_PANE")
		}
	}
	return "", ""
}

// ComputeStateHome mirrors the path formula used in runOpen so both call
// sites stay in lockstep.
func ComputeStateHome(homeDir string, getenv func(string) string, wsName string) string {
	if v := getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "cc-cockpit", wsName)
	}
	return filepath.Join(homeDir, ".local", "state", "cc-cockpit", wsName)
}
