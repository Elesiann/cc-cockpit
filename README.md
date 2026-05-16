# cc-cockpit

**An attention layer for parallel coding agents. One screen to watch every session you've started — across any repos, with any agent tool inside.**

[![CI](https://github.com/Elesiann/cc-cockpit/actions/workflows/ci.yml/badge.svg)](https://github.com/Elesiann/cc-cockpit/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Elesiann/cc-cockpit)](https://github.com/Elesiann/cc-cockpit/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

---

## Quickstart

```bash
go install github.com/elesiann/cc-cockpit/cmd/cc-cockpit@latest
cd ~/wherever-your-repos-are
cc-cockpit open
```

`open` discovers child git repos in the current directory, installs the Claude Code hooks on first run, and attaches a tmux session with a live dashboard. From the control pane, spawn a Claude session in a specific repo:

```bash
cc-cockpit start api fix auth bug
```

That's it — no separate `install`, `init`, or `doctor` step needed. The three commands still exist for scripts and custom setups.

> [!NOTE]
> On first run, `cc-cockpit open` edits `~/.claude/settings.json` to register the Claude Code hooks. The original file is backed up as `settings.json.bak-<timestamp>` if it would change.

---

## Why this exists

Claude Code made it cheap to start many agents at once. The bottleneck moved from **compute** to **attention** — many sessions running at the same time demand many contexts in your head.

Most tools answer this at the **agent layer**: they dispatch agents, isolate file edits with git worktrees, manage branches, even open pull requests. That's the layer where files get written.

cc-cockpit lives one layer above. It does not dispatch agents. It does not isolate files. It does not touch your repos. It just watches every terminal, builds a live status table, and rings the bell when one of them needs you. **Whatever runs inside each pane is your choice** — vanilla `claude`, a managed-agent fleet, a worktree orchestrator, or even a non-Claude tool that emits the same hook events.

The result: **one screen to watch them all**, no matter how many repos they span or which tools you use inside them.

### Principles

- **Observation, not orchestration.** cc-cockpit reads hook events; it never dispatches and never writes code.
- **Your repos stay untouched.** Sessions run in your real working directories. No `.claude/worktrees/`, no auto-branches, nothing to clean up.
- **No daemon.** Just tmux, `flock`, and an append-only event log per workspace.
- **Composable.** Run any agent tool inside the panes cc-cockpit watches. cc-cockpit only cares that hook events show up in the log.

cc-cockpit is the missing **attention layer** between you and N parallel terminals. Nothing more, nothing less.

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

## Prerequisites

- Linux, macOS, or WSL2.
- [`tmux`](https://github.com/tmux/tmux) 3.0+ on `PATH`.
- Claude Code 2.1+ with the hook event model (SessionStart, PostToolUse, Stop, SessionEnd, Notification, PermissionRequest, UserPromptSubmit).
- Go 1.25+ if building from source.

---

## Install

There are two ways to get the binary onto your `PATH`. After either one, run `cc-cockpit install` to register the Claude Code hooks.

```bash
# Option A — build from source (recommended for now):
go install github.com/elesiann/cc-cockpit/cmd/cc-cockpit@latest

# Option B — download a release binary (linux/amd64 — see Releases page for arm64):
curl -L https://github.com/elesiann/cc-cockpit/releases/download/v0.3.0/cc-cockpit_0.3.0_linux_amd64.tar.gz \
  | tar -xz -C ~/.local/bin/ cc-cockpit

# Then, regardless of how you got the binary:
cc-cockpit install
```

`cc-cockpit install` symlinks the binary into `~/.local/bin/` (idempotent), backs up `~/.claude/settings.json` if it would change, and merges the cc-cockpit hook entries. Re-run any time without fear of duplicates.

Useful flags: `--bin-dir DIR`, `--settings FILE`, `--no-bin`, `--no-hooks`. If `cc-cockpit` is still not found afterwards, make sure `~/.local/bin` is on your shell `PATH`.

The `Notification` hook uses `matcher: "idle_prompt|permission_prompt"` — this is the real signal that Claude is waiting on you (validated empirically against real Claude Code payloads before the hook was wired).

---

## Workspaces

A workspace is just a directory with `.cc-cockpit/workspace.json`. `cc-cockpit open` creates this automatically when missing, discovering child git repos at depths 1–3 and registering each as a session target.

For explicit setup (custom labels, scripted bootstrap, no-discovery), use `cc-cockpit init`:

```bash
cc-cockpit init --name my-workspace api=packages/api web=packages/web infra=infra
```

The `name` field is the runtime state directory key (must match `^[a-zA-Z0-9][a-zA-Z0-9._-]*$`). State binds to the canonical path of the workspace root on first open and rejects mismatches on subsequent opens — no silent cross-workspace contamination.

---

## Inside the cockpit

```
┌─── dashboard (60 cols) ───┬─── control (bash shell) ───┐
│ STATUS  SID  REPO  TASK   │ $                          │
│ ▶ ...                     │                            │
└───────────────────────────┴────────────────────────────┘
```

The dashboard is read-only and auto-updates every 0.5s. The control pane is a regular shell — that's where you run `cc-cockpit start <repo> <task>` to spawn a new Claude session in its own tmux window named `<repo>: <task>`.

cc-cockpit uses its own private tmux server (`-L cc-cockpit`), so it never collides with any tmux config you already have. Switch between Claude windows with `Ctrl-b n` / `p` / `<number>`; detach with `Ctrl-b d` (sessions persist — `cc-cockpit open` reattaches).

**Statuses:**
- `▶ running` — Claude is working (assistant turn or tool execution).
- `● waiting_input` — parked waiting on you. Terminal bell fires on entry.
- `◯ idle` — last turn ended; no pending input.
- `◼ ended` — closed via `/quit`, or auto-cleaned after a crash.

**End a session:** `/quit` in the Claude window. If Claude crashes without firing `SessionEnd`, the per-session `tmux pane-exited` hook auto-emits a synthetic one — no manual cleanup. For the rare case where neither fires:

```bash
cc-cockpit mark-ended <sid-prefix>           # one session
cc-cockpit mark-ended all-non-ended --yes    # all of them
```

Dismissal is not final. If the matched session was actually still live, its next event brings it back into the active table.

---

## Command cheat sheet

| Command | Use |
|---|---|
| `cc-cockpit install [--bin-dir DIR] [--settings FILE] [--no-bin] [--no-hooks]` | Symlink the binary onto `PATH` and merge Claude Code hooks. Idempotent; backs up existing settings. |
| `cc-cockpit init [--name NAME] [repo=path ...]` | Create `.cc-cockpit/workspace.json`; with no repo specs, auto-discovers child git repos. |
| `cc-cockpit doctor` | Check prerequisites, PATH, hooks, workspace config, and child repos. |
| `cc-cockpit open` | Open the cockpit for the workspace containing your cwd. |
| `cc-cockpit start <repo> <task...>` | Open a new tmux window running Claude in `repos[<repo>]`. Run from inside the cockpit's control pane. |
| `cc-cockpit mark-ended <sid-prefix> [--yes]` | Append a synthetic `SessionEnd` for stale sessions. The dismissal is **not final**: if the session was actually still live, any later event from it (prompt, tool use, notification) brings it back. Prefixes that match more than one session require `--yes`. |
| `cc-cockpit mark-ended all-non-ended --yes` | Dismiss every currently non-ended session. `--yes` required because this always matches multiple sessions. |
| `cc-cockpit reduce` | (debug) Read `events.jsonl` on stdin, print the reduced state as JSON. Useful for inspecting how the reducer interprets a log. |
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
2. The `SessionStart` hook in `~/.claude/settings.json` fires. It is a silent no-op unless `COCKPIT_SESSION_ACTIVE=1`, so cc-cockpit's hooks do not affect Claude sessions started outside the cockpit.
3. Every event (`SessionStart`, `UserPromptSubmit`, `PermissionRequest`, `Notification`, `PostToolUse`, `Stop`, `SessionEnd`) appends an envelope to `events.jsonl` under an exclusive `flock`. Sequence numbers are monotonic; wall-clock is advisory.
4. The dashboard pane loops every 0.5s. It snapshots the log under a shared `flock`, runs the reducer in-process to produce `current.json`, renders the frame, and rings the bell on new `waiting_input` sessions.
5. There is no daemon. No IPC. No database. If the dashboard dies, `events.jsonl` keeps growing. On restart, the reducer rebuilds the state from the log.

Everything else is consequences of those five points.

**Ordering assumption** — within a single Claude Code session, hooks fire serially: the `claude` process waits for each hook to exit before proceeding to the next one. So for any given `session_id`, `seq` order matches emission order. Across sessions the flock serializes appends but the reducer operates on disjoint per-session state, so cross-session races don't matter. If Claude Code ever introduces concurrent hooks within one session, the reducer would need a source-side ordering field in the payload.

---

## Nuances & troubleshooting

**"Not in a cc-cockpit workspace"** — you're outside any dir whose ancestors contain `.cc-cockpit/workspace.json`. `cd` into one, or run `cc-cockpit init` from the workspace parent first.

**"init: no child git repos found"** — you're outside a workspace parent, or the repos have not been cloned/symlinked yet. `cd` to the directory that contains the child repos, or run `cc-cockpit init --name <name> label=path`.

**"start: must be run inside the cockpit"** — `cc-cockpit start` only works from a pane opened by the cockpit (needs `$COCKPIT_STATE_HOME` in env, set by the tmux session). Run `cc-cockpit open` first.

**"start: unknown repo 'X'"** — the label isn't in `.repos`. The error lists valid labels.

**Dashboard shows ghost sessions from a previous run** — runtime state persists across detaches and restarts (by design — it's event-sourced). The pane-exited hook handles the common crashed-Claude case automatically; for stranger cases use `cc-cockpit mark-ended <prefix>`.

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

**Dashboard shows `⚠ dropped=N` in the header** — the reducer skipped N malformed lines in `events.jsonl` (truncated writes, disk-full mid-append, manual edits). The rest of the log was processed normally. Reducer output for inspection: `cc-cockpit reduce < ~/.local/state/cc-cockpit/<workspace-name>/events.jsonl`.

---

## Contributing

Early-stage, single-author project. If you try it and it breaks, open an issue with the event log (`events.jsonl`) attached. PRs welcome — open a discussion issue first so we can align on scope.

---

## License

[MIT](LICENSE) © 2026 Giovani Corrêa
