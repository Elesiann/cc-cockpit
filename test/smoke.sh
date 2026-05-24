#!/bin/bash
# smoke.sh — invariant-guarding smoke test for cc-cockpit.
#
# Run from anywhere:   bash test/smoke.sh
# Exits 0 on full pass, non-zero on any failure (prints FAIL: <what>).

set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

BUILD_DIR="$(mktemp -d)"
trap 'rm -rf "$BUILD_DIR"' EXIT
(cd "$HERE" && go build -o "$BUILD_DIR/cc-cockpit" ./cmd/cc-cockpit) \
  || { echo "smoke: go build cc-cockpit failed" >&2; exit 2; }

BIN="$BUILD_DIR/cc-cockpit"
REDUCER=("$BIN" reduce)

PASS=0
FAIL=0

pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); }

SANDBOX="$(mktemp -d)"
export XDG_STATE_HOME="$SANDBOX/state"
trap 'rm -rf "$SANDBOX" "$BUILD_DIR"' EXIT

make_ws() {
  local dir="$1" name="$2"; shift 2
  mkdir -p "$dir/.cc-cockpit"
  local repos="{}"
  if [ $# -gt 0 ]; then
    repos="$(printf '%s\n' "$@" | jq -Rn '[inputs | split("=") | {key:.[0], value:.[1]}] | from_entries')"
  fi
  jq -n --arg n "$name" --argjson r "$repos" '{name:$n, repos:$r}' > "$dir/.cc-cockpit/workspace.json"
}

# =============================================================
echo '[0] install installs PATH symlink and Claude hooks'
# =============================================================
SETTINGS="$SANDBOX/claude/settings.json"
mkdir -p "$(dirname "$SETTINGS")"
cat > "$SETTINGS" <<EOF
{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"echo keep"}]}]}}
EOF
out="$("$BIN" install --bin-dir "$SANDBOX/bin" --settings "$SETTINGS" 2>&1)"
rc=$?
session_start_count="$(jq '[.hooks.SessionStart[] | .hooks[]? | select(.command | contains("cc-cockpit hook SessionStart"))] | length' "$SETTINGS")"
notification_matcher="$(jq -r '.hooks.Notification[-1].matcher' "$SETTINGS")"
preserved_stop="$(jq '[.hooks.Stop[] | .hooks[]? | select(.command == "echo keep")] | length' "$SETTINGS")"
if [ "$rc" -eq 0 ] \
   && [ -L "$SANDBOX/bin/cc-cockpit" ] \
   && [ "$(readlink -f "$SANDBOX/bin/cc-cockpit")" = "$(readlink -f "$BIN")" ] \
   && [ "$session_start_count" = "1" ] \
   && [ "$notification_matcher" = "idle_prompt|permission_prompt" ] \
   && [ "$preserved_stop" = "1" ]; then
  pass 'install symlinks binary, installs hooks, preserves unrelated hooks'
else
  fail "install failed: rc=$rc out='$out' session_start_count=$session_start_count notification_matcher=$notification_matcher preserved_stop=$preserved_stop"
fi

out="$("$BIN" install --bin-dir "$SANDBOX/bin" --settings "$SETTINGS" 2>&1)"
session_start_count="$(jq '[.hooks.SessionStart[] | .hooks[]? | select(.command | contains("cc-cockpit hook SessionStart"))] | length' "$SETTINGS")"
[ "$session_start_count" = "1" ] \
  && pass 'install is idempotent for cc-cockpit hooks' \
  || fail "install duplicated hooks: count=$session_start_count out='$out'"

