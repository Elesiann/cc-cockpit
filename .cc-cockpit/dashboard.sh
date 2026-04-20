#!/bin/bash
# dashboard.sh — snapshot → reduce → render loop with flicker-free updates
# and event-delta bell detection.
#
# The bell fires on *any* new attention event (Notification/PermissionRequest)
# whose seq is greater than $last_bell_seq, regardless of whether the reduced
# status ever visibly enters waiting_input. This catches transient prompts that
# the reducer collapses to 'running' within a single poll window.
#
# Frame is rendered to a buffer and written with cursor-home + clear-below so
# idle ticks don't flicker.
set -u

: "${COCKPIT_STATE_HOME:?COCKPIT_STATE_HOME not set}"
: "${CC_COCKPIT_HOME:?CC_COCKPIT_HOME not set}"

STATE="$COCKPIT_STATE_HOME"
mkdir -p "$STATE"
touch "$STATE/events.jsonl"

BELL_FILE="$STATE/last_bell_seq"

# Atomic write: tmp + mv. A dashboard killed mid-write would otherwise leave
# $BELL_FILE empty, and the next boot would spam `[: : integer expression
# expected` errors every tick when arithmetic hits the empty value.
write_bell() {
  local v="$1"
  echo "$v" > "$BELL_FILE.tmp" && mv "$BELL_FILE.tmp" "$BELL_FILE"
}

# On first-ever boot, start at the current max seq so we don't replay historical
# attention events. Persist across restarts so a dashboard crash + rerun doesn't
# re-bell the same events. Empty/non-numeric values fall back to 0.
if [ -s "$BELL_FILE" ]; then
  last_bell_seq="$(cat "$BELL_FILE" 2>/dev/null)"
else
  last_bell_seq="$(jq -R -s '
    split("\n") | map(select(length > 0) | fromjson?) | map(select(. != null))
    | [.[] | .seq // 0] | max // 0
  ' < "$STATE/events.jsonl" 2>/dev/null || echo 0)"
  write_bell "$last_bell_seq"
fi
# Guard: a corrupt or empty bell file must not poison arithmetic below.
case "$last_bell_seq" in
  ''|*[!0-9]*) last_bell_seq=0 ;;
esac

prev_frame=""

# Enter alternate screen + hide cursor; restore on exit.
trap 'printf "\033[?25h\033[?1049l"' EXIT INT TERM
printf '\033[?1049h\033[?25l'

while true; do
  stage_err=""

  # Each stage redirects stderr to a .tmp file; on success we remove it so the
  # last-successful tick never carries stale failure context. On failure we
  # promote the .tmp to a durable .err file that the banner points to.
  stage_finalize() {
    local name="$1" rc="$2"
    if [ "$rc" -eq 0 ]; then
      rm -f "$STATE/$name.err.tmp" "$STATE/$name.err"
    else
      mv -f "$STATE/$name.err.tmp" "$STATE/$name.err" 2>/dev/null
    fi
  }

  # 1. snapshot under shared lock
  if ( flock -s 9; cp "$STATE/events.jsonl" "$STATE/events.snapshot.jsonl"; ) 9>"$STATE/events.lock" 2>"$STATE/snapshot.err.tmp"; then
    stage_finalize snapshot 0
  else
    stage_finalize snapshot 1
    stage_err="snapshot"
  fi

  # 2. reduce (atomic via tmp + mv; don't mv on failure so current.json stays
  #    at last-known-good and the banner makes the staleness obvious)
  if [ -z "$stage_err" ]; then
    if "$CC_COCKPIT_HOME/reduce-state.sh" < "$STATE/events.snapshot.jsonl" \
         > "$STATE/current.json.tmp" 2>"$STATE/reduce.err.tmp"; then
      mv "$STATE/current.json.tmp" "$STATE/current.json"
      stage_finalize reduce 0
    else
      stage_finalize reduce 1
      stage_err="reduce"
    fi
  fi

  # 3. render (always try; reads whatever current.json exists)
  body=""
  render_rc=0
  body="$("$CC_COCKPIT_HOME/render.sh" "$STATE/current.json" 2>"$STATE/render.err.tmp")" || render_rc=$?
  stage_finalize render "$render_rc"
  [ "$render_rc" -ne 0 ] && stage_err="${stage_err:+$stage_err, }render"

  # Compose frame: prepend a banner when any stage failed so a stale screen
  # can never masquerade as a live one.
  if [ -n "$stage_err" ]; then
    frame="⚠ DASHBOARD STAGE FAILED: $stage_err — displayed state may be stale.
   logs: $STATE/{snapshot,reduce,render}.err
────────────────────────────────────────────────────────────────
$body"
  else
    frame="$body"
  fi

  # 4. write frame only if it changed
  if [ "$frame" != "$prev_frame" ]; then
    printf '\033[H'
    printf '%s\n' "$frame" | awk '{printf "%s\033[K\n", $0}'
    printf '\033[J'
    prev_frame="$frame"
  fi

  # 5. bell on new attention events (event-delta, not reduced-state)
  # Read new attention count and new max seq from the snapshot in one pass.
  read -r new_attn max_seq < <(jq -R -s -r --argjson last "$last_bell_seq" '
    split("\n")
    | map(select(length > 0) | fromjson?)
    | map(select(. != null)) as $events
    | ($events
        | map(select(.seq > $last
                     and (.event_type == "Notification"
                          or .event_type == "PermissionRequest")))
        | length) as $new_attn
    | ($events | [.[] | .seq // 0] | max // $last) as $max_seq
    | "\($new_attn) \($max_seq)"
  ' < "$STATE/events.snapshot.jsonl" 2>/dev/null || echo "0 $last_bell_seq")

  if [ "${new_attn:-0}" -gt 0 ]; then
    printf '\a'
  fi
  if [ "${max_seq:-0}" -gt "$last_bell_seq" ]; then
    last_bell_seq="$max_seq"
    write_bell "$last_bell_seq"
  fi

  sleep 0.5
done
