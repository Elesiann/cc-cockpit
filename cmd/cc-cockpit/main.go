// cc-cockpit — watch-only attention layer for Claude Code sessions.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/elesiann/cc-cockpit/internal/dashboard"
	"github.com/elesiann/cc-cockpit/internal/hook"
	"github.com/elesiann/cc-cockpit/internal/install"
	"github.com/elesiann/cc-cockpit/internal/state"
	"github.com/elesiann/cc-cockpit/internal/winfocus"
	"github.com/elesiann/cc-cockpit/internal/workspace"
)

// Version is the binary's reported version. Overridden at release time via:
//
//	go build -ldflags="-X main.Version=<tag>"
var Version = "1.0.0"

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
	case "reap":
		os.Exit(runReap(args))
	case "hook":
		os.Exit(runHook(args))
	case "bind-window":
		os.Exit(runBindWindow(args))
	case "focus-window":
		os.Exit(runFocusWindow(args))
	case "watch":
		os.Exit(runWatch(args))
	case "stats":
		os.Exit(runStats(args))
	case "reduce":
		os.Exit(runReduce(args))
	case "install", "setup":
		os.Exit(runInstall(args))
	case "uninstall":
		os.Exit(runUninstall(args))
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
  uninstall           remove cc-cockpit hook entries and the PATH symlink
  init                create optional .cc-cockpit/workspace.json labels
  doctor              check install + optional workspace health
  watch [--ws X,Y]    headless dashboard; --ws scopes to names, --sort controls active rows
  stats [--ws X,Y]    print event/session counts from cc-cockpit state logs
  end <prefix>            end a session in dashboard state; works from any terminal (scans all workspaces)
  reap [--older-than DUR] sweep all workspaces, end every non-ended session whose last activity is older than DUR (default: 1h)
  hook <Event>        internal: ingest a Claude Code hook payload
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
	fmt.Printf("\nnext:\n  cc-cockpit install\n  cc-cockpit watch\n")
	return 0
}

// ---------- doctor ----------

func runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	showState := fs.Bool("state", false, "include cc-cockpit state-log diagnostics")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// Print the running version first so the user (and any pasted bug
	// report) can tell whether they ran 1.0.1 or a stale go install.
	fmt.Printf("cc-cockpit %s\n\n", Version)
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

	for _, tool := range []string{"claude", "cc-cockpit"} {
		path, err := exec.LookPath(tool)
		switch {
		case err == nil:
			ok("%s found: %s", tool, path)
		case tool == "cc-cockpit":
			failFix("cc-cockpit install", "cc-cockpit not found on PATH")
		case tool == "claude":
			failFix("install Claude Code from claude.com/claude-code", "claude not found on PATH")
		}
	}

	settingsPath := envOrDefault("CLAUDE_SETTINGS_PATH", filepath.Join(homeDir(), ".claude", "settings.json"))
	settingsRaw, err := os.ReadFile(settingsPath)
	if err != nil {
		failFix("cc-cockpit install", "Claude settings not found: %s", settingsPath)
	} else {
		if missing, err := install.MissingHookEvents(settingsRaw); err != nil {
			failFix("cc-cockpit install (rewrites the hooks block)", "Claude settings invalid: %v", err)
		} else if len(missing) == 0 {
			ok("Claude hooks installed and executable")
			// Stale-install check: the hook commands point at a specific
			// binary path. After `go install`-ing to a different GOPATH,
			// moving the symlink, or rebuilding to a new location, the
			// settings can keep working (the old binary still exists) but
			// run a different — often older — version than what's on PATH.
			// This is a silent footgun. Flag it.
			running, _ := resolveSelfPath()
			for _, bin := range install.InstalledHookBinaries(settingsRaw) {
				resolved, err := filepath.EvalSymlinks(bin)
				if err != nil {
					resolved = bin
				}
				if running != "" && resolved != running {
					failFix("cc-cockpit install (re-points hooks at this binary)",
						"hooks point at %s, but this binary is %s", bin, running)
				}
			}
		} else {
			failFix("cc-cockpit install", "Claude hooks missing, stale, or pointing at a non-executable cc-cockpit: %s", strings.Join(missing, ", "))
		}
	}

	// Workspace.
	cwd, _ := os.Getwd()
	root := workspace.FindRoot(cwd)
	if root == "" {
		ok("workspace: not initialized (sessions will route to _global)")
	} else {
		ws, err := workspace.Load(root)
		switch {
		case err != nil:
			failFix("edit .cc-cockpit/workspace.json (see README for schema)", "workspace.json invalid: %v", err)
		case !workspace.ValidSlug(ws.Name):
			failFix("rename workspace.name to lowercase-with-dashes (matches ^[a-zA-Z0-9][a-zA-Z0-9._-]*$)", "invalid workspace name %q", ws.Name)
		default:
			ok("workspace: %s (%s)", ws.Name, root)
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

	if *showState {
		stateIssues := printDoctorState(settingsRaw)
		issues += stateIssues
	}

	if issues == 0 {
		fmt.Println("doctor: all checks passed")
		return 0
	}
	fmt.Printf("doctor: %d issue(s) found\n", issues)
	return 1
}

func printDoctorState(settingsRaw []byte) int {
	root := dashboard.DefaultStateRoot(homeDir(), os.Getenv)
	fmt.Printf("\nstate:\n")
	fmt.Printf("  root: %s\n", root)
	issues := 0
	homes := collectStateHomes(root, nil)
	if len(homes) == 0 {
		fmt.Println("  workspace logs: 0")
	} else {
		fmt.Printf("  workspace logs: %d\n", len(homes))
	}
	type logInfo struct {
		name      string
		path      string
		size      int64
		malformed int
	}
	var logs []logInfo
	for _, home := range homes {
		evPath := filepath.Join(home, "events.jsonl")
		info, err := os.Stat(evPath)
		if err != nil {
			continue
		}
		malformed := 0
		if raw, err := state.Snapshot(home); err == nil {
			malformed = state.Reduce(bytes.NewReader(raw)).DroppedEvents
		}
		logs = append(logs, logInfo{
			name:      filepath.Base(home),
			path:      evPath,
			size:      info.Size(),
			malformed: malformed,
		})
	}
	sort.Slice(logs, func(i, j int) bool {
		if logs[i].size != logs[j].size {
			return logs[i].size > logs[j].size
		}
		return logs[i].name < logs[j].name
	})
	if len(logs) > 0 {
		fmt.Println("  largest logs:")
		limit := len(logs)
		if limit > 5 {
			limit = 5
		}
		for _, l := range logs[:limit] {
			fmt.Printf("    - %s: %s malformed=%d\n", l.name, humanBytes(l.size), l.malformed)
		}
	}

	legacy := findLegacyStateFiles(root)
	if len(legacy) == 0 {
		fmt.Println("  legacy files: none")
	} else {
		issues++
		fmt.Println("  legacy files:")
		for _, p := range legacy {
			fmt.Printf("    - %s\n", p)
		}
	}

	if len(settingsRaw) > 0 {
		missing, err := install.MissingHookEvents(settingsRaw)
		if err != nil {
			fmt.Printf("  hooks: invalid settings: %v\n", err)
			issues++
		} else {
			needed := []string{state.EventStopFailure, state.EventPostToolUseFailure, state.EventPostToolBatch}
			var missingNew []string
			for _, ev := range needed {
				if stringInSlice(ev, missing) {
					missingNew = append(missingNew, ev)
				}
			}
			if len(missingNew) == 0 {
				fmt.Println("  hooks: failure/batch events installed")
			} else {
				issues++
				fmt.Printf("  hooks: missing failure/batch events: %s\n", strings.Join(missingNew, ", "))
			}
		}
	}
	return issues
}

func findLegacyStateFiles(root string) []string {
	var out []string
	for _, pattern := range []string{
		filepath.Join(root, "cockpit.live.*"),
		filepath.Join(root, "*", "cockpit.live.*"),
	} {
		matches, _ := filepath.Glob(pattern)
		out = append(out, matches...)
	}
	sort.Strings(out)
	return out
}

func stringInSlice(s string, values []string) bool {
	for _, v := range values {
		if v == s {
			return true
		}
	}
	return false
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(unit), 0
	for n >= div*unit && exp < 4 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ---------- end / reap ----------

// endTarget is one (workspace stateHome, session_id, session) triple
// selected for synthetic SessionEnd emission.
type endTarget struct {
	stateHome string
	sid       string
	sess      *state.Session
}

// collectEndTargets walks every state dir under the cc-cockpit state root
// and returns active sessions where predicate(sid, sess) is true. Aggregates
// across all workspaces so `end` and `reap` work from any terminal.
//
// Each events.jsonl is read through state.Snapshot so the read is
// coordinated with state.Append's exclusive flock — without that, a
// concurrent writer could leave a torn last line in the reader's view and
// `reap` would under-count last_activity by one event per session per
// sweep, missing sessions that are just over the threshold.
func collectEndTargets(predicate func(string, *state.Session) bool) []endTarget {
	root := dashboard.DefaultStateRoot(homeDir(), os.Getenv)
	matches, _ := filepath.Glob(filepath.Join(root, "*", "events.jsonl"))
	var dirs []string
	for _, m := range matches {
		dirs = append(dirs, filepath.Dir(m))
	}
	var targets []endTarget
	for _, sh := range dirs {
		raw, err := state.Snapshot(sh)
		if err != nil {
			continue
		}
		st := state.Reduce(bytes.NewReader(raw))
		for sid, sess := range st.Sessions {
			if sess.Status == state.StatusEnded {
				continue
			}
			if predicate(sid, sess) {
				targets = append(targets, endTarget{sh, sid, sess})
			}
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		if targets[i].stateHome != targets[j].stateHome {
			return targets[i].stateHome < targets[j].stateHome
		}
		return targets[i].sid < targets[j].sid
	})
	return targets
}

// applyEndTargets emits a synthetic SessionEnd per target. This is a
// dashboard-state dismissal only; the underlying Claude process is left alone.
func applyEndTargets(cmdName, reason string, targets []endTarget) {
	for _, t := range targets {
		if err := appendSyntheticEnd(t.stateHome, t.sid, reason); err != nil {
			die(cmdName, "append to %s: %v", t.stateHome, err)
		}
		ws := filepath.Base(t.stateHome)
		fmt.Printf("  ended: [%s] %s\n", ws, t.sid)
	}
}

func runEnd(args []string) int {
	// Hand-parse so --yes/-y/--dry-run can appear anywhere (Go's flag pkg
	// stops at the first positional, which would reject
	// `end all-non-ended --yes`).
	var yes, dryRun bool
	var posArgs []string
	for _, a := range args {
		switch a {
		case "--yes", "-y":
			yes = true
		case "--dry-run", "-n":
			dryRun = true
		default:
			if strings.HasPrefix(a, "-") {
				die("end", "unknown flag %q", a)
			}
			posArgs = append(posArgs, a)
		}
	}
	if len(posArgs) == 0 {
		die("end", "need <session_id-prefix> (or 'all-non-ended') [--yes] [--dry-run]")
	}
	if len(posArgs) > 1 {
		die("end", "only one positional argument expected")
	}
	prefix := posArgs[0]

	predicate := func(sid string, _ *state.Session) bool {
		return prefix == "all-non-ended" || strings.HasPrefix(sid, prefix)
	}
	targets := collectEndTargets(predicate)

	if len(targets) == 0 {
		fmt.Printf("end: no matching non-ended sessions for %q across any workspace\n", prefix)
		return 0
	}

	// --dry-run prints the targets and exits 0 without writing. Symmetric
	// with reap --dry-run; the goal is "show me what would happen" before
	// committing to it.
	if dryRun {
		fmt.Printf("end: would end %d session(s):\n", len(targets))
		for _, t := range targets {
			fmt.Printf("  - [%s] %s\n", filepath.Base(t.stateHome), t.sid)
		}
		return 0
	}

	// `all-non-ended` is always treated as a broad nuke and must require --yes,
	// even when it happens to match a single session. The user's intent at the
	// keyword is "end everything"; silently running without confirmation when
	// the live set happens to be size 1 turns the wildcard into a footgun.
	needsConfirm := len(targets) > 1 || prefix == "all-non-ended"
	if needsConfirm && !yes {
		fmt.Fprintf(os.Stderr, "end: would end %d session(s):\n", len(targets))
		for _, t := range targets {
			fmt.Fprintf(os.Stderr, "  - [%s] %s\n", filepath.Base(t.stateHome), t.sid)
		}
		die("end", "re-run with --yes to confirm (or give a more specific prefix)")
	}

	applyEndTargets("end", "operator-dismissed", targets)
	fmt.Printf("end: %d session(s) ended (any later event un-ends).\n", len(targets))
	return 0
}

func runReap(args []string) int {
	olderThan := time.Hour // default: 1h
	var yes, dryRun bool
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--yes", "-y":
			yes = true
		case "--dry-run", "-n":
			dryRun = true
		case "--older-than":
			if i+1 >= len(args) {
				die("reap", "--older-than requires a value (e.g. 30m, 2h)")
			}
			d, err := time.ParseDuration(args[i+1])
			if err != nil {
				die("reap", "invalid duration %q: %v", args[i+1], err)
			}
			if d <= 0 {
				die("reap", "--older-than must be positive (got %s)", d)
			}
			olderThan = d
			i++
		default:
			die("reap", "unknown argument %q (use --older-than DUR, --dry-run, --yes)", a)
		}
	}

	now := time.Now()
	predicate := func(_ string, sess *state.Session) bool {
		// LastActivity is an RFC3339 string set by the reducer on every event.
		// Sessions with malformed/empty timestamps are conservatively skipped.
		t, err := time.Parse(time.RFC3339, sess.LastActivity)
		if err != nil {
			return false
		}
		return now.Sub(t) > olderThan
	}
	targets := collectEndTargets(predicate)

	if len(targets) == 0 {
		fmt.Printf("reap: no sessions older than %s\n", olderThan)
		return 0
	}

	if dryRun || !yes {
		fmt.Fprintf(os.Stderr, "reap: would end %d session(s) older than %s:\n", len(targets), olderThan)
		for _, t := range targets {
			age := now.Sub(parseActivityOrZero(t.sess.LastActivity)).Round(time.Minute)
			fmt.Fprintf(os.Stderr, "  - [%s] %s  (idle %s)\n", filepath.Base(t.stateHome), t.sid, age)
		}
		if dryRun {
			return 0
		}
		die("reap", "re-run with --yes to confirm")
	}

	applyEndTargets("reap", "stale-reaped", targets)
	fmt.Printf("reap: %d session(s) reaped (idle > %s).\n", len(targets), olderThan)
	return 0
}

