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
	if err := cmd("resize-pane", "-t", name+":0.0", "-x", "60").Run(); err != nil {
		return fmt.Errorf("resize-pane: %w", err)
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

// NewWindow opens a window in the session running command in cwd. Returns
// the new pane's id (e.g. "%42") captured from -P -F '#{pane_id}'.
func NewWindow(session, windowName, cwd string, env []string, command ...string) (string, error) {
	out, err := cmd(newWindowArgs(session, windowName, cwd, env, command)...).Output()
	if err != nil {
		return "", fmt.Errorf("new-window: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
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

func newWindowArgs(session, windowName, cwd string, env []string, command []string) []string {
	args := []string{
		"new-window", "-d",
		"-t", session,
		"-n", windowName,
		"-c", cwd,
		"-P", "-F", "#{pane_id}",
	}
	for _, kv := range env {
		args = append(args, "-e", kv)
	}
	return append(args, command...)
}
