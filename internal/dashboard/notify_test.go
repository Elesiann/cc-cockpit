package dashboard

import "testing"

func TestDetectWSLFromContent(t *testing.T) {
	cases := []struct {
		name    string
		content string
		want    bool
	}{
		{"plain linux", "Linux version 6.10 (gcc)", false},
		{"WSL2 microsoft tag", "Linux version 5.15.146.1-microsoft-standard-WSL2", true},
		{"WSL lowercase", "Linux version 5.15 wsl2-foo", true},
		{"empty", "", false},
	}
	for _, c := range cases {
		got := detectWSLFromContent(c.content)
		if got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestEscapeAppleScript(t *testing.T) {
	if got := escapeAppleScript(`he said "hi"`); got != `he said \"hi\"` {
		t.Errorf("quotes: got %q", got)
	}
	if got := escapeAppleScript("line1\nline2"); got != "line1 line2" {
		t.Errorf("newline: got %q", got)
	}
	if got := escapeAppleScript(`back\slash`); got != `back\\slash` {
		t.Errorf("backslash: got %q", got)
	}
}

func TestEscapePowerShell(t *testing.T) {
	if got := escapePowerShell(`it's`); got != `it''s` {
		t.Errorf("quote: got %q", got)
	}
	if got := escapePowerShell("a\nb"); got != "a b" {
		t.Errorf("newline: got %q", got)
	}
}

func TestBuildNotifyMessage(t *testing.T) {
	cases := []struct {
		count int
		repo  string
		task  string
		want  string
	}{
		{1, "api", "fix auth", "api · fix auth waiting for input"},
		{1, "api", "", "api waiting for input"},
		{1, "", "", "session waiting for input"},
		{3, "api", "fix", "3 sessions waiting for input"},
	}
	for _, c := range cases {
		got := buildNotifyMessage(c.count, c.repo, c.task)
		if got != c.want {
			t.Errorf("buildNotifyMessage(%d,%q,%q): got %q want %q",
				c.count, c.repo, c.task, got, c.want)
		}
	}
}

func TestResolveNotifier_NeverPanics(t *testing.T) {
	// This is platform-dependent; the contract under test is "returns a
	// notifier whose Notify method is safe to call no matter what's on PATH."
	n := resolveNotifier()
	n.Notify("test message — should not crash") // no panic, no goroutine leak
	if n.describe == "" {
		t.Errorf("describe must be set even when no backend available")
	}
}
