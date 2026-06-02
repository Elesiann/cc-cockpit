package hook

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBuild_SessionStart_PullsFromPayload(t *testing.T) {
	payload := map[string]any{
		"cwd":             "/repos/api",
		"source":          "startup",
		"model":           "claude-opus",
		"transcript_path": "/home/gio/.claude/projects/api/sid1.jsonl",
	}
	got := Build("SessionStart", "sid1", payload)
	if got == nil {
		t.Fatalf("SessionStart should produce an event")
	}
	p := got["payload"].(map[string]any)
	if p["cwd"] != "/repos/api" {
		t.Errorf("cwd: got %v, want /repos/api", p["cwd"])
	}
	if p["transcript_path"] != "/home/gio/.claude/projects/api/sid1.jsonl" {
		t.Errorf("transcript_path: got %v", p["transcript_path"])
	}
}

func TestBuild_UserPromptSubmit_TruncatesAt80Runes(t *testing.T) {
	// The cap is rune-based so a 200-ASCII-char input truncates to 80
	// runes (= 80 bytes for ASCII) and a 200-rune multibyte input
	// truncates to 80 runes with byte length < 200.
	longASCII := strings.Repeat("a", 200)
	got := Build("UserPromptSubmit", "sid", map[string]any{"prompt": longASCII})
	p := got["payload"].(map[string]any)
	preview, _ := p["prompt_preview"].(string)
	if got := utf8.RuneCountInString(preview); got != 80 {
		t.Errorf("ASCII preview rune count: got %d, want 80", got)
	}
	if !utf8.ValidString(preview) {
		t.Errorf("ASCII preview must be valid UTF-8")
	}
}

func TestBuild_UserPromptSubmit_NewlinesBecomeSpaces(t *testing.T) {
	got := Build("UserPromptSubmit", "sid", map[string]any{"prompt": "line1\nline2\nline3"})
	p := got["payload"].(map[string]any)
	if p["prompt_preview"] != "line1 line2 line3" {
		t.Errorf("prompt_preview: got %q, want 'line1 line2 line3'", p["prompt_preview"])
	}
}

func TestBuild_Notification_FiltersUnknownTypes(t *testing.T) {
	cases := map[string]bool{
		"idle_prompt":       true,
		"permission_prompt": true,
		"some_other_type":   false,
		"":                  false,
	}
	for ntype, accept := range cases {
		got := Build("Notification", "sid", map[string]any{"notification_type": ntype})
		if accept && got == nil {
			t.Errorf("notification_type %q should be accepted", ntype)
		}
		if !accept && got != nil {
			t.Errorf("notification_type %q should be dropped, got %#v", ntype, got)
		}
	}
}

func TestBuild_PreToolUse_CapturesToolName(t *testing.T) {
	got := Build("PreToolUse", "sid", map[string]any{"tool_name": "Bash"})
	if got == nil {
		t.Fatalf("PreToolUse should not be dropped")
	}
	if got["event_type"] != "PreToolUse" {
		t.Errorf("event_type: got %v, want PreToolUse", got["event_type"])
	}
	p := got["payload"].(map[string]any)
	if p["tool_name"] != "Bash" {
		t.Errorf("tool_name: got %v, want Bash", p["tool_name"])
	}
	// Unlike PostToolUse, PreToolUse doesn't carry a success flag — the call
	// hasn't completed yet, so there's nothing to report on.
	if _, hasSuccess := p["success"]; hasSuccess {
		t.Errorf("PreToolUse payload must not include success: got %#v", p)
	}
}

func TestBuild_PostToolUse_AlwaysSuccessTrue(t *testing.T) {
	// Bash hook hardcodes success:true; the reducer doesn't read it but the
	// event shape stays compatible.
	got := Build("PostToolUse", "sid", map[string]any{"tool_name": "Read"})
	p := got["payload"].(map[string]any)
	if p["tool_name"] != "Read" {
		t.Errorf("tool_name: got %v, want Read", p["tool_name"])
	}
	if p["success"] != true {
		t.Errorf("success: got %v, want true", p["success"])
	}
}

func TestBuild_UnknownEventDropped(t *testing.T) {
	if got := Build("Unknown", "sid", nil); got != nil {
		t.Errorf("unknown event should be dropped, got %#v", got)
	}
}

func TestBuild_StopAndSessionEndEmptyPayload(t *testing.T) {
	for _, ev := range []string{"Stop", "SessionEnd"} {
		got := Build(ev, "sid", map[string]any{"unrelated": "junk"})
		p := got["payload"].(map[string]any)
		if len(p) != 0 {
			t.Errorf("%s should have empty payload, got %#v", ev, p)
		}
	}
}
