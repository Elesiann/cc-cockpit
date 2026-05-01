// cc-cockpit — Go port (work in progress).
//
// During Phase 1 of the bash → Go migration, subcommands are ported one at a
// time. Anything not yet ported here is still served by the bash binary at
// .cc-cockpit/bin/cc-cockpit.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/elesiann/cc-cockpit/internal/dashboard"
	"github.com/elesiann/cc-cockpit/internal/hook"
	"github.com/elesiann/cc-cockpit/internal/state"
	"github.com/elesiann/cc-cockpit/internal/workspace"
)

const Version = "0.1.0-mvp"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]
	switch cmd {
	case "--version", "-v":
		fmt.Printf("cc-cockpit %s\n", Version)
	case "init":
		os.Exit(runInit(args))
	case "doctor":
		os.Exit(runDoctor(args))
	case "mark-ended":
		os.Exit(runMarkEnded(args))
	case "hook":
		os.Exit(runHook(args))
	case "dashboard":
		os.Exit(runDashboard(args))
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "cc-cockpit: subcommand %q not yet implemented in Go port\n", cmd)
		fmt.Fprintln(os.Stderr, "Use the bash binary at .cc-cockpit/bin/cc-cockpit for full functionality.")
		os.Exit(2)
	}
}

func usage() {
	fmt.Println(`cc-cockpit ` + Version + `

Available subcommands (during the bash → Go migration):
  --version           print version
  init                create .cc-cockpit/workspace.json
  doctor              check install + workspace health
  mark-ended          dismiss stale sessions (synthetic SessionEnd)
  hook <Event>        internal: ingest a Claude Code hook payload
  dashboard           render the dashboard pane (loop until SIGTERM)
  help                show this message

Other subcommands (open, start) are still served by the bash binary at
.cc-cockpit/bin/cc-cockpit.`)
}

func die(prefix, format string, args ...any) {
	fmt.Fprintf(os.Stderr, prefix+": "+format+"\n", args...)
	os.Exit(2)
}

// ---------- init ----------

func runInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	name := fs.String("name", "", "workspace name (default: slug from cwd basename)")
	force := fs.Bool("force", false, "overwrite existing workspace.json")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cwd, err := os.Getwd()
	if err != nil {
		die("init", "cannot determine current directory: %v", err)
	}

	wsName := *name
	if wsName == "" {
		wsName = workspace.SlugFromPath(cwd)
	}
	if !workspace.ValidSlug(wsName) {
		die("init", "invalid workspace name %q (must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$)", wsName)
	}

	ws := &workspace.Workspace{Name: wsName, Repos: map[string]string{}}
	specs := fs.Args()

	if len(specs) > 0 {
		for _, spec := range specs {
			label, path, hasEq := strings.Cut(spec, "=")
			if !hasEq {
				path = spec
				label = workspace.SlugFromPath(path)
			}
			if path == "" {
				die("init", "empty path in repo spec %q", spec)
			}
			if err := ws.AddRepo(cwd, label, path); err != nil {
				die("init", err.Error())
			}
		}
	} else {
		repoDirs, err := workspace.DiscoverRepos(cwd)
		if err != nil {
			die("init", "cannot scan for repos: %v", err)
		}
		for _, repoDir := range repoDirs {
			if err := ws.AddRepo(cwd, workspace.SlugFromPath(repoDir), repoDir); err != nil {
				die("init", err.Error())
			}
		}
	}

	if len(ws.Repos) == 0 {
		die("init", "no child git repos found. Run from a workspace parent or pass repo specs like api=packages/api")
	}

	wsPath := filepath.Join(cwd, ".cc-cockpit", "workspace.json")
	if !*force {
		if _, err := os.Stat(wsPath); err == nil {
			die("init", "workspace already exists at %s (use --force to rewrite)", wsPath)
		}
	}

	if err := ws.Save(cwd); err != nil {
		die("init", "cannot write workspace.json: %v", err)
	}

	fmt.Printf("workspace: %s\n", wsName)
	fmt.Printf("config: %s\n", wsPath)
	fmt.Println()
	fmt.Println("repos:")
	keys := sortedKeys(ws.Repos)
	for _, k := range keys {
		fmt.Printf("  %-16s %s\n", k, ws.Repos[k])
	}
	fmt.Println()
	fmt.Println("next:")
	fmt.Println("  cc-cockpit open")
	fmt.Printf("  cc-cockpit start %s <task>\n", keys[0])
	return 0
}

// ---------- doctor ----------

