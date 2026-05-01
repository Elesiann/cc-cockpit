// Package install handles `cc-cockpit install`: symlinks the binary onto
// PATH and idempotently merges the cc-cockpit hooks into ~/.claude/settings.json.
package install

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Events is the canonical list of Claude Code hooks cc-cockpit subscribes to.
var Events = []string{
	"SessionStart",
	"UserPromptSubmit",
	"PermissionRequest",
	"Notification",
	"PostToolUse",
	"Stop",
	"SessionEnd",
}

// MergeHooks idempotently installs cc-cockpit hooks into a Claude settings
// document. Existing entries whose .hooks[].command contains "cc-cockpit hook "
// are replaced; everything else is preserved. Returns the new bytes.
//
// existing may be empty (treated as {}).
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
		if ev == "Notification" {
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

// entryHasCockpitHook reports whether a settings hook-entry already contains
// a cc-cockpit hook command (matches the bash version's substring check).
func entryHasCockpitHook(e any) bool {
	entry, _ := e.(map[string]any)
	if entry == nil {
		return false
	}
	hooks, _ := entry["hooks"].([]any)
	for _, h := range hooks {
		hMap, _ := h.(map[string]any)
		cmd, _ := hMap["command"].(string)
		if strings.Contains(cmd, "cc-cockpit hook ") {
			return true
		}
	}
	return false
}

// InstallHooks reads settingsPath, merges cc-cockpit hooks pointing at binPath,
// backs up the existing file (`<path>.bak-<ts>`) if it would change, and
// writes the new settings atomically. No-op if nothing would change.
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

// InstallBin places binDir/cc-cockpit pointing at selfPath. If a symlink to
// the same target already exists, no-op. Existing other files / symlinks are
// replaced.
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
