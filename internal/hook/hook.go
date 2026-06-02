// Package hook builds cc-cockpit event envelopes from Claude Code hook
// payloads. Build is pure for testability; the wrapping I/O lives in main.go.
package hook

import (
	"strings"

	"github.com/elesiann/cc-cockpit/internal/state"
)

// previewRuneCap bounds the UserPromptSubmit prompt preview. The cap is
// expressed in runes (not bytes) so a multibyte prompt — Japanese, emoji,
// accented Latin — is truncated on a character boundary instead of
// producing invalid UTF-8.
const previewRuneCap = 80

// Build returns the event envelope for (event, payload), or nil if the
// hook should be silently dropped (unknown event, or a Notification whose
// type isn't in the whitelist).
func Build(event, sessionID string, payload map[string]any) map[string]any {
	switch event {
	case state.EventSessionStart:
		return envelope(event, sessionID, map[string]any{
			"cwd":    payload["cwd"],
			"source": payload["source"],
			"model":  payload["model"],
			// transcript_path is on every Claude Code hook payload. We
			// capture it at SessionStart (stable for the session's life)
			// so the recap reader knows which .jsonl to scan.
			"transcript_path": payload["transcript_path"],
		})

	case state.EventUserPromptSubmit:
		prompt, _ := payload["prompt"].(string)
		prompt = strings.ReplaceAll(prompt, "\n", " ")
		if runes := []rune(prompt); len(runes) > previewRuneCap {
			prompt = string(runes[:previewRuneCap])
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

	case state.EventPreToolUse:
		tool, _ := payload["tool_name"].(string)
		return envelope(event, sessionID, map[string]any{"tool_name": tool})

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