// claudeHookEvents is the canonical list. Keep in sync with install_claude_hooks
// in the bash binary (and the Go install port when it lands).
var claudeHookEvents = []string{
	"SessionStart", "UserPromptSubmit", "PermissionRequest",
	"Notification", "PostToolUse", "Stop", "SessionEnd",
}

func runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	issues := 0
	ok := func(format string, args ...any) {
		fmt.Printf("ok   "+format+"\n", args...)
	}
	fail := func(format string, args ...any) {
		fmt.Printf("fail "+format+"\n", args...)
		issues++
	}

	// Tool checks. jq is still required during the migration because the bash
	// binary uses it; this check goes away in step 10 when bash is removed.
	for _, tool := range []string{"jq", "zellij", "claude", "cc-cockpit"} {
		path, err := exec.LookPath(tool)
		switch {
		case err == nil:
			ok("%s found: %s", tool, path)
		case tool == "cc-cockpit":
			fail("cc-cockpit not found on PATH (run ./install from the source checkout)")
		default:
			fail("%s not found on PATH", tool)
		}
	}

	// Claude settings.
	settingsPath := os.Getenv("CLAUDE_SETTINGS_PATH")
	if settingsPath == "" {
		home, _ := os.UserHomeDir()
		settingsPath = filepath.Join(home, ".claude", "settings.json")
	}
	settingsRaw, err := os.ReadFile(settingsPath)
	if err != nil {
		fail("Claude settings not found: %s (run cc-cockpit install)", settingsPath)
	} else {
		hooks, err := parseSettingsHooks(settingsRaw)
		if err != nil {
			fail("Claude settings invalid: %v", err)
		} else {
			for _, ev := range claudeHookEvents {
				if hasCockpitHook(hooks[ev], ev) {
					ok("Claude hook installed: %s", ev)
				} else {
					fail("Claude hook missing: %s (run cc-cockpit install)", ev)
				}
			}
			if hasNotificationMatcher(hooks["Notification"]) {
				ok("Notification hook matcher is idle_prompt|permission_prompt")
			} else {
				fail("Notification hook matcher missing idle_prompt|permission_prompt")
			}
		}
	}

	// Workspace.
	cwd, _ := os.Getwd()
	root := workspace.FindRoot(cwd)
	if root == "" {
		fail("workspace not initialized (run cc-cockpit init from the workspace parent)")
	} else {
		ws, err := workspace.Load(root)
		switch {
		case err != nil:
			fail("workspace.json invalid: %v", err)
		case !workspace.ValidSlug(ws.Name):
			fail("invalid workspace name %q", ws.Name)
		default:
			ok("workspace: %s (%s)", ws.Name, root)
			if len(ws.Repos) == 0 {
				fail("workspace has no repos configured")
			}
			for _, k := range sortedKeys(ws.Repos) {
				rel := ws.Repos[k]
				if !workspace.ValidSlug(k) {
					fail("invalid repo label %q", k)
					continue
				}
				if err := workspace.CheckRepo(root, rel); err != nil {
					fail("repo '%s': %v", k, err)
				} else {
					ok("repo '%s': %s", k, rel)
				}
			}
		}
	}

	if issues == 0 {
		fmt.Println("doctor: all checks passed")
		return 0
	}
	fmt.Printf("doctor: %d issue(s) found\n", issues)
	return 1
}

// hookEntry mirrors the shape ~/.claude/settings.json uses under .hooks.<Event>:
//
//	[ { "matcher": "...", "hooks": [ { "type": "command", "command": "..." } ] }, ... ]
type hookEntry struct {
	Matcher string `json:"matcher,omitempty"`
	Hooks   []struct {
		Type    string `json:"type"`
		Command string `json:"command"`
	} `json:"hooks"`
}

func parseSettingsHooks(raw []byte) (map[string][]hookEntry, error) {
	var top struct {
		Hooks map[string][]hookEntry `json:"hooks"`
	}
	if err := json.Unmarshal(raw, &top); err != nil {
		return nil, err
	}
	if top.Hooks == nil {
		return map[string][]hookEntry{}, nil
	}
	return top.Hooks, nil
}

// hasCockpitHook returns true if any entry has a hook command containing
// "cc-cockpit hook <event>" — the same substring check the bash doctor uses.
func hasCockpitHook(entries []hookEntry, event string) bool {
	needle := "cc-cockpit hook " + event
	for _, e := range entries {
		for _, h := range e.Hooks {
			if strings.Contains(h.Command, needle) {
				return true
			}
		}
	}
	return false
}

