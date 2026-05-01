package hook

import (
	"reflect"
	"strings"
	"testing"
)

func TestBuild_SessionStart_PullsFromEnvAndPayload(t *testing.T) {
	env := Env{
		PrimaryRepo:          "api",
		DeclaredRelatedRepos: "web,infra",
		TaskName:             "fix bug",
		ZellijPaneID:         "%42",
	}
	payload := map[string]any{
		"cwd":    "/repos/api",
		"source": "startup",
		"model":  "claude-opus",
	}
	got := Build("SessionStart", "sid1", payload, env)
	if got == nil {
		t.Fatalf("SessionStart should produce an event")
	}
	p := got["payload"].(map[string]any)
	if p["primary_repo"] != "api" {
		t.Errorf("primary_repo: got %v, want api", p["primary_repo"])
	}
	if p["task_name"] != "fix bug" {
		t.Errorf("task_name: got %v, want 'fix bug'", p["task_name"])
	}
	if p["cwd"] != "/repos/api" {
		t.Errorf("cwd: got %v, want /repos/api", p["cwd"])
	}
	if p["zellij_pane_id"] != "%42" {
		t.Errorf("zellij_pane_id: got %v, want %%42", p["zellij_pane_id"])
	}
	related := p["declared_related_repos"].([]string)
	if !reflect.DeepEqual(related, []string{"web", "infra"}) {
		t.Errorf("declared_related_repos: got %v, want [web infra]", related)
	}
}

func TestBuild_SessionStart_EmptyZellijPaneIsNil(t *testing.T) {
	got := Build("SessionStart", "sid", map[string]any{}, Env{})
	p := got["payload"].(map[string]any)
	if p["zellij_pane_id"] != nil {
		t.Errorf("empty ZELLIJ_PANE_ID should map to nil, got %#v", p["zellij_pane_id"])
	}
}

func TestBuild_SessionStart_EmptyRelatedReposIsEmptyArray(t *testing.T) {
	got := Build("SessionStart", "sid", map[string]any{}, Env{})
	p := got["payload"].(map[string]any)
	related := p["declared_related_repos"].([]string)
	if related == nil || len(related) != 0 {
		t.Errorf("empty COCKPIT_DECLARED_RELATED_REPOS should be empty slice, got %#v", related)
	}
}

func TestBuild_UserPromptSubmit_Truncates80Bytes(t *testing.T) {
	long := strings.Repeat("a", 200)
	got := Build("UserPromptSubmit", "sid", map[string]any{"prompt": long}, Env{})
	p := got["payload"].(map[string]any)
	preview := p["prompt_preview"].(string)
	if len(preview) != 80 {
		t.Errorf("prompt_preview length: got %d, want 80", len(preview))
	}
}

func TestBuild_UserPromptSubmit_NewlinesBecomeSpaces(t *testing.T) {
	got := Build("UserPromptSubmit", "sid", map[string]any{"prompt": "line1\nline2\nline3"}, Env{})
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
		got := Build("Notification", "sid", map[string]any{"notification_type": ntype}, Env{})
		if accept && got == nil {
			t.Errorf("notification_type %q should be accepted", ntype)
		}
		if !accept && got != nil {
			t.Errorf("notification_type %q should be dropped, got %#v", ntype, got)
		}
	}
}

func TestBuild_PostToolUse_AlwaysSuccessTrue(t *testing.T) {
	// Bash hook hardcodes success:true; the reducer doesn't read it but the
	// event shape stays compatible.
	got := Build("PostToolUse", "sid", map[string]any{"tool_name": "Read"}, Env{})
	p := got["payload"].(map[string]any)
	if p["tool_name"] != "Read" {
		t.Errorf("tool_name: got %v, want Read", p["tool_name"])
	}
	if p["success"] != true {
		t.Errorf("success: got %v, want true", p["success"])
	}
}

func TestBuild_UnknownEventDropped(t *testing.T) {
	if got := Build("Unknown", "sid", nil, Env{}); got != nil {
		t.Errorf("unknown event should be dropped, got %#v", got)
	}
}

func TestBuild_StopAndSessionEndEmptyPayload(t *testing.T) {
	for _, ev := range []string{"Stop", "SessionEnd"} {
		got := Build(ev, "sid", map[string]any{"unrelated": "junk"}, Env{})
		p := got["payload"].(map[string]any)
		if len(p) != 0 {
			t.Errorf("%s should have empty payload, got %#v", ev, p)
		}
	}
}

func TestSplitCSV(t *testing.T) {
	cases := map[string][]string{
		"":         {},
		"a":        {"a"},
		"a,b":      {"a", "b"},
		"a,b,,c":   {"a", "b", "c"},
		",":        {},
		",,a,,b,,": {"a", "b"},
	}
	for in, want := range cases {
		got := splitCSV(in)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("splitCSV(%q): got %v, want %v", in, got, want)
		}
	}
}
