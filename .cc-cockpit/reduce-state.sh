#!/bin/bash
# reduce-state.sh — consume events.jsonl from stdin, emit current.json on stdout.
# MVP: no RepoDiscovered / CwdChanged / TaskRenamed branches.
set -u

jq -s '
  sort_by(.seq)
  | reduce .[] as $e (
      {sessions: {}, last_seq: 0};
      .last_seq = $e.seq
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
            .sessions[$e.session_id].status = "ended"
            | .sessions[$e.session_id].ended_at //= $e.wall_clock_iso8601
            | .sessions[$e.session_id].last_activity = $e.wall_clock_iso8601
          else . end
        else . end
    )
'