func hasNotificationMatcher(entries []hookEntry) bool {
	for _, e := range entries {
		if e.Matcher != "idle_prompt|permission_prompt" {
			continue
		}
		for _, h := range e.Hooks {
			if strings.Contains(h.Command, "cc-cockpit hook Notification") {
				return true
			}
		}
	}
	return false
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------- mark-ended ----------

func runMarkEnded(args []string) int {
	// Hand-parse so --yes/-y can appear anywhere (matches bash behavior).
	var yes bool
	var posArgs []string
	for _, a := range args {
		switch a {
		case "--yes", "-y":
			yes = true
		default:
			if strings.HasPrefix(a, "-") {
				die("mark-ended", "unknown flag %q", a)
			}
			posArgs = append(posArgs, a)
		}
	}
	if len(posArgs) == 0 {
		die("mark-ended", "need <session_id-prefix> (or 'all-non-ended') [--yes]")
	}
	if len(posArgs) > 1 {
		die("mark-ended", "only one positional argument expected")
	}
	prefix := posArgs[0]

	stateHome := os.Getenv("COCKPIT_STATE_HOME")
	if stateHome == "" {
		die("mark-ended", "COCKPIT_STATE_HOME not set (run inside 'cc-cockpit open')")
	}

	logPath := filepath.Join(stateHome, "events.jsonl")
	f, err := os.Open(logPath)
	if err != nil {
		die("mark-ended", "no events.jsonl in %s", stateHome)
	}
	st := state.Reduce(f)
	_ = f.Close()

	var targets []string
	for sid, sess := range st.Sessions {
		if sess.Status == "ended" {
			continue
		}
		if prefix == "all-non-ended" || strings.HasPrefix(sid, prefix) {
			targets = append(targets, sid)
		}
	}
	sort.Strings(targets)

	if len(targets) == 0 {
		fmt.Printf("mark-ended: no matching non-ended sessions for %q\n", prefix)
		return 0
	}

	if len(targets) > 1 && !yes {
		fmt.Fprintf(os.Stderr, "mark-ended: would dismiss %d session(s):\n", len(targets))
		for _, sid := range targets {
			fmt.Fprintf(os.Stderr, "  - %s\n", sid)
		}
		die("mark-ended", "re-run with --yes to confirm (or give a more specific prefix)")
	}

	for _, sid := range targets {
		ev := map[string]any{
			"event_type": "SessionEnd",
			"session_id": sid,
			"payload":    map[string]any{"synthetic": true, "reason": "operator-dismissed"},
		}
		if err := state.Append(stateHome, ev); err != nil {
			die("mark-ended", "append: %v", err)
		}
		fmt.Printf("  dismissed: %s\n", sid)
	}
	fmt.Printf("mark-ended: %d session(s) dismissed (any later event from a live session will un-dismiss).\n", len(targets))
	return 0
}

// ---------- hook ----------

// runHook is invoked by Claude Code's hook system many times per session.
// It MUST be silent on every error path (any output lands in the Claude pane
// as noise). Panics are recovered; missing env, malformed payload, append
// failures all return 0 silently.
func runHook(args []string) int {
	defer func() { _ = recover() }()

	if len(args) == 0 {
		return 0
	}
	event := args[0]

	if os.Getenv("COCKPIT_SESSION_ACTIVE") != "1" {
		return 0
	}
	stateHome := os.Getenv("COCKPIT_STATE_HOME")
	if stateHome == "" {
		return 0
	}

	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 0
	}
	var payload map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &payload)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	sid, _ := payload["session_id"].(string)
	if sid == "" {
		return 0
	}

	env := hook.Env{
		PrimaryRepo:          os.Getenv("COCKPIT_PRIMARY_REPO"),
		DeclaredRelatedRepos: os.Getenv("COCKPIT_DECLARED_RELATED_REPOS"),
		TaskName:             os.Getenv("COCKPIT_TASK_NAME"),
		ZellijPaneID:         os.Getenv("ZELLIJ_PANE_ID"),
	}
	ev := hook.Build(event, sid, payload, env)
	if ev == nil {
		return 0
	}
	_ = state.Append(stateHome, ev)
	return 0
}

// ---------- dashboard ----------

func runDashboard(args []string) int {
	if len(args) > 0 {
		die("dashboard", "unexpected arguments: %v", args)
	}
	stateHome := os.Getenv("COCKPIT_STATE_HOME")
	if stateHome == "" {
		die("dashboard", "COCKPIT_STATE_HOME not set (run inside 'cc-cockpit open')")
	}
	workspaceName := os.Getenv("COCKPIT_WORKSPACE_NAME")
	if workspaceName == "" {
		workspaceName = "?"
	}
	if err := dashboard.Run(stateHome, workspaceName); err != nil {
		die("dashboard", err.Error())
	}
	return 0
}
