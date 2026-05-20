# cc-cockpit

**An attention layer for parallel coding agents. One screen to watch every session you've started — across any repos, with live status, native Claude Code labels/colors, and short recaps of what happened while you were away.**

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
┌─── cc-cockpit dashboard ───────────────┬─── control ────────────────────────┐
│  cc-cockpit · veecode-saas             │ $ start api fix auth bug           │
│  active=3  🔧 1  ⏸️ 1  💤 1  ended=0   │ $                                  │
│                                        │ $                                  │
│  STATUS       SID       REPO     TASK  │                                    │
│  ⏳ processing a7b2...   api      auth  │                                    │
│  ⏸️ waiting   3f1c...   portal   review│                                    │
│  💤 idle      c90e...   infra    ci    │                                    │
│    ↳ recap: Goal: clear migration debt…│                                    │
└────────────────────────────────────────┴────────────────────────────────────┘
```

(Actual renders include full task labels, activity timers, `/rename` names, `/color` row coloring, native recaps when available, and an `ended` footer.)

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

## Watch mode (headless, multi-workspace)

For a glanceable view without booting the cockpit, run:

```bash
cc-cockpit watch
```

`watch` opens a read-only dashboard in the current terminal that aggregates **every** active Claude session into a single table — no tmux, no control pane, no `init`. Sessions are routed by cwd: ones inside a `workspace.json` ancestor land in that workspace's state dir; everything else (interactive `claude` in `/tmp`, ad-hoc shell calls) lands in a synthetic `_global` dir. `watch` scans all of them under `~/.local/state/cc-cockpit/*/events.jsonl` (or `$XDG_STATE_HOME/cc-cockpit/...`) and shows one row per non-ended session, with an extra `WS` column so workspaces are easy to distinguish.

**You don't need to "set up" anything**: with the hooks installed (`cc-cockpit install`), open `cc-cockpit watch` in any terminal, then start a `claude` session in any other terminal — it appears within one tick. Sessions without a `primary_repo` (interactive ones) show the basename of their cwd in the REPO column.

`watch` is meant to reduce interpretation work, not add another telemetry feed:

- **Granular status:** rows distinguish `running`, `thinking`, `processing`, `waiting`, `completed`, and `idle`, with compact glyphs (`🔧`, `🤔`, `⏳`, `⏸️`, `✅`, `💤`) so you can scan the table without reading every row.
- **Native names and colors:** if you use Claude Code's `/rename` inside a session, cc-cockpit uses that name in the TASK column. If you use `/color <name>`, cc-cockpit colors the matching row in both `open` and `watch`. Supported colors: red, green, yellow, blue, magenta/purple, cyan, white, gray/grey.
- **Native recaps:** when Claude Code writes its built-in `away_summary` recap to the session transcript (typically ~3 minutes after the session goes quiet), `watch` shows a single dim `↳ recap:` line only when that session is idle. Busy/processing/waiting sessions omit recaps so the primary state stays visually dominant. Missing recaps are omitted — no placeholder noise. cc-cockpit only reads the transcript; it never invokes another model to generate summaries.
- **Stale flag:** a mid-turn session (`running`, `thinking`, or `processing`) with no events for 15 minutes renders with a trailing `?` (probably crashed mid-turn). Cockpit-spawned sessions already get a synthetic `SessionEnd` from the tmux `pane-exited` hook; this is the headless equivalent.
- **Desktop notifications:** when a session enters `waiting_input`, `watch` also calls `wsl-notify-send.exe` / `notify-send` / `osascript` (whichever resolves on PATH) so you don't miss the bell on Windows Terminal under WSL2. At boot it prints which backend was chosen to stderr.
- **Coexists with `open`:** the two modes share the same event log files. You can run `watch` in one terminal and `open` in another and they'll show the same data.
- **Exit:** `Ctrl-C` restores the terminal cleanly. `watch` writes nothing except the bell baseline next to each workspace's `events.jsonl`.

## Inside the cockpit

```
┌─── dashboard (60 cols) ───┬─── control (bash shell) ───┐
│ STATUS  SID  REPO  TASK   │ $                          │
│ 💤 idle  c90e infra ci     │                            │
│   ↳ recap: Goal: fix auth… │                            │
└───────────────────────────┴────────────────────────────┘
```

The dashboard is read-only and auto-updates every 0.5s. The control pane is a regular shell — that's where you run `start [<repo>] <task>` to spawn a new Claude session as a pane below the dashboard. The `<repo>` arg is optional: if your shell's cwd is inside a known repo, cc-cockpit picks that one. Each pane's top border shows `<repo>: <task>` so you can tell them apart at a glance, and the border foreground recolors with status (green=running, yellow=waiting on you, gray=idle).

Inside Claude Code, use its native display commands normally:

```text
/rename last-mile migration
/color cyan
```

cc-cockpit picks those up automatically. `/rename` replaces the TASK column for that session, and `/color` wraps the corresponding row in the requested ANSI color. These are display-only hints: they do not change the workspace, repo, branch, prompt, or event log semantics.

For multi-agent work in one repo, use `start-fleet <repo> [name...]` from the control pane. It opens a single pane (border: `fleet · <repo>` — or `fleet · <repo>: <name>` if you pass a name) running [Claude Code's Agent View](https://code.claude.com/docs/en/agent-view) TUI scoped to that repo. Dispatch as many background agents as you want from inside the TUI; each one shows up as its own row on the dashboard.

> The control pane spawns with bash aliases for `start`, `start-fleet`, `end`, and `doctor` so you can drop the `cc-cockpit` prefix. Your `~/.bashrc` is sourced first, then the aliases are layered on top. The aliases only exist inside the control pane — other shells on your system are untouched.

cc-cockpit uses its own private tmux server (`-L cc-cockpit`), so it never collides with any tmux config you already have. Mouse is on by default: click a pane to focus, drag a border to resize, scroll to enter copy mode. Detach with `Ctrl-b d` (sessions persist — `cc-cockpit open` reattaches).

Rows can come from three sources: single-agent panes from `cc-cockpit start`, fleet-pane agents from `cc-cockpit start-fleet`, or background sessions dispatched externally (`claude --bg`, `/bg`, `claude agents`) whose dispatch directory is inside this workspace. Agent View rows have no local pane; interact with them via `claude attach <id>` or the fleet pane.

**Statuses:**
- `🔧 running` — Claude is executing a tool or otherwise working.
- `🤔 thinking` — assistant turn is in progress before tool execution.
- `⏳ processing` — Claude is processing the turn after tool activity.
- `⏸️ waiting` — parked waiting on you. Terminal bell fires on entry.
- `✅ completed` — the last turn finished recently.
- `💤 idle` — the session has been quiet for a while.
- `◼ ended` — closed via `/quit`, or auto-cleaned after a crash.
- trailing `?` — a mid-turn session has been quiet for 15m+ and may be stale.

**End a session:** `/quit` in the Claude window. If Claude crashes without firing `SessionEnd`, the per-session `tmux pane-exited` hook auto-emits a synthetic one — no manual cleanup. For the rare case where neither fires:

```bash
cc-cockpit end <sid-prefix>           # one session — marks ended and closes its pane
cc-cockpit end all-non-ended --yes    # all of them
```

The end is not final. If the matched session was actually still live (no real pane to close, e.g. a fleet-dispatched background agent), its next event brings it back into the active table.

---

## Command cheat sheet

| Command | Use |
|---|---|
| `cc-cockpit install [--bin-dir DIR] [--settings FILE] [--no-bin] [--no-hooks]` | Symlink the binary onto `PATH` and merge Claude Code hooks. Idempotent; backs up existing settings. |
| `cc-cockpit init [--name NAME] [repo=path ...]` | Create `.cc-cockpit/workspace.json`; with no repo specs, auto-discovers child git repos. |
| `cc-cockpit doctor` | Check prerequisites, PATH, hooks, workspace config, and child repos. |
| `cc-cockpit open` | Open the cockpit for the workspace containing your cwd. |
| `cc-cockpit watch` | Headless dashboard: aggregate every workspace's sessions in the current terminal (no tmux, no setup). Shows `/rename`, `/color`, and native Claude Code recaps when available. Ctrl-C to exit. |
| `cc-cockpit close [<workspace>] [--yes]` | Kill the workspace's tmux session (closes the cockpit). Discovers the workspace from `$COCKPIT_WORKSPACE_NAME` if run from inside, else walks up cwd. Requires `--yes` to confirm the live sessions about to die. |
| `cc-cockpit close --all [--yes]` | Kill the entire cc-cockpit tmux server (every workspace at once). |
| `cc-cockpit start [<repo>] <task...>` | Open a new pane below the dashboard, running Claude in `repos[<repo>]`. Run from inside the cockpit's control pane. The `<repo>` arg can be omitted when the shell's cwd is inside a known repo — cc-cockpit picks the longest-prefix match. |
| `cc-cockpit start-fleet <repo> [name...]` | Open an Agent View pane scoped to `repos[<repo>]` — one pane, many background agents dispatched from inside the TUI. Optional `name` becomes the pane label suffix (`fleet · <repo>: <name>`). Each agent shows up as its own dashboard row. |
| `cc-cockpit end <sid-prefix> [--yes]` | Mark a session ended (synthetic `SessionEnd`) and close its tmux pane. Works from any terminal: with `COCKPIT_STATE_HOME` set, scopes to that workspace; without it, scans every workspace under the state root. **Not final**: if the session was actually still live (e.g. a fleet-dispatched background agent with no pane to close), any later event from it brings it back. Prefixes that match more than one session require `--yes`. |
| `cc-cockpit end all-non-ended --yes` | End every currently non-ended session (across all workspaces when run outside the cockpit, or just the current one when inside) and close their panes. `--yes` required because this always matches multiple sessions. |
| `cc-cockpit reap [--older-than DUR] [--dry-run] [--yes]` | Sweep every workspace and end sessions whose `last_activity` is older than `DUR` (default: `1h`). Use `--dry-run` to preview matches. The cleanup tool for stale "running"/"idle" rows that never fired `SessionEnd`. |
| `cc-cockpit reduce` | (debug) Read `events.jsonl` on stdin, print the reduced state as JSON. Useful for inspecting how the reducer interprets a log. |
| `cc-cockpit --version` | Print version. |
| `cc-cockpit --help` | Short usage. |

**tmux keybindings you actually need** (default `Ctrl-b` prefix; mouse also works):

| Keys | Does |
|---|---|
| Click a pane | Focus it (mouse is on by default) |
| `Ctrl-b o` / `Ctrl-b ←↑↓→` | Move focus between panes |
| `Ctrl-b x` | Kill focused pane (confirm prompt) |
| `Ctrl-b z` | Zoom focused pane to full screen; press again to restore |
| `Ctrl-b Space` | Cycle through layout presets (rebalance pane sizes) |
| `Ctrl-b d` | **Detach** (sessions survive; re-attach with `cc-cockpit open` from the same workspace) |

---

## How it works (30-second version)

1. When you `start`, `cc-cockpit` invokes `tmux split-window -v -f -t <session>:0 -e COCKPIT_SESSION_ACTIVE=1 ... claude` on the private `-L cc-cockpit` server, so the Claude session opens as a pane below the dashboard.
2. The `SessionStart` hook in `~/.claude/settings.json` fires. cc-cockpit ingests it for two kinds of sessions: ones you started with `cc-cockpit start` (gated by the `COCKPIT_SESSION_ACTIVE=1` env var), and Claude Code Agent View background sessions (`claude --bg`, `/bg`, `claude agents`) whose dispatch directory is inside a cc-cockpit workspace. Anything else is silently dropped.
3. Every event (`SessionStart`, `UserPromptSubmit`, `PermissionRequest`, `Notification`, `PostToolUse`, `Stop`, `SessionEnd`) appends an envelope to `events.jsonl` under an exclusive `flock`. Sequence numbers are monotonic; wall-clock is advisory.
4. The dashboard pane loops every 0.5s. It snapshots the log under a shared `flock`, runs the reducer in-process to produce `current.json`, reads additive Claude Code display metadata (`/rename`, `/color`) plus cached native `away_summary` recaps, renders the frame, and rings the bell on new `waiting_input` sessions.
5. There is no daemon. No IPC. No database. If the dashboard dies, `events.jsonl` keeps growing. On restart, the reducer rebuilds the state from the log; display metadata and recaps are re-read from Claude Code's own files.

Everything else is consequences of those five points.

**Ordering assumption** — within a single Claude Code session, hooks fire serially: the `claude` process waits for each hook to exit before proceeding to the next one. So for any given `session_id`, `seq` order matches emission order. Across sessions the flock serializes appends but the reducer operates on disjoint per-session state, so cross-session races don't matter. If Claude Code ever introduces concurrent hooks within one session, the reducer would need a source-side ordering field in the payload.

---

## Nuances & troubleshooting

**"Not in a cc-cockpit workspace"** — you're outside any dir whose ancestors contain `.cc-cockpit/workspace.json`. `cd` into one, or run `cc-cockpit init` from the workspace parent first.

**"init: no child git repos found"** — you're outside a workspace parent, or the repos have not been cloned/symlinked yet. `cd` to the directory that contains the child repos, or run `cc-cockpit init --name <name> label=path`.

**"start: must be run inside the cockpit"** — `cc-cockpit start` only works from a pane opened by the cockpit (needs `$COCKPIT_STATE_HOME` in env, set by the tmux session). Run `cc-cockpit open` first.

**"start: unknown repo 'X'"** — the label isn't in `.repos`. The error lists valid labels.

**Dashboard shows ghost sessions from a previous run** — runtime state persists across detaches and restarts (by design — it's event-sourced). The pane-exited hook handles the common crashed-Claude case automatically; for stranger cases use `cc-cockpit end <prefix>`.

**Bell didn't fire even though I saw Claude asking for permission** — in `permission_mode: "auto"`, Claude auto-approves and the `Notification` fires transiently. The **visible status** may never enter `waiting_input` because the reducer collapses `Notification → PostToolUse` inside one 0.5s tick. The **bell still fires**, because it's driven by new event sequence numbers (any new `Notification`/`PermissionRequest` event), not by the reduced state. To sustain `waiting_input` in the dashboard, press `Shift+Tab` in the Claude pane to cycle out of auto mode.

**Terminal header of Claude window looks weird** — tmux window names are set correctly via `tmux new-window -n "<repo>: <task>"`; what shows up inside Claude's own UI is a claude-side concern.

**Dashboard isn't updating or seems frozen** — the dashboard only repaints when the frame actually changes. If nothing is happening, it's fine. Sanity check: `ls -la ~/.local/state/cc-cockpit/<workspace>/current.json` (mtime should advance every ~0.5s).

**`/rename` or `/color` didn't show up** — cc-cockpit reads Claude Code's own local metadata, not a cc-cockpit command. `/rename` comes from `~/.claude/sessions/*.json`; `/color` comes from recent entries in `~/.claude/history.jsonl`. Make sure you typed the slash command inside the Claude Code session itself. Unknown color names are ignored.

**No `↳ recap:` line yet** — recaps are native Claude Code `away_summary` events, not generated by cc-cockpit. They usually appear about 3 minutes after a session goes quiet, and cc-cockpit only shows them once the session is idle. Sessions that are still `running`, `thinking`, `processing`, `waiting`, or sessions where Claude Code hasn't written an `away_summary` yet, simply omit the line. cc-cockpit never shows a placeholder and never calls another model to force a summary.

**Everything broken, reset hard:**
```bash
cc-cockpit close --all --yes                  # kill any live cockpit tmux session(s)
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
