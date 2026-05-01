// Package hook builds cc-cockpit event envelopes from Claude Code hook
// payloads. Build is pure for testability; the wrapping I/O lives in main.go.
package hook

import (
	"strings"

	"github.com/elesiann/cc-cockpit/internal/state"
)

// Env carries COCKPIT_* + ZELLIJ_PANE_ID values that SessionStart embeds.
type Env struct {
	PrimaryRepo          string
	DeclaredRelatedRepos string // raw, comma-separated
	TaskName             string
	ZellijPaneID         string
}

// Build returns the event envelope for (event, payload, env), or nil if the
// hook should be silently dropped (unknown event, or a Notification whose
// type isn't in the whitelist).
func Build(event, sessionID string, payload map[string]any, env Env) map[string]any {
	switch event {
	case state.EventSessionStart:
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

	case state.EventUserPromptSubmit:
		prompt, _ := payload["prompt"].(string)
		prompt = strings.ReplaceAll(prompt, "\n", " ")
		if len(prompt) > 80 {
			prompt = prompt[:80]
		}
		return envelope(event, sessionID, map[string]any{"prompt_preview": prompt})

	case state.EventPermissionRequest, state.EventStop, state.EventSessionEnd:
		return envelope(event, sessionID, map[string]any{})

	case state.EventNotification:
		ntype, _ := payload["notification_type"].(string)
		if ntype != "idle_prompt" && ntype != "permission_prompt" {
			return nil
		}
		return envelope(event, sessionID, map[string]any{"notification_type": ntype})

	case state.EventPostToolUse:
		tool, _ := payload["tool_name"].(string)
		return envelope(event, sessionID, map[string]any{"tool_name": tool, "success": true})
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

// splitCSV drops empty parts and always returns a non-nil slice so JSON
// emits [] rather than null.
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