# uninstall round-trip: must remove every cc-cockpit hook and the symlink,
# leave preserved Stop entry alone, leave the top-level theme key alone.
UNINSTALL_SANDBOX="$SANDBOX/uninstall-test"
mkdir -p "$UNINSTALL_SANDBOX/bin"
cat > "$UNINSTALL_SANDBOX/settings.json" <<'EOF'
{"theme":"dark","hooks":{"Stop":[{"hooks":[{"type":"command","command":"/usr/bin/echo keep"}]}]}}
EOF
"$BIN" install --bin-dir "$UNINSTALL_SANDBOX/bin" --settings "$UNINSTALL_SANDBOX/settings.json" >/dev/null
out="$("$BIN" uninstall --bin-dir "$UNINSTALL_SANDBOX/bin" --settings "$UNINSTALL_SANDBOX/settings.json" 2>&1)"
rc=$?
cockpit_left="$(jq '[.. | objects | .command? // empty | select(contains("cc-cockpit hook"))] | length' "$UNINSTALL_SANDBOX/settings.json")"
keep_left="$(jq '[.hooks.Stop[] | .hooks[]? | select(.command == "/usr/bin/echo keep")] | length' "$UNINSTALL_SANDBOX/settings.json")"
theme="$(jq -r '.theme' "$UNINSTALL_SANDBOX/settings.json")"
if [ "$rc" -eq 0 ] && [ "$cockpit_left" = "0" ] && [ "$keep_left" = "1" ] && [ "$theme" = "dark" ] && [ ! -e "$UNINSTALL_SANDBOX/bin/cc-cockpit" ]; then
  pass 'uninstall removes cockpit hooks + symlink, preserves user data'
else
  fail "uninstall failed: rc=$rc cockpit_left=$cockpit_left keep_left=$keep_left theme=$theme out='$out'"
fi
# Second uninstall must be a clean no-op.
out="$("$BIN" uninstall --bin-dir "$UNINSTALL_SANDBOX/bin" --settings "$UNINSTALL_SANDBOX/settings.json" 2>&1)"
rc=$?
if [ "$rc" -eq 0 ] && echo "$out" | grep -q 'no cc-cockpit hooks' && echo "$out" | grep -q 'no symlink at'; then
  pass 'uninstall is idempotent'
else
  fail "uninstall second-run was not a no-op: rc=$rc out='$out'"
fi

# =============================================================
echo '[1] removed tmux commands are not public CLI'
# =============================================================
for cmd in open close start start-fleet dashboard; do
  out="$("$BIN" "$cmd" 2>&1)"
  rc=$?
  if [ "$rc" -eq 2 ] && echo "$out" | grep -q 'unknown subcommand'; then
    pass "$cmd is removed"
  else
    fail "$cmd still appears callable: rc=$rc out='$out'"
  fi
done
help="$("$BIN" --help 2>&1)"
if ! echo "$help" | grep -Eq '^  (open|close|start|start-fleet|dashboard)([[:space:]]|$)'; then
  pass 'help omits removed tmux commands'
else
  fail "help still mentions removed commands: $help"
fi
out="$("$BIN" watch --ws 2>&1)"
rc=$?
if [ "$rc" -eq 2 ] && echo "$out" | grep -q '\-\-ws requires a value'; then
  pass 'watch --ws without value errors clearly'
else
  fail "watch --ws bare flag: rc=$rc out='$out'"
fi
out="$("$BIN" watch --bogus 2>&1)"
rc=$?
if [ "$rc" -eq 2 ] && echo "$out" | grep -q 'unexpected argument'; then
  pass 'watch rejects unknown flags'
else
  fail "watch unknown flag: rc=$rc out='$out'"
fi

# =============================================================
echo '[2] hook routes global and workspace sessions silently'
# =============================================================
unset COCKPIT_STATE_HOME
out="$(echo '{"session_id":"global1","cwd":"/tmp"}' | "$BIN" hook SessionStart 2>&1)"
rc=$?
global_events="$XDG_STATE_HOME/cc-cockpit/_global/events.jsonl"
if [ "$rc" -eq 0 ] && [ -z "$out" ] && [ -s "$global_events" ]; then
  pass 'hook without workspace: silent and routed to _global'
else
  fail "global hook route failed: rc=$rc out='$out' events=$global_events"
fi

