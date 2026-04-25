#!/bin/bash
# smoke.sh — invariant-guarding smoke test for cc-cockpit.
#
# Run from anywhere:   bash test/smoke.sh
# Exits 0 on full pass, non-zero on any failure (prints FAIL: <what>).
#
# Covers the eight invariants from the design:
#  (1) hook is silent when COCKPIT_SESSION_ACTIVE is unset
#  (2) workspace slug validation rejects traversal / slashes
#  (3) canonical-root binding rejects name collisions
#  (4) spawn containment rejects ../escape and non-git dirs
#  (5) reducer tolerates malformed events, reports dropped_events
#  (6) bell fires on new attention events even when reducer collapses state
#  (7) synthetic SessionEnd is revivable; natural SessionEnd is terminal
#  (8) reducer is deterministic (byte-identical on repeat runs)
#
# Also covers targeted hardening regressions and the Zellij spawn layout.
#
# Does NOT cover: actual Zellij launch, live-instance lock end-to-end (needs
# two real opens, one of which execs Zellij). Those are validated by manual
# smoke testing during development.

set -u
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BIN="$HERE/.cc-cockpit/bin/cc-cockpit"
REDUCER="$HERE/.cc-cockpit/reduce-state.sh"
RENDER="$HERE/.cc-cockpit/render.sh"
LAYOUT="$HERE/.cc-cockpit/layouts/initial.kdl"

PASS=0
FAIL=0

pass() { printf '  \033[32mPASS\033[0m %s\n' "$1"; PASS=$((PASS+1)); }
fail() { printf '  \033[31mFAIL\033[0m %s\n' "$1"; FAIL=$((FAIL+1)); }

# ----- sandbox -----
SANDBOX="$(mktemp -d)"
export XDG_STATE_HOME="$SANDBOX/state"
trap 'rm -rf "$SANDBOX"' EXIT

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
out="$("$HERE/install" --bin-dir "$SANDBOX/bin" --settings "$SETTINGS" 2>&1)"
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

# =============================================================
echo '[1] hook silent without COCKPIT_SESSION_ACTIVE'
# =============================================================
unset COCKPIT_SESSION_ACTIVE COCKPIT_STATE_HOME
out="$(echo '{"session_id":"x","cwd":"/tmp"}' | "$BIN" hook SessionStart 2>&1)"
rc=$?
if [ "$rc" -eq 0 ] && [ -z "$out" ] && [ ! -d "$XDG_STATE_HOME" ]; then
  pass 'hook without COCKPIT_SESSION_ACTIVE: exit 0, no output, no state dir touched'
else
  fail "hook guard breached: rc=$rc out='$out' state_dir=$(ls "$XDG_STATE_HOME" 2>/dev/null)"
fi

# =============================================================
echo '[2] workspace name slug validation'
# =============================================================
mkdir -p "$SANDBOX/ws-badjson/.cc-cockpit"
printf '{not json\n' > "$SANDBOX/ws-badjson/.cc-cockpit/workspace.json"
out="$(cd "$SANDBOX/ws-badjson" && "$BIN" open 2>&1 < /dev/null)"
if echo "$out" | grep -q 'workspace.json must be a valid JSON object' \
   && ! echo "$out" | grep -q '^jq:'; then
  pass 'invalid workspace.json rejected without raw jq noise'
else
  fail "invalid workspace.json error noisy/unclear: '$out'"
fi

cd "$SANDBOX" && mkdir -p ws-badslug/.cc-cockpit
for bad in '../evil' 'foo/bar' '.hidden' '' 'a b'; do
  echo "{\"name\":\"$bad\",\"repos\":{}}" > "$SANDBOX/ws-badslug/.cc-cockpit/workspace.json"
  out="$(cd "$SANDBOX/ws-badslug" && "$BIN" open 2>&1 < /dev/null)"
  if echo "$out" | grep -q 'invalid workspace name\|missing .name'; then
    pass "slug '$bad' rejected"
  else
    fail "slug '$bad' NOT rejected: '$out'"
  fi
done

# =============================================================
echo '[3] init bootstraps workspace.json'
# =============================================================
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
   && echo "$out" | grep -q '^repos:$' \
   && echo "$out" | grep -q 'cc-cockpit start api <task>'; then
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

