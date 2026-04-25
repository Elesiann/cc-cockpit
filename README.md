# cc-cockpit

**A workspace supervisor for running N Claude Code sessions across M independent repos — without forcing them into a single git tree.**

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
![Status](https://img.shields.io/badge/status-v0.1.0--mvp-orange)

---

## Why this exists

Claude Code made it cheap to spin up many agents in parallel. The bottleneck moved from **compute** to **attention** — N sessions running simultaneously demand N contexts in an operator's head. Existing agent orchestrators answer this, but they all make the same assumption:

> *"Your N agents work on the same codebase."*

Monorepo tools, devcontainer runners, IDE multi-agent plugins, Git-worktree orchestrators — they all expect one repository. The workspace **is** the repo. Launching an agent in an unrelated sibling repo means a second instance, a second config, a second mental model.

**That's not how real work is structured.** A feature lives across `customer-portal`, `instance-manager`, and `supabase-bootstrap` — three independently-versioned repos with their own CI, their own remote, their own release cadence. A "workspace" is a **cognitive unit**, not a git unit.

cc-cockpit treats the workspace that way:

- The workspace parent is just a **directory**. Not a super-repo. Not a monorepo. Not a git worktree.
- Each child is a first-class git repo — its own `.git/`, its own remote, its own history.
- The cockpit observes sessions across all of them, in one attention loop, with one bell.

This is the differentiator. **Agent orchestrators locked to one git repo are legion. One that refuses to lock you in is — as far as we can tell — zero.**

---

## What it does

- **`cc-cockpit install`** — installs the `cc-cockpit` command on `PATH` and wires the Claude Code hooks.
- **`cc-cockpit init`** — creates `.cc-cockpit/workspace.json` from the child git repos in a workspace directory.
- **`cc-cockpit open`** — opens the cockpit for that workspace.
- **`cc-cockpit start api fix auth bug`** — opens a new pane running Claude Code inside the `api` child repo, with the right env wired in.
- **Dashboard** — auto-updates every 0.5s, shows status (`running` / `waiting_input` / `idle` / `ended`) for every session, rings the terminal bell the moment any agent enters `waiting_input`.

All of this is bash + jq + Zellij. A few hundred lines of shell, no daemon, no background process, no database — just an event-sourced append log per workspace and a deterministic reducer.

---

## Screenshot

```
┌─── cc-cockpit dashboard ───────────┬─── control ────────────────────────┐
│  cc-cockpit · veecode-saas         │ $ cc-cockpit start api fix bug     │
│  active=3 (running=2 waiting=1)    │ $                                  │
│                                    │ $                                  │
│  STATUS         SID       REPO     │                                    │
│  ▶ running      a7b2...   api      │                                   │
│  ● waiting_in   3f1c...   portal   │                                    │
│  ◯ idle         c90e...   infra    │                                   │
└────────────────────────────────────┴────────────────────────────────────┘
```

(Actual renders include task labels, activity timers, and an `ended` footer.)

---

## Mental model

Three planes you always have in your head:

```
┌─────────── Execution plane ─────────────────┐
│ N Claude Code sessions, each in its own     │
│ Zellij pane, cwd = a specific child repo.   │
└─────────────────────────────────────────────┘

┌─── Control plane ───┐    ┌─── Observation plane ───┐
│ cc-cockpit CLI      │    │ dashboard pane          │
│   open / start /    │    │ (read-only view of      │
│   mark-ended / hook │    │  sessions + bell on     │
│                     │    │  waiting_input)         │
└─────────────────────┘    └─────────────────────────┘
```

**Non-negotiable principle**: the cockpit **observes** agents — it never orchestrates them. The operator is always in the loop. This is a deliberate choice, not a limitation.

**Key concepts:**

| Concept | Where it lives |
|---|---|
| Workspace parent | Any directory with `.cc-cockpit/workspace.json` (doesn't need to be a git repo) |
| Child repo | An independent git repo inside the workspace parent. Sessions only run inside a child, never the parent. |
| Source tree (scripts) | Wherever you cloned this repo. The `cc-cockpit` binary on your `PATH` symlinks into `.cc-cockpit/bin/`. |
| Runtime state | `$XDG_STATE_HOME/cc-cockpit/<workspace-name>/` — `events.jsonl`, `current.json`, `seq.counter`, `events.lock` |
| Hooks | Global `~/.claude/settings.json` — absolute path to the binary; silent no-op unless `COCKPIT_SESSION_ACTIVE=1` is in the env |

---

## Prerequisites

- Linux / WSL2. macOS has not been tested.
- [`zellij`](https://zellij.dev/) 0.44+ on `PATH`.
- `jq` 1.7+.
- Claude Code 2.1+ with the hook event model (SessionStart, PostToolUse, Stop, SessionEnd, Notification, PermissionRequest, UserPromptSubmit).
- Bash 5+.

---

## Install

```bash
# clone the cockpit source wherever you like
git clone https://github.com/<you>/cc-cockpit.git ~/cc-cockpit
cd ~/cc-cockpit

# install the cc-cockpit command and Claude Code hooks
./install
```

No manual hook merge is needed. `./install` creates `~/.local/bin/cc-cockpit`, backs up `~/.claude/settings.json` if it must change, and installs the hook entries idempotently.

The `Notification` hook uses `matcher: "idle_prompt|permission_prompt"` — this is the real signal that Claude is waiting on you (validated empirically against real Claude Code payloads before the hook was wired).

---

## Create a workspace

```bash
# Put, symlink, or clone the real git repos below a workspace parent.
# The parent directory does NOT need to be a git repo itself.
mkdir -p ~/my-workspace
cd ~/my-workspace

# Initialize the workspace config.
cc-cockpit init
```

Then open the cockpit:

```bash
cc-cockpit open
```

For an explicit layout, initialize once before opening:

```bash
cc-cockpit init --name my-workspace api=packages/api web=packages/web infra=infra
cc-cockpit open
```

**Rules about `workspace.json`:**

- `name` is the runtime state dir key. It must match `^[a-zA-Z0-9][a-zA-Z0-9._-]*$` (no slashes, no `..`). Pick something stable — renaming it orphans the previous state.
- If two different workspace directories declare the same `name`, the second `cc-cockpit open` fails with a clear error. State binds to the **canonical path** of the workspace root on first open and rejects mismatches on subsequent opens. No silent cross-workspace contamination.
- Keys in `repos` are **short labels** you type in `--repo` (`api`, `web`, ...), not filesystem paths.
- Values are **relative paths** from the workspace parent to each child repo. Paths that resolve outside the workspace root (e.g. `../sibling`) are rejected at `spawn` time.
- Children must be **real git repos** — `spawn` verifies `git -C <child> rev-parse --git-dir` and refuses to start Claude anywhere else.
- You can have multiple workspaces with different `name`s; they don't conflict.

---

## Daily workflow

```bash
# enter the workspace parent, or any dir inside it, and open the cockpit
cd ~/my-workspace
cc-cockpit open
```

This walks up until it finds `.cc-cockpit/workspace.json`, initializes state, and launches Zellij with two panes side-by-side:

```
┌─── dashboard (60 cols) ───┬─── control (bash shell) ───┐
│ STATUS  SID  REPO  TASK   │ $                          │
│ ▶ ...                     │                            │
└───────────────────────────┴────────────────────────────┘
```

**Start a session** (from the control pane):
```bash
cc-cockpit start api fix auth bug
```

A Claude pane opens in a bottom row below the dashboard/control pair. Additional spawned sessions share that bottom row. Dashboard updates in ≤1s.

**Observe:** keep an eye on `STATUS`.

- `▶ running` — Claude is working (assistant turn in progress, or tool executing).
- `● waiting_input` — Claude is parked waiting on you. Terminal bell fires on transition into this state (once per entry; re-armed after it exits).
- `◯ idle` — Last turn ended, no activity, no pending input.
- `◼ ended` — Session closed (either via `/quit` or dismissed with `mark-ended`).

**End a session:** `/quit` in the Claude pane. Dashboard moves it to the `ended` footer.

**Dismiss a stale session** (e.g. after a crash where `SessionEnd` never fired):
```bash
cc-cockpit mark-ended <sid-prefix>
# matches >1 session? --yes required:
cc-cockpit mark-ended all-non-ended --yes
```
Dismissal is non-terminal: if the matched session turns out to still be live, the next event it emits will un-dismiss it and it reappears in the active table.

---

## Command cheat sheet

| Command | Use |
|---|---|
| `cc-cockpit install` | Install the command on `PATH` and install Claude Code hooks. Usually run via `./install` from the cloned source tree. |
| `cc-cockpit init [--name NAME] [repo=path ...]` | Create `.cc-cockpit/workspace.json`; with no repo specs, auto-discovers child git repos. |
| `cc-cockpit open` | Open the cockpit for the workspace containing your cwd. |
| `cc-cockpit start <repo> <task...>` | Open a new Zellij pane running Claude in `repos[<repo>]`. Run from inside the Zellij (control pane). |
| `cc-cockpit spawn <repo> <task...>` | Compatibility alias for `start`; the old `spawn --repo <key> --task "<text>"` form still works. |
| `cc-cockpit mark-ended <sid-prefix> [--yes]` | Append a synthetic `SessionEnd` for stale sessions. The dismissal is **non-terminal**: if the session was actually still live, any later event from it (prompt, tool use, notification) un-dismisses it. Prefixes that match more than one session require `--yes`. |
| `cc-cockpit mark-ended all-non-ended --yes` | Dismiss every currently non-ended session (e.g. after a full Zellij restart). `--yes` required because this always matches multiple sessions. |
| `cc-cockpit hook <Event>` | **Internal.** Called from `~/.claude/settings.json`. Do not invoke by hand. |
| `cc-cockpit --version` | Print version. |
| `cc-cockpit --help` | Short usage. |

**Zellij keybindings you actually need** (Default mode):

| Keys | Does |
|---|---|
| `Ctrl+p` then `←↑↓→` (or `hjkl`), then `Esc` | Move focus between panes |
| `Ctrl+p` then `x` | Close focused pane |
| `Ctrl+o` then `d` | **Detach** Zellij (state survives, re-attach with `zellij attach`) |
| `Ctrl+q` | Quit Zellij entirely (kills all panes — use after `mark-ended`) |

---

## How it works (30-second version)

1. When you `start`, `cc-cockpit` invokes `zellij action new-pane ... env COCKPIT_SESSION_ACTIVE=1 ... claude`.
2. The `SessionStart` hook in `~/.claude/settings.json` fires. It's a silent no-op unless `COCKPIT_SESSION_ACTIVE=1` — so global hooks don't pollute non-cockpit Claude sessions.
3. Every event (`SessionStart`, `UserPromptSubmit`, `PermissionRequest`, `Notification`, `PostToolUse`, `Stop`, `SessionEnd`) appends an envelope to `events.jsonl` under an exclusive `flock`. Sequence numbers are monotonic; wall-clock is advisory.
4. The dashboard pane loops every 0.5s: snapshots the log under a shared `flock`, runs a pure-jq reducer to produce `current.json`, renders, and rings the bell on new `waiting_input` entrants.
5. There is no daemon. No IPC. No database. If the dashboard dies, `events.jsonl` keeps growing; on restart, the reducer reconstructs everything.

Everything else is consequences of those five points.

**Ordering assumption** — within a single Claude Code session, hooks fire serially: the `claude` process waits for each hook to exit before proceeding to the next one. So for any given `session_id`, `seq` order matches emission order. Across sessions the flock serializes appends but the reducer operates on disjoint per-session state, so cross-session races don't matter. If Claude Code ever introduces concurrent hooks within one session, the reducer would need a source-side ordering field in the payload.

---

## Nuances & troubleshooting

**"Not in a cc-cockpit workspace"** — you're outside any dir whose ancestors contain `.cc-cockpit/workspace.json`. `cd` into one, or run `cc-cockpit init` from the workspace parent first.

**"init: no child git repos found"** — you're outside a workspace parent, or the repos have not been cloned/symlinked yet. `cd` to the directory that contains the child repos, or run `cc-cockpit init --name <name> label=path`.

**"start: must be run inside a Zellij session"** — `cc-cockpit start` only works from a pane opened by the cockpit (needs `$ZELLIJ` in env). Run `cc-cockpit open` first.

**"start: unknown repo 'X'"** — the label isn't in `.repos`. The error lists valid labels.

**Dashboard shows ghost sessions from a previous Zellij** — runtime state persists across Zellij restarts (by design — it's event-sourced). Use `cc-cockpit mark-ended <prefix>` to dismiss.

**Bell didn't fire even though I saw Claude asking for permission** — in `permission_mode: "auto"`, Claude auto-approves and the `Notification` fires transiently. The **visible status** may never enter `waiting_input` because the reducer collapses `Notification → PostToolUse` inside one 0.5s tick. The **bell still fires**, because it's driven by new event sequence numbers (any new `Notification`/`PermissionRequest` event), not by the reduced state. To sustain `waiting_input` in the dashboard, press `Shift+Tab` in the Claude pane to cycle out of auto mode.

**Terminal header of Claude pane looks weird** — the spawn command is `env COCKPIT_... claude`, and the terminal title shows the argv. Pane name (in the Zellij border) IS set correctly via `--name "<repo>: <task>"`; the header text inside Claude's own UI is a claude-side concern.

**Dashboard isn't updating or seems frozen** — the dashboard only repaints when the frame actually changes. If nothing is happening, it's fine. Sanity check: `ls -la ~/.local/state/cc-cockpit/<workspace>/current.json` (mtime should advance every ~0.5s).

**Everything broken, reset hard:**
```bash
rm -rf ~/.local/state/cc-cockpit/<workspace-name>/
# next `cc-cockpit open` starts clean
```

**Inspect raw events:**
```bash
jq -c . ~/.local/state/cc-cockpit/<workspace-name>/events.jsonl | less -R
```

**Dashboard shows `⚠ dropped=N` in the header** — the reducer skipped N malformed lines in `events.jsonl` (truncated writes, disk-full mid-append, manual edits). The rest of the log was processed normally. To investigate, grep for non-JSON lines:
```bash
while IFS= read -r line; do echo "$line" | jq . >/dev/null 2>&1 || echo "BAD: $line"; done \
  < ~/.local/state/cc-cockpit/<workspace-name>/events.jsonl
```

---

## What's intentionally NOT here (v1 non-goals)

These will not work today; don't look for them:

- Killing or jumping to panes from inside the dashboard (needs a Rust Zellij plugin).
- `retask` (renaming a session's task label in-place). On the roadmap.
- Automatic repo discovery (PreToolUse classifier, RepoDiscovered events) — on the roadmap.
- Stale-session auto-cleanup. Use `mark-ended` for now.
- Tracking `cwd` changes mid-session (Claude's `CwdChanged` hook didn't fire in M0 validation).
- Desktop notifications. The terminal bell is the only audible signal.
- Clone/bootstrap of child repos from the workspace.json.
- Multiple Zellij sessions running the same workspace concurrently.

Most are on the v1.1/v1.5 roadmap.

---

## Source layout

```
<this repo>/
├── README.md
├── LICENSE
├── install                          ← one-time installer wrapper
└── .cc-cockpit/
    ├── bin/cc-cockpit                 ← the binary (bash, single file)
    ├── reduce-state.sh                ← pure-jq reducer, events.jsonl → current.json
    ├── render.sh                      ← current.json → terminal frame
    ├── dashboard.sh                   ← render loop + bell + alt-screen flicker-free
    ├── layouts/initial.kdl            ← Zellij layout (dashboard | control over spawned panes)
    └── examples/
        ├── workspace.example.json     ← minimal workspace config
        └── settings.snippet.json      ← hook registration for ~/.claude/settings.json

$XDG_STATE_HOME/cc-cockpit/<name>/     ← runtime state (never committed)
├── events.jsonl                       ← append-only event log
├── events.snapshot.jsonl              ← dashboard-local copy (under flock -s)
├── current.json                       ← reducer output (+ dropped_events count)
├── seq.counter                        ← global monotonic counter (under flock -x)
├── events.lock                        ← flock target
├── last_bell_seq                      ← dashboard's bell checkpoint (event-delta)
└── workspace_root                     ← canonical workspace path this state binds to
```

---

## One-screen summary

```
install once  →  ./install
workspace once  →  cc-cockpit init
daily  →  cc-cockpit open  →  Zellij opens (dashboard | control)
         ↓
control pane  →  cc-cockpit start X do the thing  →  new claude pane appears
         ↓
dashboard auto-updates, bell on waiting_input
         ↓
/quit each claude  or  cc-cockpit mark-ended all-non-ended
         ↓
Ctrl+q to close Zellij
```

---

## Contributing

Early-stage, single-author project. If you try it and it breaks, open an issue with the event log (`events.jsonl`) attached. If you want a feature listed in the non-goals section above, PRs welcome — open a discussion issue first so we can align on scope.

---

## License

[MIT](LICENSE) © 2026 Giovani Corrêa
