package install

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elesiann/cc-cockpit/internal/state"
)

func TestMergeHooks_FromEmptySettings(t *testing.T) {
	out, err := MergeHooks(nil, "/usr/local/bin/cc-cockpit")
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	if err := json.Unmarshal(out, &top); err != nil {
		t.Fatal(err)
	}
	hooks := top["hooks"].(map[string]any)
	for _, ev := range Events {
		entries, ok := hooks[ev].([]any)
		if !ok || len(entries) == 0 {
			t.Errorf("event %s missing in merged output", ev)
		}
	}
	// Notification gets the matcher.
	notifEntry := hooks["Notification"].([]any)[0].(map[string]any)
	if notifEntry["matcher"] != "idle_prompt|permission_prompt" {
		t.Errorf("Notification matcher missing or wrong: %v", notifEntry["matcher"])
	}
}

func TestMergeHooks_Idempotent(t *testing.T) {
	out1, _ := MergeHooks(nil, "/bin/cc-cockpit")
	out2, _ := MergeHooks(out1, "/bin/cc-cockpit")
	if string(out1) != string(out2) {
		t.Errorf("merge not idempotent: out1 != out2")
	}
}

func TestMergeHooks_PreservesUnrelatedHookEntries(t *testing.T) {
	existing := []byte(`{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"/usr/bin/echo keep"}]}]}}`)
	out, err := MergeHooks(existing, "/bin/cc-cockpit")
	if err != nil {
		t.Fatal(err)
	}
	var top map[string]any
	json.Unmarshal(out, &top)
	stop := top["hooks"].(map[string]any)["Stop"].([]any)
	// Should have the user's existing entry PLUS the cc-cockpit one.
	if len(stop) < 2 {
		t.Errorf("unrelated Stop hook dropped, got %d entries", len(stop))
	}
	keepFound := false
	for _, e := range stop {
		hooks := e.(map[string]any)["hooks"].([]any)
		for _, h := range hooks {
			cmd, _ := h.(map[string]any)["command"].(string)
			if strings.Contains(cmd, "echo keep") {
				keepFound = true
			}
		}
	}
	if !keepFound {
		t.Errorf("user's 'echo keep' hook was removed during merge")
	}
}

