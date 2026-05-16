// Package tmux is a minimal wrapper around the tmux CLI used by `cc-cockpit
// open` and `cc-cockpit start`. All commands run on a private server
// (-L cc-cockpit) so cc-cockpit sessions don't collide with the user's
// own tmux work.
package tmux

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Server is the -L label for the private tmux server cc-cockpit uses.
const Server = "cc-cockpit"

func cmd(args ...string) *exec.Cmd {
	return exec.Command("tmux", append([]string{"-L", Server}, args...)...)
}

// HasSession reports whether a session by that name exists on our server.
func HasSession(name string) bool {
	return cmd("has-session", "-t", name).Run() == nil
}

// NewSession creates a detached session with two side-by-side panes:
// a 60-col dashboard pane on the left running `cc-cockpit dashboard`, and
// a control pane (bash) on the right. env entries are KEY=VALUE pairs
// applied to both panes via tmux's -e flag.
func NewSession(name string, env []string) error {
	if err := cmd(newSessionArgs(name, env)...).Run(); err != nil {
		return fmt.Errorf("new-session: %w", err)
	}
	if err := cmd(splitControlArgs(name, env)...).Run(); err != nil {
		return fmt.Errorf("split-window: %w", err)
	}
	// 80 cols on the dashboard pane: standard terminal-width minimum, plus
	// room for the active/ended tables to render long repo names without
	// truncation. Matches the column caps in internal/dashboard/render.go.
	if err := cmd("resize-pane", "-t", name+":0.0", "-x", "80").Run(); err != nil {
		return fmt.Errorf("resize-pane: %w", err)
	}
	// Label the cockpit panes so the border title isn't the bash fallback
	// (hostname). "watcher" = read-only dashboard, "control" = shell where
	// you spawn new sessions. Claude panes get their own title later via
	// NewClaudePane.
	if err := cmd("select-pane", "-t", name+":0.0", "-T", "watcher").Run(); err != nil {
		return fmt.Errorf("select-pane -T watcher: %w", err)
	}
	if err := cmd("select-pane", "-t", name+":0.1", "-T", "control").Run(); err != nil {
		return fmt.Errorf("select-pane -T control: %w", err)
	}
	return applyServerOptions()
}

// applyServerOptions sets server-global tmux options on the private
// cc-cockpit server: mouse on (click panes to focus, drag to resize,
// wheel to enter copy-mode), and pane-border-status/format so each
// Claude pane self-labels with its title (set via select-pane -T).
// Global is safe because the -L cc-cockpit server is dedicated.
func applyServerOptions() error {
	for _, args := range serverOptionArgs() {
		if err := cmd(args...).Run(); err != nil {
			return fmt.Errorf("set-option %v: %w", args, err)
		}
	}
	return nil
}

// Attach attaches the current terminal to the named session and waits for
// the user to exit/detach.
func Attach(name string) error {
	c := cmd("attach-session", "-t", name)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// NewClaudePane spawns a Claude session as a pane inside window 0 of the
// cockpit session, recreating the original bash+Zellij layout:
//
//	┌─ dashboard ─┬─ control ──┐
//	├─────────────┴────────────┤
//	│ claude 1 │ claude 2 │ …  │
//	└──────────────────────────┘
//
// First spawn (window 0 has only dashboard + control) creates a full-width
// pane on the bottom row via split-window -v -f. Subsequent spawns target
// the most-recent Claude pane and split it horizontally so panes tile
// side-by-side in the bottom row. After creation, the pane's title is set
// to paneName so it renders in the pane border.
//
// Returns the new pane's id (e.g. "%42") captured from -P -F '#{pane_id}'.
func NewClaudePane(session, paneName, cwd string, env []string, command ...string) (string, error) {
	existing, err := listPaneIDs(session, 0)
	if err != nil {
		return "", fmt.Errorf("list-panes: %w", err)
	}
	out, err := cmd(spawnPaneArgs(session, cwd, env, existing, command)...).Output()
	if err != nil {
		return "", fmt.Errorf("split-window: %w", err)
	}
	paneID := strings.TrimSpace(string(out))
	if err := cmd("select-pane", "-t", paneID, "-T", paneName).Run(); err != nil {
		return paneID, fmt.Errorf("select-pane -T: %w", err)
	}
	return paneID, nil
}

func listPaneIDs(session string, windowIdx int) ([]string, error) {
	out, err := cmd("list-panes", "-t", fmt.Sprintf("%s:%d", session, windowIdx), "-F", "#{pane_id}").Output()
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			ids = append(ids, line)
		}
	}
	return ids, nil
}

// SetPaneExitedHook installs shellCmd as the pane-exited hook for one tmux
// session. tmux fires pane-exited when a pane's process dies (the case for
// crashed Claude sessions). It does NOT fire on `kill-pane` / `kill-window`,
// which are operator actions.
//
// Per-session (-t session) instead of global (-g) so opening a second
// workspace doesn't overwrite the first's hook — each session keeps its own
// stateHome embedded.
//
// Use #{hook_pane} for the dying pane's id; #{pane_id} resolves to the
// currently-focused pane in hook context, which is wrong here.
func SetPaneExitedHook(session, shellCmd string) error {
	return cmd("set-hook", "-t", session, "pane-exited", "run-shell "+shellCmd).Run()
}

// Version returns tmux's reported version string (e.g. "tmux 3.4").
func Version() (string, error) {
	out, err := exec.Command("tmux", "-V").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ----- arg builders (split out for testability) -----

func newSessionArgs(name string, env []string) []string {
	args := []string{"new-session", "-d", "-s", name}
	for _, kv := range env {
		args = append(args, "-e", kv)
	}
	return append(args, "cc-cockpit", "dashboard")
}

func splitControlArgs(name string, env []string) []string {
	args := []string{"split-window", "-h", "-t", name + ":0"}
	for _, kv := range env {
		args = append(args, "-e", kv)
	}
	return append(args, "bash")
}

// serverOptionArgs returns the tmux set-option invocations applied by
// applyServerOptions. Split out so tests can validate the option set.
func serverOptionArgs() [][]string {
	return [][]string{
		{"set-option", "-g", "mouse", "on"},
		{"set-option", "-g", "pane-border-status", "top"},
		{"set-option", "-g", "pane-border-format", " #{pane_title} "},
	}
}

// spawnPaneArgs returns the tmux split-window args for adding a Claude
// pane to window 0. existingPaneIDs is the output of list-panes for that
// window. With 2 panes (the cockpit's dashboard + control) the new pane
// is a full-width row across the bottom (-v -f). With 3 or more, the
// previous Claude pane is split horizontally (-h) so panes tile
// side-by-side. The new pane's id is captured via -P -F.
func spawnPaneArgs(session, cwd string, env []string, existingPaneIDs []string, command []string) []string {
	var args []string
	if len(existingPaneIDs) <= 2 {
		// First spawn: full-width bottom row.
		args = []string{"split-window", "-v", "-f", "-t", session + ":0"}
	} else {
		// Subsequent spawn: tile next to the most-recent pane.
		last := existingPaneIDs[len(existingPaneIDs)-1]
		args = []string{"split-window", "-h", "-t", last}
	}
	args = append(args, "-c", cwd, "-P", "-F", "#{pane_id}")
	for _, kv := range env {
		args = append(args, "-e", kv)
	}
	return append(args, command...)
}