func parseActivityOrZero(iso string) time.Time {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ---------- hook ----------

// runHook is invoked by Claude Code many times per session. Hooks run inline
// with Claude, so every error path — including panics — must be silent.
func runHook(args []string) int {
	defer func() { _ = recover() }()

	if len(args) == 0 {
		return 0
	}
	event := args[0]

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
	stateHome := resolver.Resolve(sid, cwd)
	if stateHome == "" {
		return 0
	}

	ev := hook.Build(event, sid, payload)
	if ev == nil {
		return 0
	}
	_ = state.Append(stateHome, ev)

	// On session start, bind this session to its Windows Terminal window so
	// `watch` can later raise it. Resolve the pts here (still parented to
	// claude), then hand the slow capture off to a detached child so we never
	// block Claude's startup.
	if event == state.EventSessionStart {
		maybeBindWindow(sid, stateHome)
	}
	return 0
}

// maybeBindWindow spawns a detached `bind-window` to record the session's
// Windows Terminal HWND. Best-effort and silent: any failure just means the
// focus feature won't work for this session. No-op outside WSL + WT.
func maybeBindWindow(sid, stateHome string) {
	if !winfocus.Enabled() {
		return
	}
	pts := winfocus.FindSessionPTS()
	if pts == "" {
		return
	}
	self, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(self, "bind-window", "--session", sid, "--state-home", stateHome, "--pts", pts)
	// New session so the child outlives this short-lived hook process.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = cmd.Start()
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

// ---------- bind-window / focus-window ----------

// runBindWindow records the calling session's Windows Terminal HWND in a
// sidecar so `watch` can later raise it. Invoked detached by the SessionStart
// hook; also runnable by hand for debugging. Best-effort: failure is reported
// on stderr but never escalated.
func runBindWindow(args []string) int {
	fs := flag.NewFlagSet("bind-window", flag.ContinueOnError)
	session := fs.String("session", "", "session id")
	stateHome := fs.String("state-home", "", "state dir for the session's workspace")
	pts := fs.String("pts", "", "controlling pts (resolved from ancestry if empty)")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *session == "" || *stateHome == "" {
		die("bind-window", "need --session and --state-home")
	}
	if err := winfocus.Capture(*stateHome, *session, *pts); err != nil {
		fmt.Fprintf(os.Stderr, "bind-window: %v\n", err)
		return 1
	}
	return 0
}

// runFocusWindow raises a Windows Terminal window by HWND. Used by hand for
// debugging; `watch` calls winfocus.Focus in-process.
func runFocusWindow(args []string) int {
	if len(args) != 1 {
		die("focus-window", "usage: focus-window <hwnd>")
	}
	if err := winfocus.Focus(args[0]); err != nil {
		die("focus-window", "%v", err)
	}
	return 0
}

// ---------- watch ----------

// runWatch renders an aggregate of every workspace under the cc-cockpit state
// root in the current terminal. Read-only; does not launch or manage Claude
// processes. Exits on SIGINT/SIGTERM. --ws scopes the view to one or more
// workspace names (comma-separated or repeated). --color=never suppresses
// every ANSI escape (per-row /color, gray rollup/recap lines) for clean
// log capture.
func runWatch(args []string) int {
	var allowed []string
	sortMode := dashboard.SortStarted
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--ws":
			if i+1 >= len(args) {
				die("watch", "--ws requires a value (e.g. --ws api,web)")
			}
			allowed = append(allowed, splitCSVNonEmpty(args[i+1])...)
			i++
		case strings.HasPrefix(a, "--ws="):
			allowed = append(allowed, splitCSVNonEmpty(strings.TrimPrefix(a, "--ws="))...)
		case a == "--color=never":
			dashboard.NoColor = true
		case a == "--color=auto":
			dashboard.NoColor = false
		case a == "--sort":
			if i+1 >= len(args) {
				die("watch", "--sort requires a value (started, activity, attention)")
			}
			sortMode = args[i+1]
			i++
		case strings.HasPrefix(a, "--sort="):
			sortMode = strings.TrimPrefix(a, "--sort=")
		default:
			die("watch", "unexpected argument %q (supported: --ws, --color, --sort)", a)
		}
	}
	if sortMode != dashboard.SortStarted && sortMode != dashboard.SortActivity && sortMode != dashboard.SortAttention {
		die("watch", "invalid --sort %q (want started, activity, or attention)", sortMode)
	}
	dashboard.ActiveSort = sortMode
	root := dashboard.DefaultStateRoot(homeDir(), os.Getenv)
	src := dashboard.AggregateSource{StateRoot: root, AllowedWorkspaces: allowed, Sort: sortMode}
	if err := dashboard.Run(src); err != nil {
		die("watch", err.Error())
	}
	return 0
}

