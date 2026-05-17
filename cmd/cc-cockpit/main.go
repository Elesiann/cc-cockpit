// cc-cockpit — workspace supervisor for parallel Claude Code sessions.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/elesiann/cc-cockpit/internal/dashboard"
	"github.com/elesiann/cc-cockpit/internal/hook"
	"github.com/elesiann/cc-cockpit/internal/install"
	"github.com/elesiann/cc-cockpit/internal/state"
	"github.com/elesiann/cc-cockpit/internal/tmux"
	"github.com/elesiann/cc-cockpit/internal/workspace"
)

// Version is the binary's reported version. Overridden at release time via:
//
//	go build -ldflags="-X main.Version=<tag>"
var Version = "0.6.1"

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
	case "end":
		os.Exit(runEnd(args))
	case "hook":
		os.Exit(runHook(args))
	case "dashboard":
		os.Exit(runDashboard(args))
	case "watch":
		os.Exit(runWatch(args))
	case "reduce":
		os.Exit(runReduce(args))
	case "install", "setup":
		os.Exit(runInstall(args))
	case "open":
		os.Exit(runOpen(args))
	case "close":
		os.Exit(runClose(args))
	case "start":
		os.Setenv("CC_COCKPIT_CMD_NAME", "start")
		os.Exit(runSpawn(args))
	case "start-fleet":
		os.Setenv("CC_COCKPIT_CMD_NAME", "start-fleet")
		os.Exit(runStartFleet(args))
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "cc-cockpit: unknown subcommand %q (try --help)\n", cmd)
		os.Exit(2)
	}
}

