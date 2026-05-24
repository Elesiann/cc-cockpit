// cc-cockpit — watch-only attention layer for Claude Code sessions.
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
	"time"

	"github.com/elesiann/cc-cockpit/internal/dashboard"
	"github.com/elesiann/cc-cockpit/internal/hook"
	"github.com/elesiann/cc-cockpit/internal/install"
	"github.com/elesiann/cc-cockpit/internal/state"
	"github.com/elesiann/cc-cockpit/internal/workspace"
)

// Version is the binary's reported version. Overridden at release time via:
//
//	go build -ldflags="-X main.Version=<tag>"
var Version = "0.7.0"

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
	case "watch":
		os.Exit(runWatch(args))
	case "reduce":
		os.Exit(runReduce(args))
	case "install", "setup":
		os.Exit(runInstall(args))
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
  init                create optional .cc-cockpit/workspace.json labels
  doctor              check install + optional workspace health
  watch               headless dashboard: aggregate every workspace's sessions in any terminal
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
		if hooksOK, err := install.HooksInstalled(settingsRaw); err != nil {
			failFix("cc-cockpit install (rewrites the hooks block)", "Claude settings invalid: %v", err)
		} else if hooksOK {
			ok("Claude hooks installed and executable")
		} else {
			failFix("cc-cockpit install", "Claude hooks missing, stale, or pointing at a non-executable cc-cockpit")
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

	if issues == 0 {
		fmt.Println("doctor: all checks passed")
		return 0
	}
	fmt.Printf("doctor: %d issue(s) found\n", issues)
	return 1
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
func collectEndTargets(predicate func(string, *state.Session) bool) []endTarget {
	root := dashboard.DefaultStateRoot(homeDir(), os.Getenv)
	matches, _ := filepath.Glob(filepath.Join(root, "*", "events.jsonl"))
	var dirs []string
	for _, m := range matches {
		dirs = append(dirs, filepath.Dir(m))
	}
	var targets []endTarget
	for _, sh := range dirs {
		f, err := os.Open(filepath.Join(sh, "events.jsonl"))
		if err != nil {
			continue
		}
		st := state.Reduce(f)
		_ = f.Close()
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

	predicate := func(sid string, _ *state.Session) bool {
		return prefix == "all-non-ended" || strings.HasPrefix(sid, prefix)
	}
	targets := collectEndTargets(predicate)

	if len(targets) == 0 {
		fmt.Printf("end: no matching non-ended sessions for %q across any workspace\n", prefix)
		return 0
	}

	if len(targets) > 1 && !yes {
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

// ---------- watch ----------

// runWatch renders an aggregate of every workspace under the cc-cockpit state
// root in the current terminal. Read-only; does not launch or manage Claude
// processes. Exits on SIGINT/SIGTERM.
func runWatch(args []string) int {
	if len(args) > 0 {
		die("watch", "unexpected arguments: %v", args)
	}
	root := dashboard.DefaultStateRoot(homeDir(), os.Getenv)
	src := dashboard.AggregateSource{StateRoot: root}
	if err := dashboard.Run(src); err != nil {
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

	if _, err := exec.LookPath("claude"); err != nil {
		fmt.Fprintln(os.Stderr, "install: warning: claude not found on PATH")
	}
	fmt.Println("install: done")
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