mkdir -p "$SANDBOX/open-fake-bin"
cat > "$SANDBOX/open-fake-bin/zellij" <<EOF
#!/bin/bash
printf '%s\n' "\$@" > "$SANDBOX/open-zellij.args"
EOF
chmod +x "$SANDBOX/open-fake-bin/zellij"
out="$(cd "$SANDBOX/ws-init" && PATH="$SANDBOX/open-fake-bin:$PATH" "$BIN" open 2>&1)"
rc=$?
if [ "$rc" -eq 0 ] \
   && grep -qx -- '--layout' "$SANDBOX/open-zellij.args"; then
  pass 'open launches Zellij for an initialized workspace'
else
  fail "open failed: rc=$rc out='$out' args='$(cat "$SANDBOX/open-zellij.args" 2>/dev/null)'"
fi

# =============================================================
echo '[4] canonical-root binding blocks name collision'
# =============================================================
mkdir -p "$SANDBOX/ws-a/child" "$SANDBOX/ws-b/child"
(cd "$SANDBOX/ws-a/child" && git init -q)
(cd "$SANDBOX/ws-b/child" && git init -q)
make_ws "$SANDBOX/ws-a" collide child=child
make_ws "$SANDBOX/ws-b" collide child=child

# Manually pre-seed state as if ws-a had opened (since real open execs zellij)
STATE_COLLIDE="$XDG_STATE_HOME/cc-cockpit/collide"
mkdir -p "$STATE_COLLIDE"
echo "$(realpath "$SANDBOX/ws-a")" > "$STATE_COLLIDE/workspace_root"
# Now ws-b opens with same name — should fail
out="$(cd "$SANDBOX/ws-b" && "$BIN" open 2>&1 < /dev/null)"
if echo "$out" | grep -q 'already bound to'; then
  pass 'collision rejected with clear error'
else
  fail "collision NOT rejected: '$out'"
fi

# =============================================================
echo '[5] spawn containment + git-repo check'
# =============================================================
mkdir -p "$SANDBOX/ws-spawn/good" "$SANDBOX/ws-spawn/notgit" "$SANDBOX/outside-ws"
(cd "$SANDBOX/ws-spawn/good" && git init -q)
make_ws "$SANDBOX/ws-spawn" spawntest good=good notgit=notgit escape=../outside-ws

export ZELLIJ=fake
export CC_COCKPIT_HOME="$HERE/.cc-cockpit"
export COCKPIT_STATE_HOME="$SANDBOX/spawn-state"
export CC_COCKPIT_WORKSPACE_ROOT="$SANDBOX/ws-spawn"
export COCKPIT_WORKSPACE_NAME=spawntest

out="$("$BIN" spawn --repo escape --task t 2>&1)"
echo "$out" | grep -q 'outside workspace root' && pass 'escape (../outside-ws) rejected' || fail "escape accepted: '$out'"

out="$("$BIN" spawn --repo notgit --task t 2>&1)"
echo "$out" | grep -q 'not a git repo' && pass 'non-git dir rejected' || fail "non-git accepted: '$out'"

FAKE_BIN="$SANDBOX/fake-bin"
mkdir -p "$FAKE_BIN"
cat > "$FAKE_BIN/zellij" <<EOF
#!/bin/bash
printf '%s\n' "\$@" > "$SANDBOX/zellij-spawn.args"
EOF
cat > "$FAKE_BIN/claude" <<'EOF'
#!/bin/bash
exit 0
EOF
chmod +x "$FAKE_BIN/zellij" "$FAKE_BIN/claude"
OLD_PATH="$PATH"
export PATH="$FAKE_BIN:$PATH"
out="$("$BIN" spawn --repo good --task "layout test" 2>&1)"
rc=$?
export PATH="$OLD_PATH"
if [ "$rc" -eq 0 ] \
   && grep -qx -- 'new-pane' "$SANDBOX/zellij-spawn.args" \
   && grep -qx -- '--cwd' "$SANDBOX/zellij-spawn.args" \
   && ! grep -qx -- '--direction' "$SANDBOX/zellij-spawn.args"; then
  pass 'spawn lets Zellij auto-layout new panes'
else
  fail "spawn did not use auto-layout new-pane: rc=$rc out='$out' args='$(cat "$SANDBOX/zellij-spawn.args" 2>/dev/null)'"
fi

OLD_PATH="$PATH"
export PATH="$FAKE_BIN:$PATH"
out="$("$BIN" start good shorthand task 2>&1)"
rc=$?
export PATH="$OLD_PATH"
if [ "$rc" -eq 0 ] \
   && grep -qx -- 'good: shorthand task' "$SANDBOX/zellij-spawn.args" \
   && grep -qx -- 'COCKPIT_TASK_NAME=shorthand task' "$SANDBOX/zellij-spawn.args"; then
  pass 'start accepts repo plus unquoted task words'