func usage() {
	fmt.Println(`cc-cockpit ` + Version + `

Subcommands:
  install             install the binary on PATH and Claude Code hooks
  init                create .cc-cockpit/workspace.json
  doctor              check install + workspace health
  open                open the cockpit (launches tmux with dashboard + control)
  close [<ws>]        close the cockpit (kill the workspace's tmux session); --all kills every cockpit session
  watch               headless dashboard: aggregate every workspace's sessions in any terminal
  start [<repo>] <task>   open a Claude pane in repos[<repo>] running the given task (repo auto-detected from cwd if inside a repo)
  start-fleet <repo> ...  open an Agent View pane scoped to repos[<repo>] (multi-agent)
  end <prefix>            end a session (synthetic SessionEnd) and close its tmux pane
  hook <Event>        internal: ingest a Claude Code hook payload
  dashboard           internal: render the dashboard pane (loop until SIGTERM)
  reduce              read events.jsonl on stdin, write reduced state JSON to stdout
  --version           print version
  help                show this message`)
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

	keys := make([]string, 0, len(ws.Repos))
	for k := range ws.Repos {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	fmt.Printf("workspace: %s\nconfig: %s\n\nrepos:\n", wsName, wsPath)
	for _, k := range keys {
		fmt.Printf("  %-16s %s\n", k, ws.Repos[k])
	}
	fmt.Printf("\nnext:\n  cc-cockpit open\n  cc-cockpit start %s <task>\n", keys[0])
	return 0
}

// ---------- doctor ----------

func runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	issues := 0
	ok := func(format string, args ...any) {
		fmt.Printf("ok: "+format+"\n", args...)
	}
	// failFix prints `fail: <desc>` followed by an indented `→ fix: <hint>`
	// line so every failure tells the operator what to do next instead of
	// just what's wrong.
	failFix := func(fix, format string, args ...any) {
		fmt.Printf("fail: "+format+"\n", args...)
		fmt.Printf("  → fix: %s\n", fix)
		issues++
	}

	for _, tool := range []string{"tmux", "claude", "cc-cockpit"} {
		path, err := exec.LookPath(tool)
		switch {
		case err == nil:
			ok("%s found: %s", tool, path)
		case tool == "cc-cockpit":
			failFix("cc-cockpit install", "cc-cockpit not found on PATH")
		case tool == "tmux":
			failFix("install tmux 3.0+ (apt/brew/etc)", "tmux not found on PATH")
		case tool == "claude":
			failFix("install Claude Code from claude.com/claude-code", "claude not found on PATH")
		}
	}
	if v, err := tmux.Version(); err == nil {
		if checkTmuxVersion(v) {
			ok("tmux version: %s", v)
		} else {
			failFix("install tmux 3.0+ (apt/brew/etc)", "tmux version too old: %s (need 3.0+)", v)
		}
	}

	settingsPath := envOrDefault("CLAUDE_SETTINGS_PATH", filepath.Join(homeDir(), ".claude", "settings.json"))
	settingsRaw, err := os.ReadFile(settingsPath)
	if err != nil {
		failFix("cc-cockpit install", "Claude settings not found: %s", settingsPath)
	} else {
		var top struct {
			Hooks map[string][]any `json:"hooks"`
		}
		if err := json.Unmarshal(settingsRaw, &top); err != nil {
			failFix("cc-cockpit install (rewrites the hooks block)", "Claude settings invalid: %v", err)
		} else {
			for _, ev := range install.Events {
				found := false
				for _, e := range top.Hooks[ev] {
					if install.EntryHasCockpitHook(e) {
						found = true
						break
					}
				}
				if found {
					ok("Claude hook installed: %s", ev)
				} else {
					failFix("cc-cockpit install", "Claude hook missing: %s", ev)
				}
			}
			matcherOK := false
			for _, e := range top.Hooks[state.EventNotification] {
				if install.EntryHasMatcher(e, "idle_prompt|permission_prompt") {
					matcherOK = true
					break
				}
			}
			if matcherOK {
				ok("Notification hook matcher is idle_prompt|permission_prompt")
			} else {
				failFix("cc-cockpit install", "Notification hook matcher missing idle_prompt|permission_prompt")
			}
		}
	}

	// Workspace.
	cwd, _ := os.Getwd()
	root := workspace.FindRoot(cwd)
	if root == "" {
		failFix("cd to the workspace parent and run 'cc-cockpit init'", "workspace not initialized")
	} else {
		ws, err := workspace.Load(root)
		switch {
		case err != nil:
			failFix("edit .cc-cockpit/workspace.json (see README for schema)", "workspace.json invalid: %v", err)
		case !workspace.ValidSlug(ws.Name):
			failFix("rename workspace.name to lowercase-with-dashes (matches ^[a-zA-Z0-9][a-zA-Z0-9._-]*$)", "invalid workspace name %q", ws.Name)
		default:
			ok("workspace: %s (%s)", ws.Name, root)
			if len(ws.Repos) == 0 {
				failFix("cc-cockpit init <label>=<path> ... (or edit workspace.json .repos)", "workspace has no repos configured")
			}
			for _, k := range sortedKeys(ws.Repos) {
				rel := ws.Repos[k]
				if !workspace.ValidSlug(k) {
					failFix("rename the label to lowercase-with-dashes in workspace.json", "invalid repo label %q", k)
					continue
				}
				if err := workspace.CheckRepo(root, rel); err != nil {
					failFix("check the path exists and is a git repo (or remove the entry)", "repo '%s': %v", k, err)
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

var tmuxVersionRe = regexp.MustCompile(`(\d+)\.(\d+)`)

// checkTmuxVersion returns true if version string parses to >= 3.0. Falls
// open (returns true) for unparseable versions like "tmux master" so dev
// builds aren't penalized.
func checkTmuxVersion(v string) bool {
	m := tmuxVersionRe.FindStringSubmatch(v)
	if len(m) < 3 {
		return true
	}
	major, _ := strconv.Atoi(m[1])
	return major >= 3
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

func runEnd(args []string) int {
	// Hand-parse so --yes/-y can appear anywhere (Go's flag pkg stops at the
	// first positional, which would reject `end all-non-ended --yes`).
	var yes bool
	var posArgs []string
	for _, a := range args {
		switch a {
		case "--yes", "-y":
			yes = true
		default:
			if strings.HasPrefix(a, "-") {
				die("end", "unknown flag %q", a)
			}
			posArgs = append(posArgs, a)
		}
	}
	if len(posArgs) == 0 {
		die("end", "need <session_id-prefix> (or 'all-non-ended') [--yes]")
	}
	if len(posArgs) > 1 {
		die("end", "only one positional argument expected")
	}
	prefix := posArgs[0]

	stateHome := os.Getenv("COCKPIT_STATE_HOME")
	if stateHome == "" {
		die("end", "COCKPIT_STATE_HOME not set (run inside 'cc-cockpit open')")
	}

	f, err := os.Open(filepath.Join(stateHome, "events.jsonl"))
	if err != nil {
		die("end", "no events.jsonl in %s", stateHome)
	}
	st := state.Reduce(f)
	_ = f.Close()

	var targets []string
	for sid, sess := range st.Sessions {
		if sess.Status == state.StatusEnded {
			continue
		}
		if prefix == "all-non-ended" || strings.HasPrefix(sid, prefix) {
			targets = append(targets, sid)
		}
	}
	sort.Strings(targets)

	if len(targets) == 0 {
		fmt.Printf("end: no matching non-ended sessions for %q\n", prefix)
		return 0
	}

	if len(targets) > 1 && !yes {
		fmt.Fprintf(os.Stderr, "end: would end %d session(s):\n", len(targets))
		for _, sid := range targets {
			fmt.Fprintf(os.Stderr, "  - %s\n", sid)
		}
		die("end", "re-run with --yes to confirm (or give a more specific prefix)")
	}

	for _, sid := range targets {
		if err := appendSyntheticEnd(stateHome, sid, "operator-dismissed"); err != nil {
			die("end", "append: %v", err)
		}
		fmt.Printf("  ended: %s\n", sid)
		// Close the tmux pane if we know one. Fleet/bg sessions have pane_id
		// null — skip them (operator can't single-out one bg agent's "pane").
		// Ignore errors: a pane that died between Reduce and now is a benign
		// race. The pane-exited hook that fires next will be a no-op because
		// the synthetic end we just wrote already marks the session ended.
		var paneID string
		if err := json.Unmarshal(st.Sessions[sid].PaneID, &paneID); err == nil && paneID != "" {
			if err := tmux.KillPane(paneID); err == nil {
				fmt.Printf("    closed pane %s\n", paneID)
			}
		}
	}
	fmt.Printf("end: %d session(s) ended (any later event un-ends).\n", len(targets))
	return 0
}

// ---------- hook ----------

// runHook is invoked by Claude Code many times per session. Output lands in
// the Claude pane, so every error path — including panics — must be silent.
func runHook(args []string) int {
	defer func() { _ = recover() }()

	if len(args) == 0 {
		return 0
	}
	event := args[0]

	if event == "PaneExited" {
		return runPaneExitedHook(args[1:])
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
	cwd, _ := payload["cwd"].(string)

	resolver := &hook.Resolver{
		HomeDir:   homeDir(),
		Getenv:    os.Getenv,
		ReadFile:  os.ReadFile,
		EvalLinks: filepath.EvalSymlinks,
		FindRoot:  workspace.FindRoot,
		LoadWS:    workspace.Load,
		Sleep:     time.Sleep,
	}
	stateHome, paneID := resolver.Resolve(sid, cwd)
	if stateHome == "" {
		return 0
	}

	env := hook.Env{
		PrimaryRepo:          os.Getenv("COCKPIT_PRIMARY_REPO"),
		DeclaredRelatedRepos: os.Getenv("COCKPIT_DECLARED_RELATED_REPOS"),
		TaskName:             os.Getenv("COCKPIT_TASK_NAME"),
		PaneID:               paneID,
	}
	ev := hook.Build(event, sid, payload, env)
	if ev == nil {
		return 0
	}
	_ = state.Append(stateHome, ev)
	return 0
}

// runPaneExitedHook fires from tmux's pane-exited hook. stateHome is embedded
// in the hook command at install time so we don't depend on XDG_STATE_HOME
// being set in the tmux server's env. Looks up the session whose SessionStart
// recorded the dying pane and emits a synthetic SessionEnd.
func runPaneExitedHook(args []string) int {
	if len(args) < 2 {
		return 0
	}
	stateHome, paneID := args[0], args[1]
	if stateHome == "" || paneID == "" {
		return 0
	}

	f, err := os.Open(filepath.Join(stateHome, "events.jsonl"))
	if err != nil {
		return 0
	}
	st := state.Reduce(f)
	_ = f.Close()

	paneJSON, _ := json.Marshal(paneID)
	var targetSID string
	for sid, sess := range st.Sessions {
		if sess.Status == state.StatusEnded {
			continue
		}
		if bytes.Equal(sess.PaneID, paneJSON) {
			targetSID = sid
			break
		}
	}
	if targetSID == "" {
		return 0
	}
	_ = appendSyntheticEnd(stateHome, targetSID, "pane-exited")
	return 0
}

// appendSyntheticEnd writes a SessionEnd event tagged synthetic. The reducer's
// pre-revive logic un-dismisses sessions that emit a later real event.
func appendSyntheticEnd(stateHome, sid, reason string) error {
	return state.Append(stateHome, map[string]any{
		"event_type": state.EventSessionEnd,
		"session_id": sid,
		"payload":    map[string]any{"synthetic": true, "reason": reason},
	})
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
	src := dashboard.SingleSource{StateHome: stateHome, WorkspaceName: workspaceName}
	opts := dashboard.Options{ApplyTmuxBorderColors: true, WriteCurrentJSON: true}
	if err := dashboard.Run(src, opts); err != nil {
		die("dashboard", err.Error())
	}
	return 0
}

// ---------- watch ----------

// runWatch renders an aggregate of every workspace under the cc-cockpit state
// root in the current terminal. Read-only, no tmux, no spawn. Exits on
// SIGINT/SIGTERM.
func runWatch(args []string) int {
	if len(args) > 0 {
		die("watch", "unexpected arguments: %v", args)
	}
	root := dashboard.DefaultStateRoot(homeDir(), os.Getenv)
	src := dashboard.AggregateSource{StateRoot: root}
	if err := dashboard.Run(src, dashboard.Options{}); err != nil {
		die("watch", err.Error())
	}
	return 0
}

// ---------- reduce ----------

// runReduce reads events.jsonl on stdin and prints the reduced state as
// pretty-printed JSON. Used by the smoke test and as a debugging aid:
//
//	cc-cockpit reduce < ~/.local/state/cc-cockpit/<ws>/events.jsonl
func runReduce(args []string) int {
	if len(args) > 0 {
		die("reduce", "unexpected arguments: %v", args)
	}
	st := state.Reduce(os.Stdin)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(st); err != nil {
		fmt.Fprintln(os.Stderr, "reduce:", err)
		return 1
	}
	return 0
}

// ---------- install ----------

func runInstall(args []string) int {
	binDir := envOrDefault("CC_COCKPIT_BIN_DIR", filepath.Join(homeDir(), ".local", "bin"))
	settings := envOrDefault("CLAUDE_SETTINGS_PATH", filepath.Join(homeDir(), ".claude", "settings.json"))
	doBin, doHooks := true, true

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--bin-dir":
			if i+1 >= len(args) {
				die("install", "--bin-dir requires a value")
			}
			binDir = args[i+1]
			i++
		case "--settings":
			if i+1 >= len(args) {
				die("install", "--settings requires a value")
			}
			settings = args[i+1]
			i++
		case "--no-bin":
			doBin = false
		case "--no-hooks":
			doHooks = false
		default:
			die("install", "unknown flag %q", args[i])
		}
	}

	selfPath, err := resolveSelfPath()
	if err != nil {
		die("install", "cannot determine binary path: %v", err)
	}

	binLink := filepath.Join(binDir, "cc-cockpit")
	if doBin {
		if err := install.InstallBin(binDir, selfPath); err != nil {
			die("install", "%v", err)
		}
		fmt.Printf("install: installed %s -> %s\n", binLink, selfPath)
	}

	if doHooks {
		if err := install.InstallHooks(settings, binLink); err != nil {
			die("install", "%v", err)
		}
		fmt.Printf("install: installed Claude Code hooks in %s\n", settings)
	}

	if _, err := exec.LookPath("tmux"); err != nil {
		fmt.Fprintln(os.Stderr, "install: warning: tmux not found on PATH (need 3.0+)")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		fmt.Fprintln(os.Stderr, "install: warning: claude not found on PATH")
	}
	fmt.Println("install: done")
	return 0
}

// ---------- open ----------

// liveLockFd is intentionally never closed: the flock releases when the fd
// closes, and we want the lock held until tmux exits.
var liveLockFd *os.File

// controlBashrc is written to <stateHome>/control.bashrc on each `open` and
// passed as `bash --rcfile` to the control pane. Sources the user's normal
// ~/.bashrc first so PS1/history/completions are preserved, then layers the
// cc-cockpit verb aliases so users can type `start api fix bug` instead of
// `cc-cockpit start api fix bug`. Aliases are scoped to this bash; other
// shells on the system are unaffected.
const controlBashrc = `# cc-cockpit control pane shell init (auto-generated).
if [ -f "$HOME/.bashrc" ]; then
    . "$HOME/.bashrc"
fi

alias start='cc-cockpit start'
alias start-fleet='cc-cockpit start-fleet'
alias end='cc-cockpit end'
alias close='cc-cockpit close'
alias doctor='cc-cockpit doctor'
`

func runOpen(args []string) int {
	if len(args) > 0 {
		die("open", "unexpected arguments: %v", args)
	}
	cwd, err := os.Getwd()
	if err != nil {
		die("open", "cannot determine current directory: %v", err)
	}
	root := workspace.FindRoot(cwd)
	if root == "" {
		if err := workspace.AutoInitIfMissing(cwd); err != nil {
			die("open", "auto-init: %v", err)
		}
		root = cwd
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		die("open", "cannot canonicalize workspace path: %v", err)
	}
	ws, err := workspace.Load(root)
	if err != nil {
		die("open", "%v", err)
	}
	if err := install.EnsureHooks(""); err != nil {
		die("open", "auto-install hooks: %v", err)
	}
	if !workspace.ValidSlug(ws.Name) {
		die("open", "invalid workspace name %q (must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$)", ws.Name)
	}
	if strings.ContainsAny(ws.Name, ".:") {
		die("open", "workspace name %q contains '.' or ':', which tmux session names cannot use; rename the workspace", ws.Name)
	}

	stateHome := hook.ComputeStateHome(homeDir(), os.Getenv, ws.Name)
	if err := os.MkdirAll(stateHome, 0o755); err != nil {
		die("open", "cannot create state dir %q: %v", stateHome, err)
	}

	rcPath := filepath.Join(stateHome, "control.bashrc")
	if err := os.WriteFile(rcPath, []byte(controlBashrc), 0o644); err != nil {
		die("open", "cannot write control rcfile %q: %v", rcPath, err)
	}

	if err := bindWorkspace(stateHome, canonical, ws.Name); err != nil {
		die("open", "%v", err)
	}

	if _, err := exec.LookPath("tmux"); err != nil {
		die("open", "tmux not found on PATH (need 3.0+)")
	}

	if err := acquireLiveLock(stateHome, ws.Name); err != nil {
		die("open", "%v", err)
	}

	sessionEnv := []string{
		"COCKPIT_STATE_HOME=" + stateHome,
		"COCKPIT_WORKSPACE_NAME=" + ws.Name,
		"CC_COCKPIT_WORKSPACE_ROOT=" + canonical,
	}
	if !tmux.HasSession(ws.Name) {
		if err := tmux.NewSession(ws.Name, sessionEnv, "bash", "--rcfile", rcPath); err != nil {
			die("open", "%v", err)
		}
	}

	// Install (or refresh) the per-session pane-exited hook so a crashed
	// Claude pane auto-emits a synthetic SessionEnd. Per-session (not global)
	// so opening another workspace doesn't stomp this one's hook. stateHome
	// is embedded at install time — tmux's run-shell inherits the server env,
	// where XDG_STATE_HOME may not be set. Best-effort; assumes selfPath /
	// stateHome have no shell metacharacters.
	if selfPath, err := resolveSelfPath(); err == nil {
		_ = tmux.SetPaneExitedHook(ws.Name, selfPath+" hook PaneExited "+stateHome+" #{hook_pane}")
	}

	fmt.Printf("cc-cockpit: workspace=%s  state=%s\n", ws.Name, stateHome)

	if err := tmux.Attach(ws.Name); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr.ExitCode()
		}
		die("open", "tmux: %v", err)
	}
	return 0
}

// ---------- close ----------

func runClose(args []string) int {
	var killAll, yes bool
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--all":
			killAll = true
		case "--yes", "-y":
			yes = true
		default:
			if strings.HasPrefix(a, "-") {
				die("close", "unknown flag %q", a)
			}
			positional = append(positional, a)
		}
	}
	if killAll && len(positional) > 0 {
		die("close", "--all does not take a workspace argument")
	}
	if len(positional) > 1 {
		die("close", "expected at most one workspace argument")
	}

	if killAll {
		sessions := tmux.ListSessions()
		if len(sessions) == 0 {
			fmt.Println("close: no cockpit sessions running")
			return 0
		}
		if !yes {
			fmt.Fprintf(os.Stderr, "close: would kill %d cockpit session(s):\n", len(sessions))
			for _, s := range sessions {
				fmt.Fprintf(os.Stderr, "  - %s\n", s)
			}
			die("close", "re-run with --yes to confirm")
		}
		if err := tmux.KillServer(); err != nil {
			die("close", "kill-server: %v", err)
		}
		fmt.Printf("close: killed cockpit server (%d session(s))\n", len(sessions))
		return 0
	}

	// Single-workspace flow. Discover the name from (in priority order):
	// explicit positional, COCKPIT_WORKSPACE_NAME env (set when run from
	// inside the cockpit), then walk up cwd to a workspace.json.
	var wsName string
	switch {
	case len(positional) == 1:
		wsName = positional[0]
	case os.Getenv("COCKPIT_WORKSPACE_NAME") != "":
		wsName = os.Getenv("COCKPIT_WORKSPACE_NAME")
	default:
		cwd, err := os.Getwd()
		if err != nil {
			die("close", "cannot determine cwd: %v", err)
		}
		root := workspace.FindRoot(cwd)
		if root == "" {
			die("close", "not inside a workspace and no name given; run 'cc-cockpit close <workspace>' or 'cc-cockpit close --all'")
		}
		ws, err := workspace.Load(root)
		if err != nil {
			die("close", "workspace.json: %v", err)
		}
		wsName = ws.Name
	}

	if !tmux.HasSession(wsName) {
		fmt.Printf("close: workspace %q has no live tmux session\n", wsName)
		return 0
	}

	if !yes {
		// Best-effort: count live (non-ended) sessions so the operator knows
		// what they're about to terminate. Falls back to a generic prompt if
		// state can't be read.
		liveCount := -1
		stateHome := hook.ComputeStateHome(homeDir(), os.Getenv, wsName)
		if f, err := os.Open(filepath.Join(stateHome, "events.jsonl")); err == nil {
			st := state.Reduce(f)
			_ = f.Close()
			liveCount = 0
			for _, s := range st.Sessions {
				if s.Status != state.StatusEnded {
					liveCount++
				}
			}
		}
		if liveCount >= 0 {
			fmt.Fprintf(os.Stderr, "close: about to close cockpit %q (%d live session(s) will be terminated)\n", wsName, liveCount)
		} else {
			fmt.Fprintf(os.Stderr, "close: about to close cockpit %q\n", wsName)
		}
		die("close", "re-run with --yes to confirm")
	}

	if err := tmux.KillSession(wsName); err != nil {
		die("close", "kill-session: %v", err)
	}
	fmt.Printf("close: killed cockpit session %q\n", wsName)
	return 0
}

func bindWorkspace(stateHome, canonical, name string) error {
	initLock := filepath.Join(stateHome, "init.lock")
	fd, err := os.OpenFile(initLock, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("init.lock: %w", err)
	}
	defer fd.Close()
	if err := unix.Flock(int(fd.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(fd.Fd()), unix.LOCK_UN)

	wrPath := filepath.Join(stateHome, "workspace_root")
	existing, err := os.ReadFile(wrPath)
	switch {
	case err == nil:
		existingStr := strings.TrimSpace(string(existing))
		if existingStr != canonical {
			return fmt.Errorf("workspace %q already bound to %q (current: %q); rename workspace or rm -rf %s",
				name, existingStr, canonical, stateHome)
		}
	case errors.Is(err, os.ErrNotExist):
		if err := os.WriteFile(wrPath, []byte(canonical+"\n"), 0o644); err != nil {
			return err
		}
	default:
		return err
	}

	logPath := filepath.Join(stateHome, "events.jsonl")
	if f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		_ = f.Close()
	}
	return nil
}

func acquireLiveLock(stateHome, name string) error {
	pidFile := filepath.Join(stateHome, "cockpit.live.pid")
	lockFile := filepath.Join(stateHome, "cockpit.live.lock")

	var existingHolder string
	if data, err := os.ReadFile(pidFile); err == nil && len(data) > 0 {
		existingHolder = " (pid " + strings.TrimSpace(string(data)) + ")"
	}

	fd, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return fmt.Errorf("cannot open lock file: %w", err)
	}
	if err := unix.Flock(int(fd.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = fd.Close()
		return fmt.Errorf("cockpit already running for %q%s (stale? rm -f %s %s)",
			name, existingHolder, lockFile, pidFile)
	}
	liveLockFd = fd

	_ = os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
	return nil
}

// ---------- spawn / start ----------

// detectRepoFromCwd returns the label in ws.Repos whose absolute path is the
// longest prefix of cwd. Returns "" when cwd is outside every repo, when
// workspace lookups fail, or when a label fails to resolve (which usually
// means the directory was renamed since `init`; doctor will flag it).
func detectRepoFromCwd(ws *workspace.Workspace, wsRoot string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real
	}
	var bestLabel string
	var bestLen int
	for label := range ws.Repos {
		abs, err := ws.Resolve(wsRoot, label)
		if err != nil {
			continue
		}
		if cwd == abs || strings.HasPrefix(cwd, abs+string(filepath.Separator)) {
			if len(abs) > bestLen {
				bestLabel = label
				bestLen = len(abs)
			}
		}
	}
	return bestLabel
}