// splitCSVNonEmpty trims whitespace and drops empty fields. Used so that
// --ws=api,,web stays {api, web} instead of inserting a "" workspace name
// that could never match.
func splitCSVNonEmpty(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ---------- stats ----------

type statsSession struct {
	Prompts       int
	Stops         int
	Notifications int
	Permissions   int
	ToolCalls     int
	TopTools      map[string]int
}

type statsWorkspace struct {
	Name          string
	Path          string
	Events        int
	Malformed     int
	Prompts       int
	Stops         int
	Notifications int
	Permissions   int
	ToolCalls     int
	TopTools      map[string]int
	Active        int
	Ended         int
	Sessions      map[string]*statsSession
}

func runStats(args []string) int {
	var allowed []string
	var since time.Duration
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--ws":
			if i+1 >= len(args) {
				die("stats", "--ws requires a value (e.g. --ws api,web)")
			}
			allowed = append(allowed, splitCSVNonEmpty(args[i+1])...)
			i++
		case strings.HasPrefix(a, "--ws="):
			allowed = append(allowed, splitCSVNonEmpty(strings.TrimPrefix(a, "--ws="))...)
		case a == "--since":
			if i+1 >= len(args) {
				die("stats", "--since requires a duration (e.g. 24h)")
			}
			d, err := time.ParseDuration(args[i+1])
			if err != nil || d <= 0 {
				die("stats", "invalid --since %q (want positive duration like 24h)", args[i+1])
			}
			since = d
			i++
		case strings.HasPrefix(a, "--since="):
			d, err := time.ParseDuration(strings.TrimPrefix(a, "--since="))
			if err != nil || d <= 0 {
				die("stats", "invalid --since %q (want positive duration like 24h)", strings.TrimPrefix(a, "--since="))
			}
			since = d
		default:
			die("stats", "unexpected argument %q (supported: --ws, --since)", a)
		}
	}

	root := dashboard.DefaultStateRoot(homeDir(), os.Getenv)
	homes := collectStateHomes(root, allowed)
	cutoff := time.Time{}
	if since > 0 {
		cutoff = time.Now().Add(-since)
	}
	fmt.Printf("stats: state_root=%s", root)
	if since > 0 {
		fmt.Printf(" since=%s", since)
	}
	if len(allowed) > 0 {
		fmt.Printf(" ws=%s", strings.Join(allowed, ","))
	}
	fmt.Println()
	if len(homes) == 0 {
		fmt.Println("  (no workspace logs found)")
		return 0
	}
	for _, home := range homes {
		ws, err := readStatsWorkspace(home, cutoff)
		if err != nil {
			fmt.Printf("\n[%s]\n  unreadable: %v\n", filepath.Base(home), err)
			continue
		}
		printStatsWorkspace(ws)
	}
	return 0
}

