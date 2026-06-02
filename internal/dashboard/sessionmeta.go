package dashboard

import (
	"bufio"
	"bytes"
	"encoding/json"
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

type SessionMetaLoader struct {
	colors        map[string]string
	historyPath   string
	historyOffset int64
}

func NewSessionMetaLoader() *SessionMetaLoader {
	return &SessionMetaLoader{colors: make(map[string]string)}
}

// LoadSessionMetas builds the sessionId → SessionMeta map for one render
// tick. Reads:
//   - every ~/.claude/sessions/*.json for the `name` field (set by /rename).
//   - ~/.claude/history.jsonl for the latest /color per sid.
//
// Returns an empty map on I/O errors — the dashboard renders fine without
// metas, this is purely additive polish.
func LoadSessionMetas(homeDir string) map[string]SessionMeta {
	return NewSessionMetaLoader().Load(homeDir)
}

// Load returns the current session metadata. History colors are indexed
// incrementally after the first call, while session names are re-read each tick
// because the current session files are small and represent latest state.
func (l *SessionMetaLoader) Load(homeDir string) map[string]SessionMeta {
	if l.colors == nil {
		l.colors = make(map[string]string)
	}
	l.collectHistoryColors(filepath.Join(homeDir, ".claude", "history.jsonl"))

	metas := make(map[string]SessionMeta)
	collectSessionNames(filepath.Join(homeDir, ".claude", "sessions"), metas)
	for sid, color := range l.colors {
		m := metas[sid]
		m.Color = color
		metas[sid] = m
	}
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

func (l *SessionMetaLoader) collectHistoryColors(path string) {
	if path != l.historyPath {
		l.historyPath = path
		l.historyOffset = 0
		clear(l.colors)
	}

	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			l.historyOffset = 0
			clear(l.colors)
		}
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return
	}
	if info.Size() < l.historyOffset {
		// history.jsonl was truncated or rotated. Rebuild from the beginning so
		// stale colors from the previous file cannot leak into the dashboard.
		l.historyOffset = 0
		clear(l.colors)
	}
	if info.Size() == l.historyOffset {
		return
	}
	if l.historyOffset > 0 {
		if _, err := f.Seek(l.historyOffset, 0); err != nil {
			return
		}
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		sid, color, ok := parseHistoryColorLine(scanner.Bytes())
		if !ok {
			continue
		}
		// Later entries overwrite earlier — last write per sid wins, which
		// matches the user's most recent accepted /color intent.
		l.colors[sid] = color
	}
	l.historyOffset = info.Size()
}

func parseHistoryColorLine(line []byte) (sid, color string, ok bool) {
	line = bytes.TrimSpace(line)
	if !bytes.Contains(line, []byte(`"/color`)) {
		return "", "", false
	}
	var rec struct {
		Display   string `json:"display"`
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(line, &rec); err != nil {
		return "", "", false
	}
	if rec.SessionID == "" {
		return "", "", false
	}
	fields := strings.Fields(rec.Display)
	if len(fields) != 2 || fields[0] != "/color" {
		return "", "", false
	}
	color = strings.ToLower(fields[1])
	if ansiForColor(color) == "" {
		return "", "", false
	}
	return rec.SessionID, color, true
}

// ansiForColor maps a Claude-Code-style color name to an ANSI escape prefix.
// Returns "" for unknown names — caller treats that as "no color".
// Returns "" unconditionally when dashboard.NoColor is set (--color=never).
func ansiForColor(name string) string {
	if NoColor {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "red":
		return "\033[31m"
	case "green":
		return "\033[32m"
	case "yellow":
		return "\033[33m"
	case "orange":
		return "\033[38;5;208m"
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