func runSpawn(args []string) int {
	cmdName := envOrDefault("CC_COCKPIT_CMD_NAME", "start")

	var repo, task, related string
	var positional []string
	sep := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if sep {
			positional = append(positional, a)
			continue
		}
		switch a {
		case "--repo", "--task", "--related":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				die(cmdName, "%s requires a value", a)
			}
			v := args[i+1]
			i++
			switch a {
			case "--repo":
				repo = v
			case "--task":
				task = v
			case "--related":
				related = v
			}
		case "--":
			sep = true
		default:
			if strings.HasPrefix(a, "-") {
				die(cmdName, "unknown flag %q", a)
			}
			positional = append(positional, a)
		}
	}

	stateHome := os.Getenv("COCKPIT_STATE_HOME")
	if stateHome == "" {
		die(cmdName, "must be run inside the cockpit (run 'cc-cockpit open' first)")
	}
	wsRoot := os.Getenv("CC_COCKPIT_WORKSPACE_ROOT")
	if wsRoot == "" {
		die(cmdName, "CC_COCKPIT_WORKSPACE_ROOT not set")
	}
	workspaceName := envOrDefault("COCKPIT_WORKSPACE_NAME", "?")

	ws, err := workspace.Load(wsRoot)
	if err != nil {
		die(cmdName, "workspace.json: %v", err)
	}
	if ws.Repos == nil {
		die(cmdName, "workspace.json .repos must be an object { \"<label>\": \"<path>\", ... }")
	}

	// Repo resolution priority:
	//   1. --repo flag (explicit override).
	//   2. First positional, if it names a known label in ws.Repos.
	//   3. cwd auto-detect: longest-prefix match against ws.Repos values.
	//   4. Else error.
	taskStart := 0
	if repo == "" && len(positional) > 0 {
		if _, ok := ws.Repos[positional[0]]; ok {
			repo = positional[0]
			taskStart = 1
		}
	}
	if repo == "" {
		repo = detectRepoFromCwd(ws, wsRoot)
	}
	if taskStart < len(positional) {
		if task != "" {
			die(cmdName, "unexpected positional task %q", positional[taskStart])
		}
		task = strings.Join(positional[taskStart:], " ")
	}

	if repo == "" {
		die(cmdName, "repo required (usage: cc-cockpit %s [<repo>] <task...>; auto-detect needs cwd inside a known repo)", cmdName)
	}
	if task == "" {
		die(cmdName, "task required (usage: cc-cockpit %s [<repo>] <task...>)", cmdName)
	}
	abs, err := ws.Resolve(wsRoot, repo)
	if err != nil {
		die(cmdName, "%v", err)
	}

	if _, err := exec.LookPath("claude"); err != nil {
		die(cmdName, "'claude' not found on PATH")
	}

	paneName := repo + ": " + task
	if len(paneName) > 60 {
		paneName = paneName[:60]
	}

	windowEnv := []string{
		"COCKPIT_SESSION_ACTIVE=1",
		"COCKPIT_STATE_HOME=" + stateHome,
		"COCKPIT_WORKSPACE_NAME=" + workspaceName,
		"COCKPIT_PRIMARY_REPO=" + repo,
		"COCKPIT_DECLARED_RELATED_REPOS=" + related,
		"COCKPIT_TASK_NAME=" + task,
	}
	if _, err := tmux.NewClaudePane(workspaceName, paneName, abs, windowEnv, "claude"); err != nil {
		die(cmdName, "%v", err)
	}
	return 0
}

