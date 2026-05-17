package dashboard

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// SessionMeta carries the per-session user-controlled display metadata that
// Claude Code exposes via `/rename` and `/color`. Both are optional —
// missing fields render as empty strings and the renderer falls back to its
// usual defaults.
type SessionMeta struct {
	Name  string // from ~/.claude/sessions/<pid>.json "name"
	Color string // from latest `/color <X>` in ~/.claude/history.jsonl
}

// historyTailBytes bounds how much of history.jsonl we re-scan each tick.
// Color commands are typed rarely (handfuls per week, at worst) so 256 KiB
// of recent history covers months of typical use without parsing the whole
// file (the user's was 1.9 MB at 0.6.5).
const historyTailBytes = 256 * 1024

// LoadSessionMetas builds the sessionId → SessionMeta map for one render
// tick. Reads:
//   - every ~/.claude/sessions/*.json for the `name` field (set by /rename).
//   - the tail of ~/.claude/history.jsonl for the latest /color per sid.
//
// Returns an empty map on I/O errors — the dashboard renders fine without
// metas, this is purely additive polish.
func LoadSessionMetas(homeDir string) map[string]SessionMeta {
	metas := make(map[string]SessionMeta)
	collectSessionNames(filepath.Join(homeDir, ".claude", "sessions"), metas)
	collectHistoryColors(filepath.Join(homeDir, ".claude", "history.jsonl"), metas)
	return metas
}

func collectSessionNames(dir string, metas map[string]SessionMeta) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return
	}
	for _, p := range matches {
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var rec struct {
			SessionID string `json:"sessionId"`
			Name      string `json:"name"`
		}
		if err := json.Unmarshal(data, &rec); err != nil {
			continue
		}
		if rec.SessionID == "" || rec.Name == "" {
			continue
		}
		m := metas[rec.SessionID]
		m.Name = rec.Name
		metas[rec.SessionID] = m
	}
}

func collectHistoryColors(path string, metas map[string]SessionMeta) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return
	}
	// Seek to (size - tail) and skip the first (possibly partial) line.
	if info.Size() > historyTailBytes {
		if _, err := f.Seek(info.Size()-historyTailBytes, io.SeekStart); err != nil {
			return
		}
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	first := info.Size() > historyTailBytes
	for scanner.Scan() {
		if first {
			// Drop the first line — it likely starts mid-record.
			first = false
			continue
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if !bytes.Contains(line, []byte(`"/color `)) {
			continue
		}
		var rec struct {
			Display   string `json:"display"`
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(line, &rec); err != nil {
			continue
		}
		if !strings.HasPrefix(rec.Display, "/color ") || rec.SessionID == "" {
			continue
		}
		color := strings.TrimSpace(strings.TrimPrefix(rec.Display, "/color "))
		if color == "" {
			continue
		}
		// Later entries overwrite earlier — last write per sid wins, which
		// matches the user's most recent /color intent.
		m := metas[rec.SessionID]
		m.Color = color
		metas[rec.SessionID] = m
	}
}

// ansiForColor maps a Claude-Code-style color name to an ANSI escape prefix.
// Returns "" for unknown names — caller treats that as "no color".
func ansiForColor(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "red":
		return "\033[31m"
	case "green":
		return "\033[32m"
	case "yellow":
		return "\033[33m"
	case "blue":
		return "\033[34m"
	case "magenta", "purple":
		return "\033[35m"
	case "cyan":
		return "\033[36m"
	case "white":
		return "\033[37m"
	case "gray", "grey":
		return "\033[90m"
	}
	return ""
}

const ansiReset = "\033[0m"
