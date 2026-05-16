package install

import (
	"encoding/json"
	"strings"
	"testing"
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
	merged, err := MergeHooks(nil, "/usr/local/bin/cc-cockpit")
	if err != nil {
		t.Fatal(err)
	}
	ok, err := HooksInstalled(merged)
	if err != nil || !ok {
		t.Errorf("expected (true, nil) after MergeHooks, got (ok=%v, err=%v)", ok, err)
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