func collectStateHomes(root string, allowed []string) []string {
	matches, _ := filepath.Glob(filepath.Join(root, "*", "events.jsonl"))
	sort.Strings(matches)
	var allow map[string]bool
	if len(allowed) > 0 {
		allow = make(map[string]bool, len(allowed))
		for _, name := range allowed {
			allow[name] = true
		}
	}
	var homes []string
	for _, evPath := range matches {
		home := filepath.Dir(evPath)
		name := filepath.Base(home)
		if allow != nil && !allow[name] {
			continue
		}
		homes = append(homes, home)
	}
	return homes
}

func readStatsWorkspace(stateHome string, cutoff time.Time) (statsWorkspace, error) {
	raw, err := state.Snapshot(stateHome)
	if err != nil {
		return statsWorkspace{}, err
	}
	st := state.Reduce(bytes.NewReader(raw))
	ws := statsWorkspace{
		Name:      filepath.Base(stateHome),
		Path:      stateHome,
		Malformed: st.DroppedEvents,
		TopTools:  make(map[string]int),
		Sessions:  make(map[string]*statsSession),
	}
	for _, sess := range st.Sessions {
		if sess.Status == state.StatusEnded {
			ws.Ended++
		} else {
			ws.Active++
		}
	}

	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var ev state.Event
		if err := json.Unmarshal(line, &ev); err != nil {
			continue
		}
		ts, err := parseEventTime(ev.WallClockISO8601)
		if err != nil || ev.EventType == "" || ev.SessionID == "" {
			continue
		}
		if !cutoff.IsZero() && ts.Before(cutoff) {
			continue
		}
		ws.Events++
		ss := ws.Sessions[ev.SessionID]
		if ss == nil {
			ss = &statsSession{TopTools: make(map[string]int)}
			ws.Sessions[ev.SessionID] = ss
		}
		switch ev.EventType {
		case state.EventUserPromptSubmit:
			ws.Prompts++
			ss.Prompts++
		case state.EventStop, state.EventStopFailure:
			ws.Stops++
			ss.Stops++
		case state.EventNotification:
			ws.Notifications++
			ss.Notifications++
		case state.EventPermissionRequest:
			ws.Permissions++
			ss.Permissions++
		case state.EventPostToolUse, state.EventPostToolUseFailure:
			tool := eventToolName(ev.Payload)
			ws.ToolCalls++
			ss.ToolCalls++
			if tool != "" {
				ws.TopTools[tool]++
				ss.TopTools[tool]++
			}
		}
	}
	return ws, nil
}