mkdir -p "$SANDBOX/ws-watch/repo"
(cd "$SANDBOX/ws-watch/repo" && git init -q)
make_ws "$SANDBOX/ws-watch" watchws repo=repo
out="$(cd "$SANDBOX/ws-watch/repo" && echo "{\"session_id\":\"ws1\",\"cwd\":\"$SANDBOX/ws-watch/repo\"}" | "$BIN" hook SessionStart 2>&1)"
rc=$?
ws_events="$XDG_STATE_HOME/cc-cockpit/watchws/events.jsonl"
if [ "$rc" -eq 0 ] && [ -z "$out" ] && [ -s "$ws_events" ]; then
  pass 'hook inside workspace routes to workspace state'
else
  fail "workspace hook route failed: rc=$rc out='$out' events=$ws_events"
fi

# =============================================================
echo '[3] init manages optional workspace labels'
# =============================================================
mkdir -p "$SANDBOX/ws-empty"
out="$(cd "$SANDBOX/ws-empty" && "$BIN" init --name emptyws 2>&1)"
rc=$?
empty_repos="$(jq -c '.repos' "$SANDBOX/ws-empty/.cc-cockpit/workspace.json" 2>/dev/null)"
if [ "$rc" -eq 0 ] && [ "$empty_repos" = "{}" ] && echo "$out" | grep -q 'cc-cockpit watch'; then
  pass 'init allows a workspace with no repo labels'
else
  fail "init empty workspace failed: rc=$rc repos=$empty_repos out='$out'"
fi

mkdir -p "$SANDBOX/ws-init/packages/api" "$SANDBOX/ws-init/web"
(cd "$SANDBOX/ws-init/packages/api" && git init -q)
(cd "$SANDBOX/ws-init/web" && git init -q)
out="$(cd "$SANDBOX/ws-init" && "$BIN" init --name initws 2>&1)"
rc=$?
api_path="$(jq -r '.repos.api // empty' "$SANDBOX/ws-init/.cc-cockpit/workspace.json")"
web_path="$(jq -r '.repos.web // empty' "$SANDBOX/ws-init/.cc-cockpit/workspace.json")"
if [ "$rc" -eq 0 ] \
   && [ "$api_path" = "packages/api" ] \
   && [ "$web_path" = "web" ] \
   && echo "$out" | grep -q '^workspace: initws$' \
   && echo "$out" | grep -q 'cc-cockpit watch'; then
  pass 'init auto-discovers child git repos'
else
  fail "init discovery failed: rc=$rc out='$out' api=$api_path web=$web_path"
fi

out="$(cd "$SANDBOX/ws-init" && "$BIN" init --name initws 2>&1)"
rc=$?
if [ "$rc" -eq 2 ] && echo "$out" | grep -q 'workspace already exists'; then
  pass 'init refuses to overwrite workspace.json without --force'
else
  fail "init overwrite guard failed: rc=$rc out='$out'"
fi

mkdir -p "$SANDBOX/ws-explicit/services/api"
(cd "$SANDBOX/ws-explicit/services/api" && git init -q)
out="$(cd "$SANDBOX/ws-explicit" && "$BIN" init --name explicit api=services/api 2>&1)"
rc=$?
explicit_path="$(jq -r '.repos.api // empty' "$SANDBOX/ws-explicit/.cc-cockpit/workspace.json")"
[ "$rc" -eq 0 ] && [ "$explicit_path" = "services/api" ] \
  && pass 'init accepts explicit repo=path specs' \
  || fail "init explicit failed: rc=$rc out='$out' path=$explicit_path"

for bad in '../evil' 'foo/bar' '.hidden' 'a b'; do
  out="$(cd "$SANDBOX" && "$BIN" init --force --name "$bad" 2>&1)"
  rc=$?
  if [ "$rc" -eq 2 ] && echo "$out" | grep -q 'invalid workspace name'; then
    pass "slug '$bad' rejected"
  else
    fail "slug '$bad' NOT rejected: rc=$rc out='$out'"
  fi
done

# =============================================================
echo '[4] doctor validates install and optional workspace health'
# =============================================================
cat > "$SANDBOX/bin/claude" <<'EOF'
#!/bin/bash
exit 0
EOF
chmod +x "$SANDBOX/bin/claude"
out="$(cd "$SANDBOX/ws-init" \
  && CLAUDE_SETTINGS_PATH="$SETTINGS" PATH="$SANDBOX/bin:$PATH" "$BIN" doctor 2>&1)"