else
  fail "start shorthand failed: rc=$rc out='$out' args='$(cat "$SANDBOX/zellij-spawn.args" 2>/dev/null)'"
fi

OLD_PATH="$PATH"
export PATH="$FAKE_BIN:$PATH"
out="$("$BIN" spawn good positional task 2>&1)"
rc=$?
export PATH="$OLD_PATH"
if [ "$rc" -eq 0 ] \
   && grep -qx -- 'good: positional task' "$SANDBOX/zellij-spawn.args" \
   && grep -qx -- 'COCKPIT_TASK_NAME=positional task' "$SANDBOX/zellij-spawn.args"; then
  pass 'spawn accepts shorthand repo plus task words'
else
  fail "spawn shorthand failed: rc=$rc out='$out' args='$(cat "$SANDBOX/zellij-spawn.args" 2>/dev/null)'"
fi

unset ZELLIJ CC_COCKPIT_HOME COCKPIT_STATE_HOME CC_COCKPIT_WORKSPACE_ROOT COCKPIT_WORKSPACE_NAME

# =============================================================
echo '[6] spawn rejects flags without values cleanly'
# =============================================================
for flag in --repo --task --related; do
  out="$("$BIN" spawn "$flag" 2>&1)"
  rc=$?
  if [ "$rc" -eq 2 ] \
     && echo "$out" | grep -q "spawn: $flag requires a value" \
     && ! echo "$out" | grep -q 'unbound variable'; then
    pass "spawn $flag without value rejected cleanly"
  else
    fail "spawn $flag bad error: rc=$rc out='$out'"
  fi
done

out="$("$BIN" spawn --repo --task t 2>&1)"
rc=$?
if [ "$rc" -eq 2 ] \
   && echo "$out" | grep -q 'spawn: --repo requires a value' \
   && ! echo "$out" | grep -q 'unbound variable'; then
  pass 'spawn --repo followed by another flag rejected as missing value'
else
  fail "spawn --repo accepted another flag as value: rc=$rc out='$out'"
fi

# =============================================================
echo '[7] initial layout has bottom spawn row swap layout'
# =============================================================
if grep -q 'swap_tiled_layout name="cc-cockpit"' "$LAYOUT" \
   && grep -q 'tab min_panes=3' "$LAYOUT" \
   && grep -q 'children' "$LAYOUT"; then
  pass 'layout defines bottom row for spawned panes'
else
  fail 'layout missing cc-cockpit swap layout for spawned panes'
fi