func printStatsWorkspace(ws statsWorkspace) {
	fmt.Printf("\n[%s]\n", ws.Name)
	fmt.Printf("  sessions: active=%d ended=%d malformed=%d events=%d\n", ws.Active, ws.Ended, ws.Malformed, ws.Events)
	fmt.Printf("  counts: prompts=%d stops=%d notifications=%d permissions=%d tool_calls=%d\n",
		ws.Prompts, ws.Stops, ws.Notifications, ws.Permissions, ws.ToolCalls)
	if top := formatTopTools(ws.TopTools, 5); top != "" {
		fmt.Printf("  top_tools: %s\n", top)
	}
	sids := sortedKeys(ws.Sessions)
	for _, sid := range sids {
		ss := ws.Sessions[sid]
		if ss.Prompts == 0 && ss.Stops == 0 && ss.Notifications == 0 && ss.Permissions == 0 && ss.ToolCalls == 0 {
			continue
		}
		line := fmt.Sprintf("  - %s: prompts=%d stops=%d notifications=%d permissions=%d tool_calls=%d",
			shortStatsSID(sid), ss.Prompts, ss.Stops, ss.Notifications, ss.Permissions, ss.ToolCalls)
		if top := formatTopTools(ss.TopTools, 3); top != "" {
			line += " top=" + top
		}
		fmt.Println(line)
	}
}