func runStartFleet(args []string) int {
	cmdName := envOrDefault("CC_COCKPIT_CMD_NAME", "start-fleet")

	var related string
	var positional []string
	sep := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		if sep {
			positional = append(positional, a)
			continue
		}
		switch a {
		case "--related":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--") {
				die(cmdName, "%s requires a value", a)
			}
			related = args[i+1]
			i++
		case "--":
			sep = true
		default:
			if strings.HasPrefix(a, "-") {
				die(cmdName, "unknown flag %q", a)
			}
			positional = append(positional, a)
		}
	}
	if len(positional) == 0 {
		die(cmdName, "repo required (usage: cc-cockpit %s <repo> [<name>...])", cmdName)
	}
	repo := positional[0]
	name := strings.Join(positional[1:], " ")

	stateHome := os.Getenv("COCKPIT_STATE_HOME")
	if stateHome == "" {
		die(cmdName, "must be run inside the cockpit (run 'cc-cockpit open' first)")
	}
	wsRoot := os.Getenv("CC_COCKPIT_WORKSPACE_ROOT")
	if wsRoot == "" {
		die(cmdName, "CC_COCKPIT_WORKSPACE_ROOT not set")
	}
	workspaceName := envOrDefault("COCKPIT_WORKSPACE_NAME", "?")

	ws, err := workspace.Load(wsRoot)
	if err != nil {
		die(cmdName, "workspace.json: %v", err)
	}
	if ws.Repos == nil {
		die(cmdName, "workspace.json .repos must be an object { \"<label>\": \"<path>\", ... }")
	}
	abs, err := ws.Resolve(wsRoot, repo)
	if err != nil {
		die(cmdName, "%v", err)
	}

	if _, err := exec.LookPath("claude"); err != nil {
		die(cmdName, "'claude' not found on PATH")
	}

	paneName := "fleet · " + repo
	if name != "" {
		paneName += ": " + name
	}
	if len(paneName) > 60 {
		paneName = paneName[:60]
	}

	windowEnv := []string{
		"COCKPIT_SESSION_ACTIVE=1",
		"COCKPIT_STATE_HOME=" + stateHome,
		"COCKPIT_WORKSPACE_NAME=" + workspaceName,
		"COCKPIT_PRIMARY_REPO=" + repo,
		"COCKPIT_DECLARED_RELATED_REPOS=" + related,
	}
	if _, err := tmux.NewClaudePane(workspaceName, paneName, abs, windowEnv, "claude", "agents", "--cwd", abs); err != nil {
		die(cmdName, "%v", err)
	}
	return 0
}

// ---------- helpers ----------

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func homeDir() string {
	h, _ := os.UserHomeDir()
	return h
}

// resolveSelfPath returns the absolute path to the running binary with
// symlinks resolved.
func resolveSelfPath() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real, nil
	}
	return p, nil
}
