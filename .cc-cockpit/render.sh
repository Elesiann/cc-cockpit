#!/bin/bash
# render.sh <current.json>
# Simple table render — active sessions on top, ended below.
set -u
CUR="${1:-}"
[ -f "$CUR" ] || { echo "(no current.json yet)"; exit 0; }

WS="${COCKPIT_WORKSPACE_NAME:-?}"

# Header + counts (+ dropped_events warning if any)
jq -r --arg ws "$WS" '
  . as $root
  | (.sessions // {} | to_entries) as $s
  | ($s | map(select(.value.status != "ended")) | length) as $active
  | ($s | map(select(.value.status == "running")) | length) as $running
  | ($s | map(select(.value.status == "waiting_input")) | length) as $waiting
  | ($s | map(select(.value.status == "idle")) | length) as $idle
  | ($s | map(select(.value.status == "ended")) | length) as $ended
  | ($root.dropped_events // 0) as $dropped
  | "─── cc-cockpit · \($ws) ───  active=\($active) (running=\($running) waiting=\($waiting) idle=\($idle))  ended=\($ended)\(if $dropped > 0 then "  ⚠ dropped=\($dropped)" else "" end) ───"
' "$CUR"
echo

# Active sessions table
jq -r '
  def short: if . == null then "—" else (. | tostring)[0:8] end;
  def activity($now):
    if . == null then "—"
    else ($now - (. | fromdateiso8601)) as $d
         | if $d < 60 then "\($d | floor)s"
           elif $d < 3600 then "\(($d/60) | floor)m"
           else "\(($d/3600) | floor)h" end
    end;
  def glyph:
    if   . == "running"       then "▶"
    elif . == "waiting_input" then "●"
    elif . == "idle"          then "◯"
    elif . == "ended"         then "◼"
    else "?" end;
  (now) as $now
  | (.sessions // {}) | to_entries
  | map(select(.value.status != "ended"))
  | sort_by(.value.started_at)
  | if length == 0 then
      "  (no active sessions — spawn one: cc-cockpit spawn --repo <key> --task \"...\")"
    else
      ([["STATUS","SID","REPO","TASK","ACT"]] + (map([
          "\(.value.status | glyph) \(.value.status)",
          (.key | short),
          (.value.primary_repo // "—"),
          ((.value.task_name // "—")[0:40]),
          (.value.last_activity | activity($now))
        ])))
      | map(@tsv) | .[]
    end
' "$CUR" | column -t -s $'\t'

echo

# Ended footer (last 3)
jq -r '
  def short: if . == null then "—" else (. | tostring)[0:8] end;
  def activity($now):
    if . == null then "—"
    else ($now - (. | fromdateiso8601)) as $d
         | if $d < 60 then "\($d | floor)s ago"
           elif $d < 3600 then "\(($d/60) | floor)m ago"
           else "\(($d/3600) | floor)h ago" end
    end;
  (now) as $now
  | (.sessions // {}) | to_entries
  | map(select(.value.status == "ended"))
  | sort_by(.value.ended_at // .value.last_activity) | reverse
  | .[0:3]
  | if length == 0 then empty
    else ("─── ended (last \(length)) ───"),
         (.[] | "  ◼ \(.key[0:8])  \(.value.primary_repo // "—")  \(.value.task_name // "—")  (\(.value.ended_at // .value.last_activity | activity($now)))")
    end
' "$CUR"