rc=$?
if [ "$rc" -eq 0 ] \
   && echo "$out" | grep -q "ok: workspace: initws" \
   && echo "$out" | grep -q "ok: repo 'api': packages/api" \
   && echo "$out" | grep -q "doctor: all checks passed"; then
  pass 'doctor passes for valid install and workspace'
else
  fail "doctor failed unexpectedly: rc=$rc out='$out'"
fi

out="$(cd "$SANDBOX" \
  && CLAUDE_SETTINGS_PATH="$SETTINGS" PATH="$SANDBOX/bin:$PATH" "$BIN" doctor 2>&1)"
rc=$?
if [ "$rc" -eq 0 ] \
   && echo "$out" | grep -q "workspace: not initialized" \
   && echo "$out" | grep -q "doctor: all checks passed"; then
  pass 'doctor treats missing workspace as optional'
else
  fail "doctor should pass without workspace: rc=$rc out='$out'"
fi

# Doctor must flag a stale hook binary path. Plant a settings.json whose
# hook commands explicitly point at a copied binary in $STALE_DIR; then run
# doctor with the build's $BIN. The two paths differ → stale should fire.
STALE_DIR="$SANDBOX/stale"
mkdir -p "$STALE_DIR"
cp "$BIN" "$STALE_DIR/cc-cockpit"
STALE_SETTINGS="$STALE_DIR/settings.json"
echo '{}' > "$STALE_SETTINGS"
"$STALE_DIR/cc-cockpit" install --no-bin --bin-dir "$STALE_DIR" --settings "$STALE_SETTINGS" >/dev/null
out="$(CLAUDE_SETTINGS_PATH="$STALE_SETTINGS" "$BIN" doctor 2>&1)"
rc=$?
if [ "$rc" -eq 1 ] && echo "$out" | grep -q 'hooks point at' && echo "$out" | grep -q "$STALE_DIR/cc-cockpit"; then
  pass 'doctor flags stale hook binary path'
else
  fail "doctor stale-hook check failed: rc=$rc out='$out'"
fi

