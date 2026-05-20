// Package install handles `cc-cockpit install`: symlinks the binary onto
// PATH and merges the cc-cockpit hooks into ~/.claude/settings.json.
package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/elesiann/cc-cockpit/internal/state"
)

// Events is the list of Claude Code hooks cc-cockpit subscribes to.
var Events = []string{
	state.EventSessionStart,
	state.EventUserPromptSubmit,
	state.EventPermissionRequest,
	state.EventNotification,
	state.EventPreToolUse,
	state.EventPostToolUse,
	state.EventStop,
	state.EventSessionEnd,
}

// MergeHooks idempotently installs cc-cockpit hooks into a Claude settings
// document. Entries whose .hooks[].command contains "cc-cockpit hook " are
// replaced; everything else is preserved. Empty input is treated as {}.
func MergeHooks(existing []byte, binPath string) ([]byte, error) {
	top := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &top); err != nil {
			return nil, err
		}
	}

	hooksAny, _ := top["hooks"].(map[string]any)
	if hooksAny == nil {
		hooksAny = map[string]any{}
	}

	for _, ev := range Events {
		existingEntries, _ := hooksAny[ev].([]any)
		var kept []any
		for _, e := range existingEntries {
			if !entryHasCockpitHook(e) {
				kept = append(kept, e)
			}
		}

		entry := map[string]any{
			"hooks": []any{
				map[string]any{"type": "command", "command": binPath + " hook " + ev},
			},
		}
		if ev == state.EventNotification {
			entry["matcher"] = "idle_prompt|permission_prompt"
		}
		hooksAny[ev] = append(kept, entry)
	}
	top["hooks"] = hooksAny

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(top); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// EntryHasCockpitHook reports whether a settings hook-entry contains a
// cc-cockpit hook command. Exposed so doctor can reuse it.
func EntryHasCockpitHook(e any) bool { return entryHasCockpitHook(e) }

func entryHasCockpitHook(e any) bool {
	cmds := cockpitHookCommands(e)
	return len(cmds) > 0
}

func cockpitHookCommands(e any) []string {
	entry, _ := e.(map[string]any)
	if entry == nil {
		return nil
	}
	hooks, _ := entry["hooks"].([]any)
	var cmds []string
	for _, h := range hooks {
		hMap, _ := h.(map[string]any)
		cmd, _ := hMap["command"].(string)
		if strings.Contains(cmd, "cc-cockpit hook ") {
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}

// EntryHasMatcher reports whether a hook entry uses the given matcher AND
// has a cc-cockpit command. Used by doctor to verify the Notification matcher.
func EntryHasMatcher(e any, matcher string) bool {
	entry, _ := e.(map[string]any)
	if entry == nil {
		return false
	}
	if m, _ := entry["matcher"].(string); m != matcher {
		return false
	}
	return entryHasCockpitHook(e)
}

// InstallHooks merges cc-cockpit hooks into settingsPath (creating it if
// missing). Backs up the existing file as .bak-<ts> when it would change;
// no-op if the merge result equals the existing content.
func InstallHooks(settingsPath, binPath string) error {
	dir := filepath.Dir(settingsPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create settings dir %q: %w", dir, err)
	}

	var existing []byte
	if data, err := os.ReadFile(settingsPath); err == nil {
		existing = data
	}

	merged, err := MergeHooks(existing, binPath)
	if err != nil {
		return fmt.Errorf("merge: %w", err)
	}

	if existing != nil && bytes.Equal(existing, merged) {
		return nil
	}

	if existing != nil {
		ts := time.Now().Format("20060102-150405")
		if err := os.WriteFile(settingsPath+".bak-"+ts, existing, 0o644); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
	}

	tmp := settingsPath + ".tmp"
	if err := os.WriteFile(tmp, merged, 0o644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	return os.Rename(tmp, settingsPath)
}

// HooksInstalled reports whether settingsData contains a cc-cockpit hook
// for every event in Events AND the Notification entry uses the expected
// "idle_prompt|permission_prompt" matcher. Empty/missing settings → false.
// JSON parse errors propagate. Read-only: never mutates input.
func HooksInstalled(settingsData []byte) (bool, error) {
	if len(bytes.TrimSpace(settingsData)) == 0 {
		return false, nil
	}
	var top struct {
		Hooks map[string][]any `json:"hooks"`
	}
	if err := json.Unmarshal(settingsData, &top); err != nil {
		return false, err
	}
	for _, ev := range Events {
		found := false
		for _, e := range top.Hooks[ev] {
			if cockpitHookEntryUsable(e) {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}
	for _, e := range top.Hooks[state.EventNotification] {
		if EntryHasMatcher(e, "idle_prompt|permission_prompt") && cockpitHookEntryUsable(e) {
			return true, nil
		}
	}
	return false, nil
}

func cockpitHookEntryUsable(e any) bool {
	for _, cmd := range cockpitHookCommands(e) {
		if hookCommandBinaryUsable(cmd) {
			return true
		}
	}
	return false
}

func hookCommandBinaryUsable(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	bin := fields[0]
	if filepath.IsAbs(bin) {
		st, err := os.Stat(bin)
		return err == nil && !st.IsDir() && st.Mode()&0o111 != 0
	}
	_, err := exec.LookPath(bin)
	return err == nil
}

// EnsureHooks installs cc-cockpit's Claude Code hooks if HooksInstalled
// is false. Silent no-op when already correct. Resolves settingsPath from
// CLAUDE_SETTINGS_PATH / ~/.claude/settings.json when empty.
//
// Designed for `cc-cockpit open` to self-bootstrap; does NOT touch the
// binary symlink (whoever is calling is already on PATH).
func EnsureHooks(settingsPath string) error {
	if settingsPath == "" {
		if p := os.Getenv("CLAUDE_SETTINGS_PATH"); p != "" {
			settingsPath = p
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return fmt.Errorf("home dir: %w", err)
			}
			settingsPath = filepath.Join(home, ".claude", "settings.json")
		}
	}
	data, _ := os.ReadFile(settingsPath) // missing file is fine; InstallHooks creates it
	ok, err := HooksInstalled(data)
	if err != nil {
		return fmt.Errorf("settings.json invalid: %w", err)
	}
	if ok {
		return nil
	}
	selfPath, err := hookBinaryPath()
	if err != nil {
		return fmt.Errorf("locate binary: %w", err)
	}
	return InstallHooks(settingsPath, selfPath)
}

func hookBinaryPath() (string, error) {
	if p, err := exec.LookPath("cc-cockpit"); err == nil && p != "" {
		if abs, err := filepath.Abs(p); err == nil {
			return abs, nil
		}
		return p, nil
	}
	selfPath, err := os.Executable()
	if err != nil {
		return "", err
	}
	if real, err := filepath.EvalSymlinks(selfPath); err == nil {
		selfPath = real
	}
	return selfPath, nil
}

// InstallBin symlinks binDir/cc-cockpit -> selfPath. No-op if the symlink
// already points there; otherwise replaces whatever's there.
func InstallBin(binDir, selfPath string) error {
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return fmt.Errorf("cannot create bin dir %q: %w", binDir, err)
	}
	target := filepath.Join(binDir, "cc-cockpit")
	if existing, err := os.Readlink(target); err == nil && existing == selfPath {
		return nil
	}
	_ = os.Remove(target)
	if err := os.Symlink(selfPath, target); err != nil {
		return fmt.Errorf("symlink %q -> %q: %w", target, selfPath, err)
	}
	return nil
}
