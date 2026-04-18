#!/bin/bash
# dashboard.sh — snapshot → reduce → render loop with flicker-free updates.
# Frame is rendered to a buffer, then written to the terminal with a single
# home-cursor + clear-below sequence. Avoids the full-clear flash.
set -u

: "${COCKPIT_STATE_HOME:?COCKPIT_STATE_HOME not set}"
: "${CC_COCKPIT_HOME:?CC_COCKPIT_HOME not set}"

STATE="$COCKPIT_STATE_HOME"
mkdir -p "$STATE"
touch "$STATE/events.jsonl"

prev_waiting=""
prev_frame=""

# Enter alternate screen + hide cursor; restore on exit.
trap 'printf "\033[?25h\033[?1049l"' EXIT INT TERM
printf '\033[?1049h\033[?25l'

while true; do
  # 1. snapshot under shared lock
  ( flock -s 9; cp "$STATE/events.jsonl" "$STATE/events.snapshot.jsonl"; ) 9>"$STATE/events.lock"

  # 2. reduce (atomic via tmp + mv)
  "$CC_COCKPIT_HOME/reduce-state.sh" < "$STATE/events.snapshot.jsonl" \
    > "$STATE/current.json.tmp" \
    && mv "$STATE/current.json.tmp" "$STATE/current.json"

  # 3. build frame in-memory
  frame="$("$CC_COCKPIT_HOME/render.sh" "$STATE/current.json" 2>/dev/null)"

  # 4. write frame only if it changed — silence writes between ticks = no flicker at all
  if [ "$frame" != "$prev_frame" ]; then
    # Cursor home, then print each line with clear-to-end, then clear-below.
    printf '\033[H'
    printf '%s\n' "$frame" | awk '{printf "%s\033[K\n", $0}'
    printf '\033[J'
    prev_frame="$frame"
  fi

  # 5. bell on new waiting_input
  curr_waiting="$(jq -r '
    [ (.sessions // {}) | to_entries[] | select(.value.status=="waiting_input") | .key ]
    | sort | join(",")
  ' "$STATE/current.json" 2>/dev/null)"
  new="$(comm -13 \
      <(printf '%s\n' "$prev_waiting" | tr , '\n' | sort -u) \
      <(printf '%s\n' "$curr_waiting" | tr , '\n' | sort -u) \
      | grep -v '^$' || true)"
  [ -n "$new" ] && printf '\a'
  prev_waiting="$curr_waiting"

  sleep 0.5
done