# =============================================================
echo '[5] reducer tolerates malformed events'
# =============================================================
cat > "$SANDBOX/events-bad.jsonl" <<EOF
{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"s1","payload":{"cwd":"/x"}}
THIS LINE IS NOT JSON
{"seq":99,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","payload":{}}
{"seq":100,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"bad","payload":[]}
{"seq":101,"wall_clock_iso8601":"not-a-date","event_type":"SessionStart","session_id":"badtime","payload":{}}
{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"Stop","session_id":"s1","payload":{}}
EOF
dropped="$("${REDUCER[@]}" < "$SANDBOX/events-bad.jsonl" | jq -r '.dropped_events')"
status="$("${REDUCER[@]}" < "$SANDBOX/events-bad.jsonl" | jq -r '.sessions.s1.status')"
[ "$dropped" = "4" ] && [ "$status" = "completed" ] \
  && pass "reducer: dropped=$dropped, status=$status" \
  || fail "reducer: dropped=$dropped, status=$status (expected 4, completed)"

# =============================================================
echo '[6] end/reap are state-only synthetic SessionEnd tools'
# =============================================================
# A prefix that matches no live session — exercises the "no targets" exit
# path without disturbing the sessions from earlier checks.
out="$("$BIN" end zzz-no-such-session --yes 2>&1)"
rc=$?
if [ "$rc" -eq 0 ] && echo "$out" | grep -q "no matching non-ended sessions"; then
  pass 'end with unmatched prefix exits cleanly'
else
  fail "end unmatched prefix failed: rc=$rc out='$out'"
fi

STATE_END="$XDG_STATE_HOME/cc-cockpit/endws"
mkdir -p "$STATE_END"
cat > "$STATE_END/events.jsonl" <<EOF
{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"endme","payload":{"cwd":"/r"}}
EOF
out="$("$BIN" end endme --yes 2>&1)"
rc=$?
ended_status="$("${REDUCER[@]}" < "$STATE_END/events.jsonl" | jq -r '.sessions.endme.status')"
if [ "$rc" -eq 0 ] && [ "$ended_status" = "ended" ] && ! echo "$out" | grep -q 'closed pane'; then
  pass 'end appends state-only synthetic SessionEnd'
else
  fail "end state-only behavior failed: rc=$rc status=$ended_status out='$out'"
fi

# `all-non-ended` is a wildcard; it must always require --yes even when the
# live set happens to be size 1. Without the guard, a one-session env would
# nuke that session without confirmation.
STATE_ALL="$XDG_STATE_HOME/cc-cockpit/allws"
mkdir -p "$STATE_ALL"
cat > "$STATE_ALL/events.jsonl" <<EOF
{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"solo","payload":{"cwd":"/r"}}
EOF
out="$("$BIN" end all-non-ended 2>&1)"
rc=$?
solo_status="$("${REDUCER[@]}" < "$STATE_ALL/events.jsonl" | jq -r '.sessions.solo.status')"
if [ "$rc" -eq 2 ] && [ "$solo_status" != "ended" ] && echo "$out" | grep -q 're-run with --yes'; then
  pass 'all-non-ended without --yes refuses even when count is 1'
else
  fail "all-non-ended guard failed: rc=$rc status=$solo_status out='$out'"
fi
out="$("$BIN" end all-non-ended --yes 2>&1)"
solo_status="$("${REDUCER[@]}" < "$STATE_ALL/events.jsonl" | jq -r '.sessions.solo.status')"
[ "$solo_status" = "ended" ] \
  && pass 'all-non-ended --yes proceeds' \
  || fail "all-non-ended --yes did not end solo session: status=$solo_status out='$out'"

# =============================================================
echo '[7] synthetic SessionEnd revivable; natural stays terminal'
# =============================================================
cat > "$SANDBOX/events-dismiss.jsonl" <<EOF
{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"a","payload":{"cwd":"/r"}}
{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"SessionStart","session_id":"b","payload":{"cwd":"/r"}}
{"seq":3,"wall_clock_iso8601":"2026-04-20T15:00:02Z","event_type":"SessionEnd","session_id":"a","payload":{"synthetic":true}}
{"seq":4,"wall_clock_iso8601":"2026-04-20T15:00:03Z","event_type":"SessionEnd","session_id":"b","payload":{}}
{"seq":5,"wall_clock_iso8601":"2026-04-20T15:00:04Z","event_type":"UserPromptSubmit","session_id":"a","payload":{"prompt_preview":"alive"}}
{"seq":6,"wall_clock_iso8601":"2026-04-20T15:00:05Z","event_type":"UserPromptSubmit","session_id":"b","payload":{"prompt_preview":"zombie"}}
EOF
a_status="$("${REDUCER[@]}" < "$SANDBOX/events-dismiss.jsonl" | jq -r '.sessions.a.status')"
b_status="$("${REDUCER[@]}" < "$SANDBOX/events-dismiss.jsonl" | jq -r '.sessions.b.status')"
[ "$a_status" = "thinking" ] && [ "$b_status" = "ended" ] \
  && pass "synthetic-end revived (a=$a_status); natural-end terminal (b=$b_status)" \
  || fail "dismissal logic broken: a=$a_status (want thinking), b=$b_status (want ended)"

# =============================================================
echo '[8] reducer determinism'
# =============================================================
"${REDUCER[@]}" < "$SANDBOX/events-dismiss.jsonl" > "$SANDBOX/r1"
"${REDUCER[@]}" < "$SANDBOX/events-dismiss.jsonl" > "$SANDBOX/r2"
cmp -s "$SANDBOX/r1" "$SANDBOX/r2" \
  && pass 'byte-identical across two runs' \
  || fail 'reducer not deterministic'

# =============================================================
echo
echo "────────────────────────────────────────────"
printf "PASS: %d   FAIL: %d\n" "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
