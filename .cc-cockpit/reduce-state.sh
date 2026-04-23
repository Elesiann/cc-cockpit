#!/bin/bash
# reduce-state.sh — consume events.jsonl from stdin, emit current.json on stdout.
#
# Robust to malformed events: uses line-oriented parsing with `fromjson?` so a
# single truncated / corrupted record doesn't poison the whole reducer. Invalid
# JSON lines and structurally invalid event objects are counted in
# `dropped_events` (top-level sibling of `sessions`).
#
# Ordering assumption: within a single Claude Code session, hooks fire serially
# (the claude process waits for each hook's exit before proceeding), so for any
# session_id the seq order matches emission order. Across sessions, hook
# invocations may race on the append flock, but they touch disjoint session_id
# state so cross-session interleaving is safe. If Claude Code ever introduces
# concurrent hooks within one session, a source-side ordering field would be
# needed in the payload.
#
# Dismissal semantics: a `SessionEnd` event with payload.synthetic=true marks
# the session as dismissed (e.g. via `cc-cockpit mark-ended`). Any later event
# for that session un-dismisses it, so a still-live session that was matched
# by a stale prefix will come back as soon as it emits anything. Real (non-
# synthetic) SessionEnd remains terminal.
set -u

jq -R -s '
  split("\n")
  | map(select(length > 0)) as $lines
  | ($lines | map(fromjson?) | map(select(. != null))) as $parsed
  | ($parsed
      | map(select(
          (type == "object")
          and ((.seq | type) == "number")
          and ((.wall_clock_iso8601 | type) == "string"
               and (.wall_clock_iso8601 | length) > 0
               and ((try (.wall_clock_iso8601 | fromdateiso8601) catch null) != null))
          and ((.event_type | type) == "string" and (.event_type | length) > 0)
          and ((.session_id | type) == "string" and (.session_id | length) > 0)
          and ((has("payload") | not) or (.payload == null) or ((.payload | type) == "object"))
        ))
      | map(.payload = (if has("payload") and .payload != null then .payload else {} end))
    ) as $good
  | ($lines | length) as $total
  | ($good
      | sort_by(.seq)
      | reduce .[] as $e (
          {sessions: {}, last_seq: 0};
          .last_seq = ($e.seq // .last_seq)
          # Pre-revive: if the session was dismissed (synthetic SessionEnd) and
          # any non-SessionEnd event arrives, un-end it before applying that
          # event. The event-specific branch below then sets the correct status.
          | (if (.sessions[$e.session_id] // null) != null
                and (.sessions[$e.session_id].dismissed // false) == true
                and $e.event_type != "SessionEnd" then
               .sessions[$e.session_id] |= (
                 .status = "running"
                 | .dismissed = false
                 | .revived_at = $e.wall_clock_iso8601
                 | .ended_at = null
               )
             else . end)
          | if $e.event_type == "SessionStart" then
              if .sessions[$e.session_id] then
                .sessions[$e.session_id] |= (
                  .last_activity = $e.wall_clock_iso8601
                  | .resumed_at //= $e.wall_clock_iso8601
                )
              else
                .sessions[$e.session_id] = {
                  session_id:             $e.session_id,
                  primary_repo:           $e.payload.primary_repo,
                  declared_related_repos: $e.payload.declared_related_repos,
                  task_name:              $e.payload.task_name,
                  cwd:                    $e.payload.cwd,
                  zellij_pane_id:         $e.payload.zellij_pane_id,
                  status:                 "running",
                  started_at:             $e.wall_clock_iso8601,
                  last_activity:          $e.wall_clock_iso8601,
                  last_prompt_preview:    null
                }
              end
            elif $e.event_type == "UserPromptSubmit" then
              if (.sessions[$e.session_id] and .sessions[$e.session_id].status != "ended") then
                .sessions[$e.session_id] |= (
                  .status = "running"
                  | .last_activity = $e.wall_clock_iso8601
                  | .last_prompt_preview = $e.payload.prompt_preview
                )
              else . end
            elif ($e.event_type == "PermissionRequest" or $e.event_type == "Notification") then
              if (.sessions[$e.session_id] and .sessions[$e.session_id].status != "ended") then
                .sessions[$e.session_id] |= (
                  .status = "waiting_input"
                  | .last_activity = $e.wall_clock_iso8601
                )
              else . end
            elif $e.event_type == "PostToolUse" then
              if (.sessions[$e.session_id] and .sessions[$e.session_id].status == "waiting_input") then
                .sessions[$e.session_id] |= (
                  .status = "running"
                  | .last_activity = $e.wall_clock_iso8601
                )
              else
                if .sessions[$e.session_id] then
                  .sessions[$e.session_id].last_activity = $e.wall_clock_iso8601
                else . end
              end
            elif $e.event_type == "Stop" then
              if (.sessions[$e.session_id] and .sessions[$e.session_id].status != "ended") then
                .sessions[$e.session_id] |= (
                  .status = "idle"
                  | .last_activity = $e.wall_clock_iso8601
                )
              else . end
            elif $e.event_type == "SessionEnd" then
              if .sessions[$e.session_id] then
                .sessions[$e.session_id] |= (
                  .status = "ended"
                  | .ended_at = $e.wall_clock_iso8601
                  | .last_activity = $e.wall_clock_iso8601
                  | .dismissed = (($e.payload.synthetic // false) == true)
                )
              else . end
            else . end
      )
  )
  | . + { dropped_events: ($total - ($good | length)) }
'