func eventToolName(raw json.RawMessage) string {
	var p struct {
		ToolName string `json:"tool_name"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.ToolName
}

func parseEventTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

func formatTopTools(counts map[string]int, limit int) string {
	type item struct {
		name  string
		count int
	}
	items := make([]item, 0, len(counts))
	for name, count := range counts {
		if name != "" && count > 0 {
			items = append(items, item{name: name, count: count})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].count != items[j].count {
			return items[i].count > items[j].count
		}
		return items[i].name < items[j].name
	})
	if len(items) > limit {
		items = items[:limit]
	}
	parts := make([]string, 0, len(items))
	for _, it := range items {
		parts = append(parts, fmt.Sprintf("%s %d", it.name, it.count))
	}
	return strings.Join(parts, " · ")
}

func shortStatsSID(sid string) string {
	r := []rune(sid)
	if len(r) <= 8 {
		return sid
	}
	return string(r[:8])
}

// ---------- reduce ----------

// runReduce reads events.jsonl and prints the reduced state as
// pretty-printed JSON. Used by the smoke test and as a debugging aid:
//
//	cc-cockpit reduce                          # read stdin
//	cc-cockpit reduce <path>/events.jsonl     # read a specific file
func runReduce(args []string) int {
	if len(args) > 1 {
		die("reduce", "expected 0 or 1 arguments, got %d", len(args))
	}
	var in io.Reader = os.Stdin
	if len(args) == 1 {
		f, err := os.Open(args[0])
		if err != nil {
			die("reduce", "cannot open %s: %v", args[0], err)
		}
		defer f.Close()
		in = f
	}
	st := state.Reduce(in)
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

	if _, err := exec.LookPath("claude"); err != nil {
		fmt.Fprintln(os.Stderr, "install: warning: claude not found on PATH")
	}
	fmt.Println("install: done")
	return 0
}

// ---------- uninstall ----------

// runUninstall removes cc-cockpit's footprint: hook entries in the Claude
// settings file (preserving everything else) and the binary symlink. Refuses
// to delete a regular file at the symlink path (it might be a manual install
// the user installed deliberately; we'd rather flag it than guess).
//
// Default flags mirror install: --bin-dir / --settings / --no-bin / --no-hooks.
// Idempotent: a second run is a no-op.
func runUninstall(args []string) int {
	binDir := envOrDefault("CC_COCKPIT_BIN_DIR", filepath.Join(homeDir(), ".local", "bin"))
	settings := envOrDefault("CLAUDE_SETTINGS_PATH", filepath.Join(homeDir(), ".claude", "settings.json"))
	doBin, doHooks := true, true

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--bin-dir":
			if i+1 >= len(args) {
				die("uninstall", "--bin-dir requires a value")
			}
			binDir = args[i+1]
			i++
		case "--settings":
			if i+1 >= len(args) {
				die("uninstall", "--settings requires a value")
			}
			settings = args[i+1]
			i++
		case "--no-bin":
			doBin = false
		case "--no-hooks":
			doHooks = false
		default:
			die("uninstall", "unknown flag %q", args[i])
		}
	}

	if doHooks {
		removed, err := install.UninstallHooks(settings)
		if err != nil {
			die("uninstall", "%v", err)
		}
		if removed == 0 {
			fmt.Printf("uninstall: no cc-cockpit hooks in %s\n", settings)
		} else {
			fmt.Printf("uninstall: removed %d cc-cockpit hook entr%s from %s\n", removed, plural(removed, "y", "ies"), settings)
		}
	}

	if doBin {
		removed, err := install.UninstallBin(binDir)
		if err != nil {
			die("uninstall", "%v", err)
		}
		target := filepath.Join(binDir, "cc-cockpit")
		if removed {
			fmt.Printf("uninstall: removed %s\n", target)
		} else {
			fmt.Printf("uninstall: no symlink at %s\n", target)
		}
	}

	fmt.Println("uninstall: done")
	fmt.Println("note: per-workspace event logs under the state root are kept.")
	fmt.Println("      run `rm -rf ~/.local/state/cc-cockpit` (or $XDG_STATE_HOME/cc-cockpit) to clear them.")
	return 0
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
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
