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
//
// Returns an error if the document is shaped in a way that doesn't match the
// Claude Code schema (hooks not an object, or a per-event entry not an array).
// Silently coercing those would destroy user data the merger can't represent.
func MergeHooks(existing []byte, binPath string) ([]byte, error) {
	top := map[string]any{}
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &top); err != nil {
			return nil, err
		}
	}

	var hooksAny map[string]any
	if existingHooks, present := top["hooks"]; present {
		m, ok := existingHooks.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("settings.hooks must be a JSON object, got %T — refusing to overwrite. Fix the file or remove the .hooks key", existingHooks)
		}
		hooksAny = m
	} else {
		hooksAny = map[string]any{}
	}

	for _, ev := range Events {
		var existingEntries []any
		if raw, present := hooksAny[ev]; present {
			arr, ok := raw.([]any)
			if !ok {
				return nil, fmt.Errorf("settings.hooks.%s must be a JSON array, got %T — refusing to overwrite. Fix the file or remove the .hooks.%s key", ev, raw, ev)
			}
			existingEntries = arr
		}
		var kept []any
		for _, e := range existingEntries {
			if !entryHasCockpitHook(e) {
				kept = append(kept, e)
			}
		}

		entry := map[string]any{
			"hooks": []any{
				map[string]any{"type": "command", "command": hookCommand(binPath, ev)},
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
		if isCockpitHookCommand(cmd) {
			cmds = append(cmds, cmd)
		}
	}
	return cmds
}

func hookCommand(binPath, event string) string {
	return shellQuoteArg(binPath) + " hook " + event
}

func shellQuoteArg(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n'\"\\$`!&|;()<>{}[]*?") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func isCockpitHookCommand(cmd string) bool {
	fields, ok := splitHookCommandFields(cmd)
	if !ok || len(fields) < 3 {
		return false
	}
	return filepath.Base(fields[0]) == "cc-cockpit" && fields[1] == "hook" && fields[2] != ""
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
	fields, ok := splitHookCommandFields(cmd)
	if !ok || len(fields) == 0 {
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

func splitHookCommandFields(s string) ([]string, bool) {
	var fields []string
	var b strings.Builder
	var quote rune
	escaped := false
	inField := false

	for _, r := range s {
		if escaped {
			b.WriteRune(r)
			escaped = false
			inField = true
			continue
		}
		if quote != 0 {
			if r == quote {
				quote = 0
				inField = true
				continue
			}
			if quote == '"' && r == '\\' {
				escaped = true
				continue
			}
			b.WriteRune(r)
			inField = true
			continue
		}

		switch r {
		case '\'', '"':
			quote = r
			inField = true
		case '\\':
			escaped = true
			inField = true
		case ' ', '\t', '\n', '\r':
			if inField {
				fields = append(fields, b.String())
				b.Reset()
				inField = false
			}
		default:
			b.WriteRune(r)
			inField = true
		}
	}
	if escaped {
		b.WriteRune('\\')
	}
	if quote != 0 {
		return nil, false
	}
	if inField {
		fields = append(fields, b.String())
	}
	return fields, true
}

// RemoveHooks strips cc-cockpit's hook entries from a Claude settings
// document, preserving everything else. An event whose entries list becomes
// empty after removal is dropped from .hooks. .hooks itself is dropped when
// no events remain. Returns the new bytes plus a count of removed entries
// (0 means nothing changed; caller should skip writing).
//
// Errors mirror MergeHooks: malformed top-level JSON or non-object hooks /
// non-array event entries refuse rather than coerce, so we never silently
// destroy user data.
func RemoveHooks(existing []byte) (out []byte, removed int, err error) {
	if len(bytes.TrimSpace(existing)) == 0 {
		return existing, 0, nil
	}
	top := map[string]any{}
	if err := json.Unmarshal(existing, &top); err != nil {
		return nil, 0, err
	}
	rawHooks, present := top["hooks"]
	if !present {
		return existing, 0, nil
	}
	hooks, ok := rawHooks.(map[string]any)
	if !ok {
		return nil, 0, fmt.Errorf("settings.hooks must be a JSON object, got %T — refusing to modify", rawHooks)
	}

	for ev, raw := range hooks {
		entries, ok := raw.([]any)
		if !ok {
			return nil, 0, fmt.Errorf("settings.hooks.%s must be a JSON array, got %T — refusing to modify", ev, raw)
		}
		var kept []any
		for _, e := range entries {
			if entryHasCockpitHook(e) {
				removed++
				continue
			}
			kept = append(kept, e)
		}
		if len(kept) == 0 {
			delete(hooks, ev)
		} else {
			hooks[ev] = kept
		}
	}
	if len(hooks) == 0 {
		delete(top, "hooks")
	} else {
		top["hooks"] = hooks
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(top); err != nil {
		return nil, 0, err
	}
	return buf.Bytes(), removed, nil
}

// UninstallHooks removes cc-cockpit's hook entries from settingsPath. No-op
// if the file is missing or contains no cc-cockpit hooks. Backs up the file
// as .bak-<ts> before writing, matching InstallHooks. Returns the number of
// removed entries.
func UninstallHooks(settingsPath string) (int, error) {
	existing, err := os.ReadFile(settingsPath)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	merged, removed, err := RemoveHooks(existing)
	if err != nil {
		return 0, err
	}
	if removed == 0 {
		return 0, nil
	}

	ts := time.Now().Format("20060102-150405")
	if err := os.WriteFile(settingsPath+".bak-"+ts, existing, 0o644); err != nil {
		return 0, fmt.Errorf("backup: %w", err)
	}
	tmp := settingsPath + ".tmp"
	if err := os.WriteFile(tmp, merged, 0o644); err != nil {
		return 0, fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, settingsPath); err != nil {
		return 0, err
	}
	return removed, nil
}

// UninstallBin removes binDir/cc-cockpit if it's a symlink pointing into the
// current binary lineage. Refuses to delete a regular file there (not ours
// to remove). Returns true when it deleted something, false when nothing
// needed deleting, and an error only on permission/IO failures.
func UninstallBin(binDir string) (bool, error) {
	target := filepath.Join(binDir, "cc-cockpit")
	info, err := os.Lstat(target)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if info.Mode()&os.ModeSymlink == 0 {
		// Regular file (or directory). Not ours to remove — `go install` etc.
		// could have written it. Surface the situation rather than guessing.
		return false, fmt.Errorf("%s is not a symlink — refusing to delete (remove it manually if it's a stale install)", target)
	}
	if err := os.Remove(target); err != nil {
		return false, err
	}
	return true, nil
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
	tmp := filepath.Join(binDir, fmt.Sprintf(".cc-cockpit.%d.tmp", os.Getpid()))
	_ = os.Remove(tmp)
	if err := os.Symlink(selfPath, tmp); err != nil {
		return fmt.Errorf("symlink %q -> %q: %w", target, selfPath, err)
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("symlink %q -> %q: %w", target, selfPath, err)
	}
	return nil
}
