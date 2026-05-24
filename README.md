# cc-cockpit

**A watch-only attention layer for Claude Code sessions. One terminal shows every active session across your machine, with live status, native Claude Code labels/colors, desktop alerts, and short recaps when Claude writes them.**

[![CI](https://github.com/Elesiann/cc-cockpit/actions/workflows/ci.yml/badge.svg)](https://github.com/Elesiann/cc-cockpit/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/Elesiann/cc-cockpit)](https://github.com/Elesiann/cc-cockpit/releases/latest)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

---

## Quickstart

```bash
go install github.com/elesiann/cc-cockpit/cmd/cc-cockpit@latest
cc-cockpit install
cc-cockpit watch
```

Then start Claude normally in any other terminal:

```bash
cd ~/work/api
claude
```

With the hooks installed, the session appears in `cc-cockpit watch` within one tick. cc-cockpit does not launch Claude, manage panes, create branches, or touch your repos. It only reads Claude Code hook events and renders the state.

---

## Why this exists

Claude Code made it cheap to run several coding sessions at once. The bottleneck moved from compute to attention: you need to know which session is working, which one is waiting for input, and what happened while you were away.

cc-cockpit lives above the agent layer. It does not dispatch agents, isolate file edits, manage worktrees, or open pull requests. It watches the sessions you already started and gives you one place to scan them.

### Principles

- **Observation, not orchestration.** cc-cockpit reads hook events; it never starts Claude for you.
- **Your repos stay untouched.** No worktrees, no auto-branches, no generated project files.
- **No daemon.** Just Claude Code hooks, an append-only event log, and a terminal dashboard.
- **Composable.** Use vanilla `claude`, background agents, or any workflow that emits Claude Code hook events.

---

## Screenshot

```text
── watch · 3 workspace(s) ──  active=3  🔧 1  ⏸️ 1  💤 1  ended=0 ──

─── active (3) ───
  STATUS        SID       WS       REPO     TASK       ACT
  ⏳ processing a7b2...   api      api      auth       2m
  ⏸️ waiting    3f1c...   portal   portal   review     10s
  💤 idle       c90e...   infra    infra    ci         14m
    ↳ recap: Goal: clear migration debt...

─── commands ─── (in any terminal, prefix `cc-cockpit`)
  end <prefix>               end a session in dashboard state
  end all-non-ended --yes    nuke every non-ended session
  reap [--older-than DUR]    sweep stale sessions (default: idle > 1h)
  Ctrl-C                     exit watch
```

Actual renders include full task labels, activity timers, `/rename` names, `/color` row coloring, native recaps when available, subagent rollups, and an ended footer.

---

## Prerequisites

- Linux, macOS, or WSL2.
- Claude Code 2.1+ with the hook event model: SessionStart, UserPromptSubmit, PermissionRequest, Notification, PreToolUse, PostToolUse, Stop, SessionEnd.
- Go 1.25+ if building from source.

---

## Install

There are two ways to get the binary onto your `PATH`. After either one, run `cc-cockpit install` once to register the Claude Code hooks.

```bash
# Option A: build from source
go install github.com/elesiann/cc-cockpit/cmd/cc-cockpit@latest

# Option B: download a release binary (linux/amd64; see Releases for other platforms)
curl -L https://github.com/elesiann/cc-cockpit/releases/latest/download/cc-cockpit_linux_amd64.tar.gz \
  | tar -xz -C ~/.local/bin/ cc-cockpit

# Then install hooks
cc-cockpit install
```

`cc-cockpit install` symlinks the binary into `~/.local/bin/`, backs up `~/.claude/settings.json` if it would change, and merges the cc-cockpit hook entries. Re-run it any time without duplicating hooks.

Useful flags: `--bin-dir DIR`, `--settings FILE`, `--no-bin`, `--no-hooks`.

To remove everything cc-cockpit installed, run `cc-cockpit uninstall` (see [Uninstall](#uninstall)).

The `Notification` hook uses `matcher: "idle_prompt|permission_prompt"`, which is the signal that Claude is waiting on you.

---

## Watch Mode

```bash
cc-cockpit watch                 # every workspace
cc-cockpit watch --ws api,web    # only these workspaces
```

`watch` opens a read-only dashboard in the current terminal. It scans all state dirs under `~/.local/state/cc-cockpit/*/events.jsonl` or `$XDG_STATE_HOME/cc-cockpit/*/events.jsonl`, then renders one row per non-ended session. The `--ws` flag accepts a comma-separated list (or repeated `--ws=<name>`) and restricts the view to those workspace names — useful when one project is noisy and you want to focus on another.

The first time you run `watch` on a fresh machine you'll see an `(install) cc-cockpit install in another terminal` hint instead of an empty table — once hooks are installed and you start `claude`, the first row appears within one tick.

Sessions are routed by cwd:

- If the cwd is inside a directory with `.cc-cockpit/workspace.json`, the session lands in that workspace's state dir.
- Otherwise, the session lands in `_global`.

You do not need a workspace to use cc-cockpit. Workspaces are only for stable names and repo labels.

`watch` is designed to reduce interpretation work:

- **Granular status:** `running`, `thinking`, `processing`, `waiting`, `completed`, and `idle`, with compact glyphs.
- **Native names and colors:** Claude Code `/rename` changes the TASK column; `/color <name>` colors the row.
- **Native recaps:** Claude Code `away_summary` text appears as a dim `↳ recap:` line when the session is idle.
- **Subagent rollups:** sidechain/subagent transcripts appear as a compact `↳ agents:` line below the parent session.
- **Stale flag:** mid-turn sessions with no events for 15 minutes render with `?`.
- **Desktop notifications:** waiting sessions call `wsl-notify-send.exe`, `notify-send`, or `osascript` when available.
- **Clean exit:** `Ctrl-C` restores the terminal.

---

## Optional Workspaces

A workspace is a directory with `.cc-cockpit/workspace.json`.

```bash
cc-cockpit init --name my-workspace api=packages/api web=packages/web
```

The `name` field chooses the runtime state directory. Repo labels are optional validation/documentation metadata; rows still fall back to the hook payload cwd when Claude does not provide an explicit repo label.

If you skip `init`, sessions still show up under `_global`.

---

## Commands

| Command | Use |
|---|---|
| `cc-cockpit install [--bin-dir DIR] [--settings FILE] [--no-bin] [--no-hooks]` | Symlink the binary onto `PATH` and merge Claude Code hooks. |
| `cc-cockpit uninstall [--bin-dir DIR] [--settings FILE] [--no-bin] [--no-hooks]` | Remove cc-cockpit hook entries from `settings.json` and the PATH symlink. Idempotent. |
| `cc-cockpit init [--name NAME] [repo=path ...]` | Create optional `.cc-cockpit/workspace.json` labels. |
| `cc-cockpit doctor` | Check binary, Claude, hooks (including stale binary paths), and optional workspace config. |
| `cc-cockpit watch [--ws X,Y]` | Aggregate every active Claude session in the current terminal. `--ws` restricts to selected workspace name(s). |
| `cc-cockpit end <sid-prefix> [--yes]` | Append a synthetic `SessionEnd` for matching non-ended sessions. |
| `cc-cockpit end all-non-ended --yes` | Mark every currently non-ended session as ended in dashboard state. Always requires `--yes`. |
| `cc-cockpit reap [--older-than DUR] [--dry-run] [--yes]` | Mark sessions older than `DUR` as ended. Default: `1h`. |
| `cc-cockpit reduce` | Read `events.jsonl` on stdin and print reduced state JSON. |
| `cc-cockpit --version` | Print version. |
| `cc-cockpit --help` | Short usage. |

`end` and `reap` only change cc-cockpit dashboard state. They do not close terminals or kill Claude processes. A later real event from the same synthetic-ended session brings it back.

---

## How It Works

1. `cc-cockpit install` adds Claude Code hook commands to `~/.claude/settings.json`.
2. Claude Code invokes `cc-cockpit hook <Event>` for each subscribed event.
3. The hook resolver maps the event to a state dir using the payload cwd and optional workspace config.
4. `state.Append` writes one JSONL line to `events.jsonl` with a monotonic `seq`.
5. `watch` snapshots each log under a shared lock, reduces the events, loads Claude display metadata and recaps, renders the table, and rings the bell for new attention events.

---

## Troubleshooting

**No sessions appear**: run `cc-cockpit install`, then start a new Claude session. Existing sessions that started before hook installation may not emit the startup events cc-cockpit needs.

**Everything appears under `_global`**: that is expected without `.cc-cockpit/workspace.json`. Run `cc-cockpit init --name <name>` at a workspace root if you want named grouping.

**Rows do not disappear**: use `/quit` in Claude when possible. For stale dashboard rows, use `cc-cockpit end <sid-prefix>` or `cc-cockpit reap --older-than 1h --yes`.

**Desktop notifications do not fire**: cc-cockpit only uses a notifier if `wsl-notify-send.exe`, `notify-send`, or `osascript` is on `PATH`.

---

## Uninstall

```bash
cc-cockpit uninstall
```

This removes only cc-cockpit's footprint:

- Hook entries that carry a `cc-cockpit hook <Event>` command, from `~/.claude/settings.json`. Every other tool's hooks and every top-level key (`theme`, `permissions`, …) are preserved. A timestamped backup is written next to the settings file before changes.
- The `~/.local/bin/cc-cockpit` symlink (or `--bin-dir <DIR>/cc-cockpit`). Refuses to delete a regular file there — that might be a manually-built binary, not a symlink cc-cockpit owns.

Per-workspace event logs under `~/.local/state/cc-cockpit/` (or `$XDG_STATE_HOME/cc-cockpit/`) are intentionally left in place. To clear them too:

```bash
rm -rf ~/.local/state/cc-cockpit   # or $XDG_STATE_HOME/cc-cockpit
```

`uninstall` accepts the same flags as `install`: `--bin-dir`, `--settings`, `--no-bin`, `--no-hooks`. Running it twice is a clean no-op.

---

## Project Status

Stable, single-author project. If you try it and it breaks, open an issue with the event log (`events.jsonl`) attached. PRs welcome; open a discussion issue first so we can align on scope.
