# cc-cockpit

**A workspace supervisor for running N Claude Code sessions across M independent repos — without forcing them into a single git tree.**

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
![Status](https://img.shields.io/badge/status-v0.3.0-orange)

---

## Quickstart

```bash
# one time: build & install the binary plus the Claude Code hooks
go install github.com/elesiann/cc-cockpit/cmd/cc-cockpit@latest
cc-cockpit install

# one time, from the directory that contains your child git repos
cd ~/my-workspace
cc-cockpit init
cc-cockpit doctor

# daily
cc-cockpit open
```

Inside the cockpit control pane:

```bash
cc-cockpit start api fix auth bug
```

Command words are literal: `install` sets up your machine, `init` creates workspace config, `open` opens the cockpit, and `start` starts a session.

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

All of this is a single Go binary driving tmux on its own private server. No daemon, no background process, no database — just an event-sourced append log per workspace and a deterministic reducer.

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
│ tmux window, cwd = a specific child repo.   │
└─────────────────────────────────────────────┘

┌─── Control plane ───┐    ┌─── Observation plane ───┐
│ cc-cockpit CLI      │    │ dashboard pane          │
│   open / start /    │    │ (read-only view of      │
│   doctor / mark-end │    │  sessions + bell on     │
│                     │    │  waiting_input)         │
└─────────────────────┘    └─────────────────────────┘
```

**Non-negotiable principle**: the cockpit **observes** agents — it never orchestrates them. The operator is always in the loop. This is a deliberate choice, not a limitation.

**Key concepts:**

| Concept | Where it lives |
|---|---|
| Workspace parent | Any directory with `.cc-cockpit/workspace.json` (doesn't need to be a git repo) |
| Child repo | An independent git repo inside the workspace parent. Sessions only run inside a child, never the parent. |
| Binary | A single static Go binary on `PATH` (typically `~/.local/bin/cc-cockpit`) — no sibling files needed |
| Runtime state | `$XDG_STATE_HOME/cc-cockpit/<workspace-name>/` — `events.jsonl`, `current.json`, `events.lock` |
| Hooks | Global `~/.claude/settings.json` — absolute path to the binary; silent no-op unless `COCKPIT_SESSION_ACTIVE=1` is in the env |

---

## Prerequisites

- Linux, macOS, or WSL2.
- [`tmux`](https://github.com/tmux/tmux) 3.0+ on `PATH`.
- Claude Code 2.1+ with the hook event model (SessionStart, PostToolUse, Stop, SessionEnd, Notification, PermissionRequest, UserPromptSubmit).
- Go 1.22+ if building from source.

---

## Install

There are two ways to get the binary onto your `PATH`. After either one, run `cc-cockpit install` to register the Claude Code hooks.

```bash
# Option A — build from source (recommended for now):
go install github.com/elesiann/cc-cockpit/cmd/cc-cockpit@latest

# Option B — download a release binary (when releases are published):
curl -L https://github.com/elesiann/cc-cockpit/releases/latest/download/cc-cockpit-linux-amd64 \
  -o ~/.local/bin/cc-cockpit
chmod +x ~/.local/bin/cc-cockpit

# Then, regardless of how you got the binary:
cc-cockpit install
```

`cc-cockpit install` symlinks the binary into `~/.local/bin/` (idempotent), backs up `~/.claude/settings.json` if it would change, and merges the cc-cockpit hook entries. Re-run any time without fear of duplicates.

Useful flags: `--bin-dir DIR`, `--settings FILE`, `--no-bin`, `--no-hooks`. If `cc-cockpit` is still not found afterwards, make sure `~/.local/bin` is on your shell `PATH`.

The `Notification` hook uses `matcher: "idle_prompt|permission_prompt"` — this is the real signal that Claude is waiting on you (validated empirically against real Claude Code payloads before the hook was wired).

---

## Create a workspace

```bash
# Put, symlink, or clone the real git repos below a workspace parent.
# The parent directory does NOT need to be a git repo itself.
mkdir -p ~/my-workspace
cd ~/my-workspace

# Example shape before init:
#   ~/my-workspace/api/.git
#   ~/my-workspace/web/.git
#   ~/my-workspace/infra/.git
#
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
- Keys in `repos` are **short labels** you type in `cc-cockpit start <repo> ...` (`api`, `web`, ...), not filesystem paths.
- Values are **relative paths** from the workspace parent to each child repo. Paths that resolve outside the workspace root (e.g. `../sibling`) are rejected at `start` time.
- Children must be **real git repos** — `start` verifies `git -C <child> rev-parse --git-dir` and refuses to start Claude anywhere else.
- You can have multiple workspaces with different `name`s; they don't conflict.

---

## Daily workflow

```bash
# enter the workspace parent, or any dir inside it, and open the cockpit
cd ~/my-workspace
cc-cockpit open
```

This walks up until it finds `.cc-cockpit/workspace.json`, initializes state, and attaches to a tmux session (creating it on first open) with two panes side-by-side in window 0:

```
┌─── dashboard (60 cols) ───┬─── control (bash shell) ───┐
│ STATUS  SID  REPO  TASK   │ $                          │
│ ▶ ...                     │                            │
└───────────────────────────┴────────────────────────────┘
```

cc-cockpit runs on its own private tmux server (`-L cc-cockpit`), so it doesn't collide with whatever tmux you already use.

**Start a session** (from the control pane):
```bash
cc-cockpit start api fix auth bug
```

A Claude session opens in its own tmux window named `<repo>: <task>`. Switch between them with `Ctrl-b n`/`Ctrl-b p` or `Ctrl-b <number>`. Dashboard updates in ≤1s and stays visible in window 0.

**Observe:** keep an eye on `STATUS`.

- `▶ running` — Claude is working (assistant turn in progress, or tool executing).
- `● waiting_input` — Claude is parked waiting on you. Terminal bell fires on transition into this state (once per entry; re-armed after it exits).
- `◯ idle` — Last turn ended, no activity, no pending input.
- `◼ ended` — Session closed (either via `/quit` or dismissed with `mark-ended`).

**End a session:** `/quit` in the Claude window. Dashboard moves it to the `ended` footer. If Claude crashes without firing its own `SessionEnd`, tmux's `pane-died` hook auto-emits a synthetic one and the dashboard updates within a tick — no manual cleanup needed. (cc-cockpit installs the hook with `set-hook -g`, so it only affects its own private `-L cc-cockpit` server, never your normal tmux config.)

**Dismiss a stale session** (rare; only useful if both the pane-died hook and the natural SessionEnd are missed):
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
| `cc-cockpit install [--bin-dir DIR] [--settings FILE] [--no-bin] [--no-hooks]` | Symlink the binary onto `PATH` and merge Claude Code hooks. Idempotent; backs up existing settings. |
| `cc-cockpit init [--name NAME] [repo=path ...]` | Create `.cc-cockpit/workspace.json`; with no repo specs, auto-discovers child git repos. |
| `cc-cockpit doctor` | Check prerequisites, PATH, hooks, workspace config, and child repos. |
| `cc-cockpit open` | Open the cockpit for the workspace containing your cwd. |
| `cc-cockpit start <repo> <task...>` | Open a new tmux window running Claude in `repos[<repo>]`. Run from inside the cockpit's control pane. |
| `cc-cockpit mark-ended <sid-prefix> [--yes]` | Append a synthetic `SessionEnd` for stale sessions. The dismissal is **non-terminal**: if the session was actually still live, any later event from it (prompt, tool use, notification) un-dismisses it. Prefixes that match more than one session require `--yes`. |
| `cc-cockpit mark-ended all-non-ended --yes` | Dismiss every currently non-ended session. `--yes` required because this always matches multiple sessions. |
| `cc-cockpit --version` | Print version. |
| `cc-cockpit --help` | Short usage. |

**tmux keybindings you actually need** (default `Ctrl-b` prefix):

| Keys | Does |
|---|---|
| `Ctrl-b n` / `Ctrl-b p` | Next / previous window |
| `Ctrl-b <N>` | Jump to window N (0 = dashboard+control) |
| `Ctrl-b o` / `Ctrl-b ←↑↓→` | Move focus between panes inside a window |
| `Ctrl-b &` | Kill focused window (confirm prompt) |
| `Ctrl-b d` | **Detach** (sessions survive; re-attach with `cc-cockpit open` from the same workspace) |

---

## How it works (30-second version)

1. When you `start`, `cc-cockpit` invokes `tmux new-window -e COCKPIT_SESSION_ACTIVE=1 ... claude` on the private `-L cc-cockpit` server.
2. The `SessionStart` hook in `~/.claude/settings.json` fires. It's a silent no-op unless `COCKPIT_SESSION_ACTIVE=1` — so global hooks don't pollute non-cockpit Claude sessions.
3. Every event (`SessionStart`, `UserPromptSubmit`, `PermissionRequest`, `Notification`, `PostToolUse`, `Stop`, `SessionEnd`) appends an envelope to `events.jsonl` under an exclusive `flock`. Sequence numbers are monotonic; wall-clock is advisory.
4. The dashboard pane loops every 0.5s: snapshots the log under a shared `flock`, runs the reducer in-process to produce `current.json`, renders, and rings the bell on new `waiting_input` entrants.
5. There is no daemon. No IPC. No database. If the dashboard dies, `events.jsonl` keeps growing; on restart, the reducer reconstructs everything.

Everything else is consequences of those five points.

**Ordering assumption** — within a single Claude Code session, hooks fire serially: the `claude` process waits for each hook to exit before proceeding to the next one. So for any given `session_id`, `seq` order matches emission order. Across sessions the flock serializes appends but the reducer operates on disjoint per-session state, so cross-session races don't matter. If Claude Code ever introduces concurrent hooks within one session, the reducer would need a source-side ordering field in the payload.

---

## Nuances & troubleshooting

**"Not in a cc-cockpit workspace"** — you're outside any dir whose ancestors contain `.cc-cockpit/workspace.json`. `cd` into one, or run `cc-cockpit init` from the workspace parent first.

**"init: no child git repos found"** — you're outside a workspace parent, or the repos have not been cloned/symlinked yet. `cd` to the directory that contains the child repos, or run `cc-cockpit init --name <name> label=path`.

**"start: must be run inside the cockpit"** — `cc-cockpit start` only works from a pane opened by the cockpit (needs `$COCKPIT_STATE_HOME` in env, set by the tmux session). Run `cc-cockpit open` first.

**"start: unknown repo 'X'"** — the label isn't in `.repos`. The error lists valid labels.

**Dashboard shows ghost sessions from a previous run** — runtime state persists across detaches and restarts (by design — it's event-sourced). The pane-died hook handles the common crashed-Claude case automatically; for stranger cases use `cc-cockpit mark-ended <prefix>`.

**Bell didn't fire even though I saw Claude asking for permission** — in `permission_mode: "auto"`, Claude auto-approves and the `Notification` fires transiently. The **visible status** may never enter `waiting_input` because the reducer collapses `Notification → PostToolUse` inside one 0.5s tick. The **bell still fires**, because it's driven by new event sequence numbers (any new `Notification`/`PermissionRequest` event), not by the reduced state. To sustain `waiting_input` in the dashboard, press `Shift+Tab` in the Claude pane to cycle out of auto mode.

**Terminal header of Claude window looks weird** — tmux window names are set correctly via `tmux new-window -n "<repo>: <task>"`; what shows up inside Claude's own UI is a claude-side concern.

**Dashboard isn't updating or seems frozen** — the dashboard only repaints when the frame actually changes. If nothing is happening, it's fine. Sanity check: `ls -la ~/.local/state/cc-cockpit/<workspace>/current.json` (mtime should advance every ~0.5s).

**Everything broken, reset hard:**
```bash
rm -rf ~/.local/state/cc-cockpit/<workspace-name>/
# next `cc-cockpit open` starts clean
```

**Inspect raw events:**
```bash
cat ~/.local/state/cc-cockpit/<workspace-name>/events.jsonl
# (jq -c . is optional, but works; the reducer reads JSONL)
```

**Dashboard shows `⚠ dropped=N` in the header** — the reducer skipped N malformed lines in `events.jsonl` (truncated writes, disk-full mid-append, manual edits). The rest of the log was processed normally. Reducer output for inspection: `cc-cockpit-reduce < ~/.local/state/cc-cockpit/<workspace-name>/events.jsonl`.

---

## What's intentionally NOT here (v1 non-goals)

These will not work today; don't look for them:

- Killing or jumping to panes from inside the dashboard (would need richer tmux integration; for now use tmux's own `Ctrl-b &`/`Ctrl-b <N>`).
- `retask` (renaming a session's task label in-place). On the roadmap.
- Automatic repo discovery (PreToolUse classifier, RepoDiscovered events) — on the roadmap.
- Stale-session auto-cleanup. Use `mark-ended` for now.
- Tracking `cwd` changes mid-session (Claude's `CwdChanged` hook didn't fire in M0 validation).
- Desktop notifications. The terminal bell is the only audible signal.
- Clone/bootstrap of child repos from the workspace.json.
- Multiple cockpits running the same workspace concurrently (live-instance lock prevents it).

Most are on the v1.1/v1.5 roadmap.

---

## Source layout

```
<this repo>/
├── README.md
├── LICENSE
├── go.mod / go.sum
├── cmd/
│   ├── cc-cockpit/                    ← the main binary (single static Go executable)
│   └── cc-cockpit-reduce/             ← reducer-as-binary (events.jsonl → current.json)
├── internal/
│   ├── state/                         ← Event/Session types, reducer, flock-backed Append
│   ├── workspace/                     ← workspace.json parsing, slug, repo discovery
│   ├── hook/                          ← hook event-builder (pure)
│   ├── dashboard/                     ← render loop + bell + frame renderer
│   └── install/                       ← Claude settings.json hook merge
└── test/
    └── smoke.sh                       ← invariant-guarding end-to-end tests

$XDG_STATE_HOME/cc-cockpit/<name>/     ← runtime state (never committed)
├── events.jsonl                       ← append-only event log
├── current.json                       ← reducer output (+ dropped_events count)
├── seq.counter                        ← monotonic seq counter (also recovered from log on demand)
├── events.lock                        ← flock target
├── last_bell_seq                      ← dashboard's bell checkpoint (event-delta)
├── cockpit.live.lock / .pid           ← live-instance lock (one cockpit per workspace)
├── init.lock                          ← name-↔-canonical-root binding lock
└── workspace_root                     ← canonical workspace path this state binds to
```

---

## One-screen summary

```
install once  →  go install … && cc-cockpit install
workspace once  →  cc-cockpit init
daily  →  cc-cockpit open  →  tmux session attaches (dashboard | control)
         ↓
control pane  →  cc-cockpit start X do the thing  →  new tmux window with claude
         ↓
dashboard auto-updates, bell on waiting_input, pane-died auto-cleanup
         ↓
/quit each claude  (or close the window)
         ↓
Ctrl-b d to detach (sessions persist) or close the last window to end
```

---

## Contributing

Early-stage, single-author project. If you try it and it breaks, open an issue with the event log (`events.jsonl`) attached. If you want a feature listed in the non-goals section above, PRs welcome — open a discussion issue first so we can align on scope.

---

## License

[MIT](LICENSE) © 2026 Giovani Corrêa