# =============================================================
echo '[8] reducer tolerates malformed events'
# =============================================================
cat > "$SANDBOX/events-bad.jsonl" <<EOF
{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"s1","payload":{"primary_repo":"r","declared_related_repos":[],"task_name":"t","cwd":"/x"}}
THIS LINE IS NOT JSON
{"seq":99,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","payload":{}}
{"seq":100,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"bad","payload":[]}
{"seq":101,"wall_clock_iso8601":"not-a-date","event_type":"SessionStart","session_id":"badtime","payload":{}}
{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"Stop","session_id":"s1","payload":{}}
EOF
dropped="$("$REDUCER" < "$SANDBOX/events-bad.jsonl" | jq -r '.dropped_events')"
status="$("$REDUCER" < "$SANDBOX/events-bad.jsonl" | jq -r '.sessions.s1.status')"
[ "$dropped" = "4" ] && [ "$status" = "idle" ] \
  && pass "reducer: dropped=$dropped, status=$status" \
  || fail "reducer: dropped=$dropped, status=$status (expected 4, idle)"

# =============================================================
echo '[9] render fails loudly on corrupt current.json'
# =============================================================
cat > "$SANDBOX/current-badtime.json" <<EOF
{"sessions":{"s1":{"status":"running","started_at":"not-a-date","last_activity":"not-a-date","primary_repo":"r","task_name":"t"}},"dropped_events":0}
EOF
if "$RENDER" "$SANDBOX/current-badtime.json" > "$SANDBOX/render.out" 2> "$SANDBOX/render.err"; then
  fail 'render accepted invalid timestamps with exit 0'
elif grep -q 'date "not-a-date"' "$SANDBOX/render.err"; then
  pass 'render exits non-zero when date parsing fails'
else
  fail "render failed without useful date error: $(cat "$SANDBOX/render.err")"
fi

# =============================================================
echo '[10] mark-ended tolerates empty current sessions'
# =============================================================
mkdir -p "$SANDBOX/mark-empty"
touch "$SANDBOX/mark-empty/events.jsonl"
echo '{"sessions":null}' > "$SANDBOX/mark-empty/current.json"
out="$(COCKPIT_STATE_HOME="$SANDBOX/mark-empty" "$BIN" mark-ended all-non-ended --yes 2>&1)"
rc=$?
if [ "$rc" -eq 0 ] \
   && echo "$out" | grep -q "no matching non-ended sessions" \
   && ! echo "$out" | grep -q '^jq:'; then
  pass 'mark-ended handles empty sessions without jq noise'
else
  fail "mark-ended empty sessions noisy/failing: rc=$rc out='$out'"
fi

# =============================================================
echo '[11] bell event-delta: Notification counts even when reducer collapses'
# =============================================================
cat > "$SANDBOX/events-transient.jsonl" <<EOF
{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"s1","payload":{}}
{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"Notification","session_id":"s1","payload":{"notification_type":"idle_prompt"}}
{"seq":3,"wall_clock_iso8601":"2026-04-20T15:00:02Z","event_type":"PostToolUse","session_id":"s1","payload":{"tool_name":"W"}}
EOF
attn="$(jq -R -s -r --argjson last 0 '
  split("\n") | map(select(length>0) | fromjson?) | map(select(. != null))
  | map(select(.seq > $last and (.event_type=="Notification" or .event_type=="PermissionRequest")))
  | length
' < "$SANDBOX/events-transient.jsonl")"
collapsed="$("$REDUCER" < "$SANDBOX/events-transient.jsonl" | jq -r '.sessions.s1.status')"
# Bell would fire on seq=2 (Notification). Reducer ends at 'running'.
[ "$attn" = "1" ] && [ "$collapsed" = "running" ] \
  && pass "transient Notification detected (bell=1) despite reducer 'running'" \
  || fail "bell or collapse broken: attn=$attn status=$collapsed"

# =============================================================
echo '[12] synthetic SessionEnd revivable; natural stays terminal'
# =============================================================
cat > "$SANDBOX/events-dismiss.jsonl" <<EOF
{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"a","payload":{"primary_repo":"r","task_name":"ta","declared_related_repos":[]}}
{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"SessionStart","session_id":"b","payload":{"primary_repo":"r","task_name":"tb","declared_related_repos":[]}}
{"seq":3,"wall_clock_iso8601":"2026-04-20T15:00:02Z","event_type":"SessionEnd","session_id":"a","payload":{"synthetic":true}}
{"seq":4,"wall_clock_iso8601":"2026-04-20T15:00:03Z","event_type":"SessionEnd","session_id":"b","payload":{}}
{"seq":5,"wall_clock_iso8601":"2026-04-20T15:00:04Z","event_type":"UserPromptSubmit","session_id":"a","payload":{"prompt_preview":"alive"}}
{"seq":6,"wall_clock_iso8601":"2026-04-20T15:00:05Z","event_type":"UserPromptSubmit","session_id":"b","payload":{"prompt_preview":"zombie"}}
EOF
a_status="$("$REDUCER" < "$SANDBOX/events-dismiss.jsonl" | jq -r '.sessions.a.status')"
b_status="$("$REDUCER" < "$SANDBOX/events-dismiss.jsonl" | jq -r '.sessions.b.status')"
[ "$a_status" = "running" ] && [ "$b_status" = "ended" ] \
  && pass "synthetic-end revived (a=$a_status); natural-end terminal (b=$b_status)" \
  || fail "dismissal logic broken: a=$a_status (want running), b=$b_status (want ended)"

# =============================================================
echo '[13] reducer determinism'
# =============================================================
"$REDUCER" < "$SANDBOX/events-dismiss.jsonl" > "$SANDBOX/r1"
"$REDUCER" < "$SANDBOX/events-dismiss.jsonl" > "$SANDBOX/r2"
cmp -s "$SANDBOX/r1" "$SANDBOX/r2" \
  && pass 'byte-identical across two runs' \
  || fail 'reducer not deterministic'

# =============================================================
echo
echo "────────────────────────────────────────────"
printf "PASS: %d   FAIL: %d\n" "$PASS" "$FAIL"
[ "$FAIL" -eq 0 ]
