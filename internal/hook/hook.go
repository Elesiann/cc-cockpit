// Package hook builds cc-cockpit event envelopes from Claude Code hook
// payloads.
//
// The Build function is a pure mapping from (event name, payload, env) to
// the event the reducer expects. The hook subcommand is a thin wrapper that
// reads stdin, calls Build, and appends via state.Append. Keeping Build pure
// makes it testable without touching files or env.
package hook

import (
	"strings"
)

// Env carries the COCKPIT_* + ZELLIJ_PANE_ID environment values that the
// SessionStart event embeds. Pass these explicitly so Build is pure.
type Env struct {
	PrimaryRepo          string
	DeclaredRelatedRepos string // raw COCKPIT_DECLARED_RELATED_REPOS, comma-separated
	TaskName             string
	ZellijPaneID         string
}

// Build returns the event envelope to append for (event, payload, env), or
// nil if the hook should be silently dropped (e.g. a Notification whose type
// isn't idle_prompt / permission_prompt, or an unknown event name).
//
// Output shape mirrors cmd_hook in the bash binary so the reducer treats Go-
// and bash-emitted events identically.
func Build(event, sessionID string, payload map[string]any, env Env) map[string]any {
	switch event {
	case "SessionStart":
		var zpane any
		if env.ZellijPaneID != "" {
			zpane = env.ZellijPaneID
		}
		return envelope(event, sessionID, map[string]any{
			"primary_repo":           env.PrimaryRepo,
			"declared_related_repos": splitCSV(env.DeclaredRelatedRepos),
			"task_name":              env.TaskName,
			"cwd":                    payload["cwd"],
			"zellij_pane_id":         zpane,
			"source":                 payload["source"],
			"model":                  payload["model"],
		})

	case "UserPromptSubmit":
		prompt, _ := payload["prompt"].(string)
		prompt = strings.ReplaceAll(prompt, "\n", " ")
		if len(prompt) > 80 {
			prompt = prompt[:80]
		}
		return envelope(event, sessionID, map[string]any{"prompt_preview": prompt})

	case "PermissionRequest":
		return envelope(event, sessionID, map[string]any{})

	case "Notification":
		ntype, _ := payload["notification_type"].(string)
		if ntype != "idle_prompt" && ntype != "permission_prompt" {
			return nil
		}
		return envelope(event, sessionID, map[string]any{"notification_type": ntype})

	case "PostToolUse":
		tool, _ := payload["tool_name"].(string)
		return envelope(event, sessionID, map[string]any{"tool_name": tool, "success": true})

	case "Stop":
		return envelope(event, sessionID, map[string]any{})

	case "SessionEnd":
		return envelope(event, sessionID, map[string]any{})
	}
	return nil
}

func envelope(event, sessionID string, payload map[string]any) map[string]any {
	return map[string]any{
		"event_type": event,
		"session_id": sessionID,
		"payload":    payload,
	}
}

// splitCSV splits a comma-separated env-var value, dropping empty parts.
// "" → []string{}, "a" → ["a"], "a,b,," → ["a", "b"]. Always returns a
// non-nil slice so JSON emits [] not null.
func splitCSV(s string) []string {
	out := []string{}
	if s == "" {
		return out
	}
	for _, p := range strings.Split(s, ",") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