func TestMergeHooks_ReplacesExistingCockpitHook(t *testing.T) {
	// Old install pointed at /old/cc-cockpit; merge with /new/cc-cockpit.
	// The old entry should be removed, the new one added.
	existing := []byte(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"/old/cc-cockpit hook SessionStart"}]}]}}`)
	out, err := MergeHooks(existing, "/new/cc-cockpit")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "/old/cc-cockpit hook SessionStart") {
		t.Errorf("old cc-cockpit entry not replaced: %s", string(out))
	}
	if !strings.Contains(string(out), "/new/cc-cockpit hook SessionStart") {
		t.Errorf("new cc-cockpit entry missing: %s", string(out))
	}
}

func TestMergeHooks_ReplacesQuotedExistingCockpitHook(t *testing.T) {
	existing := []byte(`{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"'/old dir/cc-cockpit' hook SessionStart"}]}]}}`)
	out, err := MergeHooks(existing, "/new/cc-cockpit")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "/old dir/cc-cockpit") {
		t.Errorf("old quoted cc-cockpit entry not replaced: %s", string(out))
	}
}

func TestMergeHooks_QuotesBinPathWithSpaces(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "bin dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "cc-cockpit")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := MergeHooks(nil, bin)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "'"+bin+"' hook SessionStart") {
		t.Fatalf("expected quoted hook command for spaced path, got:\n%s", out)
	}
	ok, err := HooksInstalled(out)
	if err != nil || !ok {
		t.Fatalf("HooksInstalled should accept quoted hook command: ok=%v err=%v", ok, err)
	}
}

func TestMergeHooks_PreservesTopLevelKeys(t *testing.T) {
	existing := []byte(`{"theme":"dark","permissions":{"allow":["Bash"]},"hooks":{}}`)
	out, _ := MergeHooks(existing, "/bin/cc-cockpit")
	var top map[string]any
	json.Unmarshal(out, &top)
	if top["theme"] != "dark" {
		t.Errorf("top-level 'theme' lost: got %v", top["theme"])
	}
	if _, ok := top["permissions"]; !ok {
		t.Errorf("top-level 'permissions' lost")
	}
}

func TestMergeHooks_RejectsInvalidJSON(t *testing.T) {
	if _, err := MergeHooks([]byte(`{not json`), "/bin/cc-cockpit"); err == nil {
		t.Errorf("expected error on invalid JSON")
	}
}

func TestMergeHooks_RefusesNonObjectHooksField(t *testing.T) {
	// User's settings.json had `hooks` as an array (or some other non-object
	// shape). Previously we silently dropped that value and wrote our object,
	// destroying user data. Now: return an error so the user can fix it.
	cases := []string{
		`{"hooks":["array","not","object"]}`,
		`{"hooks":"a string"}`,
		`{"hooks":42}`,
	}
	for _, input := range cases {
		out, err := MergeHooks([]byte(input), "/bin/cc-cockpit")
		if err == nil {
			t.Errorf("input %q: expected error, got merged output:\n%s", input, out)
			continue
		}
		if !strings.Contains(err.Error(), "settings.hooks must be a JSON object") {
			t.Errorf("input %q: error should explain the constraint, got: %v", input, err)
		}
	}
}

func TestMergeHooks_RefusesNonArrayEventEntry(t *testing.T) {
	// settings.hooks is an object (valid) but a specific event is bound to a
	// non-array value. The previous code silently overwrote it.
	input := `{"hooks":{"SessionStart":"not an array"}}`
	out, err := MergeHooks([]byte(input), "/bin/cc-cockpit")
	if err == nil {
		t.Fatalf("expected error, got merged output:\n%s", out)
	}
	if !strings.Contains(err.Error(), "SessionStart") {
		t.Errorf("error should name the offending event, got: %v", err)
	}
}

func TestEvents_IncludesBothToolUseHooks(t *testing.T) {
	// Anchor: cc-cockpit's granular state machine (running / processing)
	// depends on both PreToolUse and PostToolUse being installed in the
	// Claude Code settings. If anyone trims Events, the dashboard silently
	// drops to coarser states — this test prevents that regression.
	var hasPre, hasPost bool
	for _, ev := range Events {
		switch ev {
		case state.EventPreToolUse:
			hasPre = true
		case state.EventPostToolUse:
			hasPost = true
		}
	}
	if !hasPre {
		t.Errorf("Events missing PreToolUse — granular `running` state requires it")
	}
	if !hasPost {
		t.Errorf("Events missing PostToolUse — `processing` state requires it")
	}
}

func TestHooksInstalled_EmptyReturnsFalse(t *testing.T) {
	ok, err := HooksInstalled(nil)
	if err != nil || ok {
		t.Errorf("empty data: got (ok=%v, err=%v), want (false, nil)", ok, err)
	}
	ok, err = HooksInstalled([]byte("   \n  "))
	if err != nil || ok {
		t.Errorf("whitespace data: got (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

func TestHooksInstalled_FullMergeReturnsTrue(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "cc-cockpit")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	merged, err := MergeHooks(nil, bin)
	if err != nil {
		t.Fatal(err)
	}
	ok, err := HooksInstalled(merged)
	if err != nil || !ok {
		t.Errorf("expected (true, nil) after MergeHooks, got (ok=%v, err=%v)", ok, err)
	}
}

func TestHooksInstalled_DeadCockpitPathReturnsFalse(t *testing.T) {
	merged, err := MergeHooks(nil, "/tmp/definitely-missing-cc-cockpit/cc-cockpit")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := HooksInstalled(merged)
	if err != nil || ok {
		t.Errorf("dead hook binary: got (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

func TestInstallBin_PreservesExistingTargetWhenReplacementFails(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "cc-cockpit")
	original := []byte("#!/bin/sh\nexit 42\n")
	if err := os.WriteFile(target, original, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := InstallBin(dir, "bad\x00target"); err == nil {
		t.Fatalf("InstallBin should fail for invalid symlink target")
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("existing target was changed on failure: got %q want %q", got, original)
	}
}

func TestHooksInstalled_MissingOneEventReturnsFalse(t *testing.T) {
	merged, _ := MergeHooks(nil, "/bin/cc-cockpit")
	// Strip SessionEnd hooks back out.
	var top map[string]any
	if err := json.Unmarshal(merged, &top); err != nil {
		t.Fatal(err)
	}
	hooks := top["hooks"].(map[string]any)
	delete(hooks, "SessionEnd")
	stripped, _ := json.Marshal(top)
	ok, err := HooksInstalled(stripped)
	if err != nil || ok {
		t.Errorf("missing SessionEnd: got (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

func TestHooksInstalled_MissingMatcherReturnsFalse(t *testing.T) {
	// Hand-craft settings: every event has a cockpit hook, but Notification
	// lacks the matcher field (or has the wrong one).
	settings := `{"hooks":{`
	for i, ev := range Events {
		if i > 0 {
			settings += ","
		}
		settings += `"` + ev + `":[{"hooks":[{"type":"command","command":"/bin/cc-cockpit hook ` + ev + `"}]}]`
	}
	settings += `}}`
	ok, err := HooksInstalled([]byte(settings))
	if err != nil || ok {
		t.Errorf("no Notification matcher: got (ok=%v, err=%v), want (false, nil)", ok, err)
	}
}

func TestHooksInstalled_InvalidJSONReturnsError(t *testing.T) {
	ok, err := HooksInstalled([]byte(`{not json`))
	if err == nil || ok {
		t.Errorf("invalid JSON: got (ok=%v, err=%v), want (false, <error>)", ok, err)
	}
}
