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

# On first-ever boot, start at the current max seq so we don't replay historical
# attention events. Persist across restarts so a dashboard crash + rerun doesn't
# re-bell the same events.
if [ -f "$BELL_FILE" ]; then
  last_bell_seq="$(cat "$BELL_FILE")"
else
  last_bell_seq="$(jq -R -s '
    split("\n") | map(select(length > 0) | fromjson?) | map(select(. != null))
    | [.[] | .seq // 0] | max // 0
  ' < "$STATE/events.jsonl" 2>/dev/null || echo 0)"
  echo "$last_bell_seq" > "$BELL_FILE"
fi

prev_frame=""

# Enter alternate screen + hide cursor; restore on exit.
trap 'printf "\033[?25h\033[?1049l"' EXIT INT TERM
printf '\033[?1049h\033[?25l'

while true; do
  stage_err=""

  # 1. snapshot under shared lock
  if ! ( flock -s 9; cp "$STATE/events.jsonl" "$STATE/events.snapshot.jsonl"; ) 9>"$STATE/events.lock" 2>"$STATE/snapshot.err"; then
    stage_err="snapshot"
  fi

  # 2. reduce (atomic via tmp + mv; don't mv on failure so current.json stays
  #    at last-known-good and the banner makes the staleness obvious)
  if [ -z "$stage_err" ]; then
    if "$CC_COCKPIT_HOME/reduce-state.sh" < "$STATE/events.snapshot.jsonl" \
         > "$STATE/current.json.tmp" 2>"$STATE/reduce.err"; then
      mv "$STATE/current.json.tmp" "$STATE/current.json"
    else
      stage_err="reduce"
    fi
  fi

  # 3. render (always try; reads whatever current.json exists)
  body=""
  render_rc=0
  body="$("$CC_COCKPIT_HOME/render.sh" "$STATE/current.json" 2>"$STATE/render.err")" || render_rc=$?
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
    echo "$last_bell_seq" > "$BELL_FILE"
  fi

  sleep 0.5
done
