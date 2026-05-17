package hook

import (
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/elesiann/cc-cockpit/internal/workspace"
)

// GlobalWorkspaceName is the synthetic workspace name used as the catch-all
// state dir for Claude sessions that aren't tracked by an explicit workspace
// (interactive sessions outside any workspace tree, ad-hoc `claude --print`,
// etc). The leading underscore disqualifies it from workspace.ValidSlug so it
// can never collide with a real user-defined workspace name.
const GlobalWorkspaceName = "_global"

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

// Resolve returns the events.jsonl parent dir and pane_id for one hook
// invocation. payloadCwd is the `cwd` field from the Claude hook payload
// (may be ""). stateHome is never empty — sessions that don't match a real
// workspace land in the synthetic GlobalWorkspaceName dir so `watch` can see
// every Claude session the user runs, regardless of how it was started.
//
// Routing priority:
//  1. Agent View (state.json under ~/.claude/jobs/<sid>/) — anchors by the
//     session's own originCwd to defeat env-var leakage from a parent pane.
//  2. Cockpit-spawned (COCKPIT_SESSION_ACTIVE env) — uses COCKPIT_STATE_HOME.
//  3. Interactive — walks up payloadCwd to find a workspace.json ancestor.
//  4. Global fallback — synthetic _global workspace.
func (r *Resolver) Resolve(sid, payloadCwd string) (stateHome, paneID string) {
	// Branch 1: Agent View (state.json present).
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
			agentCwd := js.OriginCwd
			if agentCwd == "" {
				agentCwd = js.Cwd
			}
			if sh := r.routeByCwd(agentCwd); sh != "" {
				return sh, ""
			}
		}
		// Agent View outside any workspace → global. Intentionally skip the
		// env-var fast path here: env vars may have leaked from a parent
		// cockpit pane and would misroute this session.
		return r.globalStateHome(), ""
	}

	// Branch 2: cockpit-spawned (env-var fast path).
	if r.Getenv("COCKPIT_SESSION_ACTIVE") == "1" {
		if sh := r.Getenv("COCKPIT_STATE_HOME"); sh != "" {
			return sh, r.Getenv("TMUX_PANE")
		}
	}

	// Branch 3: interactive session — route by the payload's cwd.
	if sh := r.routeByCwd(payloadCwd); sh != "" {
		return sh, ""
	}

	// Branch 4: global fallback.
	return r.globalStateHome(), ""
}

// routeByCwd returns the stateHome for the workspace.json ancestor of cwd,
// or "" if cwd is empty or has no workspace ancestor.
func (r *Resolver) routeByCwd(cwd string) string {
	if cwd == "" {
		return ""
	}
	if real, err := r.EvalLinks(cwd); err == nil {
		cwd = real
	}
	root := r.FindRoot(cwd)
	if root == "" {
		return ""
	}
	ws, err := r.LoadWS(root)
	if err != nil || ws.Name == "" {
		return ""
	}
	return ComputeStateHome(r.HomeDir, r.Getenv, ws.Name)
}

func (r *Resolver) globalStateHome() string {
	return ComputeStateHome(r.HomeDir, r.Getenv, GlobalWorkspaceName)
}

// ComputeStateHome mirrors the path formula used in runOpen so both call
// sites stay in lockstep.
func ComputeStateHome(homeDir string, getenv func(string) string, wsName string) string {
	if v := getenv("XDG_STATE_HOME"); v != "" {
		return filepath.Join(v, "cc-cockpit", wsName)
	}
	return filepath.Join(homeDir, ".local", "state", "cc-cockpit", wsName)
}
