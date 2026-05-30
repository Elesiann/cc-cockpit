package dashboard

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSessionMetas_NameFromSessionFile(t *testing.T) {
	home := t.TempDir()
	sessDir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(sessDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// One session with a /rename'd name, one without.
	if err := os.WriteFile(
		filepath.Join(sessDir, "12345.json"),
		[]byte(`{"sessionId":"sid-named","name":"my-task","pid":12345}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(sessDir, "67890.json"),
		[]byte(`{"sessionId":"sid-plain","pid":67890}`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	metas := LoadSessionMetas(home)
	if got := metas["sid-named"].Name; got != "my-task" {
		t.Errorf("sid-named name: got %q, want my-task", got)
	}
	if _, ok := metas["sid-plain"]; ok {
		t.Errorf("sid-plain shouldn't appear (no name, no color), got %+v", metas["sid-plain"])
	}
}

func TestLoadSessionMetas_ColorFromHistory(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Sequence of /color entries — last one wins per sid.
	history := `{"display":"hello","sessionId":"sid-a"}
{"display":"/color blue","sessionId":"sid-a","timestamp":1}
{"display":"/color red","sessionId":"sid-a","timestamp":2}
{"display":"/color green","sessionId":"sid-b","timestamp":3}
{"display":"some other command","sessionId":"sid-b"}
`
	if err := os.WriteFile(filepath.Join(home, ".claude", "history.jsonl"), []byte(history), 0o644); err != nil {
		t.Fatal(err)
	}

	metas := LoadSessionMetas(home)
	if got := metas["sid-a"].Color; got != "red" {
		t.Errorf("sid-a color: got %q, want red (latest wins)", got)
	}
	if got := metas["sid-b"].Color; got != "green" {
		t.Errorf("sid-b color: got %q, want green", got)
	}
}

func TestLoadSessionMetas_ColorSurvivesLargeRecentHistory(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}

	history := `{"display":"/color blue","sessionId":"sid-a","timestamp":1}
` + strings.Repeat(`{"display":"large later message with no color","sessionId":"sid-b"}
`, 5000)
	if err := os.WriteFile(filepath.Join(home, ".claude", "history.jsonl"), []byte(history), 0o644); err != nil {
		t.Fatal(err)
	}

	metas := LoadSessionMetas(home)
	if got := metas["sid-a"].Color; got != "blue" {
		t.Errorf("sid-a color: got %q, want blue", got)
	}
}

func TestSessionMetaLoader_AppendedHistoryUpdatesColor(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	historyPath := filepath.Join(claudeDir, "history.jsonl")
	if err := os.WriteFile(historyPath, []byte(`{"display":"/color blue","sessionId":"sid-a"}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	loader := NewSessionMetaLoader()
	metas := loader.Load(home)
	if got := metas["sid-a"].Color; got != "blue" {
		t.Fatalf("initial color: got %q, want blue", got)
	}

	f, err := os.OpenFile(historyPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"display":"/color red","sessionId":"sid-a"}
`); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	metas = loader.Load(home)
	if got := metas["sid-a"].Color; got != "red" {
		t.Errorf("appended color: got %q, want red", got)
	}
}

func TestSessionMetaLoader_TruncatedHistoryRebuildsColors(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	historyPath := filepath.Join(claudeDir, "history.jsonl")
	if err := os.WriteFile(historyPath, []byte(`{"display":"/color red","sessionId":"sid-a"}
{"display":"padding padding padding padding padding","sessionId":"sid-a"}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	loader := NewSessionMetaLoader()
	if got := loader.Load(home)["sid-a"].Color; got != "red" {
		t.Fatalf("initial color: got %q, want red", got)
	}

	if err := os.WriteFile(historyPath, []byte(`{"display":"/color blue","sessionId":"sid-b"}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	metas := loader.Load(home)
	if _, ok := metas["sid-a"]; ok {
		t.Errorf("truncated history should clear stale sid-a color, got %+v", metas["sid-a"])
	}
	if got := metas["sid-b"].Color; got != "blue" {
		t.Errorf("rebuilt color: got %q, want blue", got)
	}
}

func TestSessionMetaLoader_MissingHistoryClearsColors(t *testing.T) {
	home := t.TempDir()
	claudeDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	historyPath := filepath.Join(claudeDir, "history.jsonl")
	if err := os.WriteFile(historyPath, []byte(`{"display":"/color red","sessionId":"sid-a"}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	loader := NewSessionMetaLoader()
	if got := loader.Load(home)["sid-a"].Color; got != "red" {
		t.Fatalf("initial color: got %q, want red", got)
	}
	if err := os.Remove(historyPath); err != nil {
		t.Fatal(err)
	}

	if metas := loader.Load(home); len(metas) != 0 {
		t.Errorf("missing history should clear cached colors, got %+v", metas)
	}
}

func TestSessionMetaLoader_UnknownColorDoesNotOverrideKnownColor(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	history := `{"display":"/color red","sessionId":"sid-a"}
{"display":"/color catalog-unsplit","sessionId":"sid-a"}
{"display":"/color blue extra","sessionId":"sid-b"}
`
	if err := os.WriteFile(filepath.Join(home, ".claude", "history.jsonl"), []byte(history), 0o644); err != nil {
		t.Fatal(err)
	}

	metas := NewSessionMetaLoader().Load(home)
	if got := metas["sid-a"].Color; got != "red" {
		t.Errorf("unknown color should not override known color: got %q, want red", got)
	}
	if _, ok := metas["sid-b"]; ok {
		t.Errorf("multi-token color command should be ignored, got %+v", metas["sid-b"])
	}
}

func TestSessionMetaLoader_OrangeColorFromHistory(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(home, ".claude", "history.jsonl"),
		[]byte(`{"display":"/color orange","sessionId":"sid-a"}
`),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	metas := NewSessionMetaLoader().Load(home)
	if got := metas["sid-a"].Color; got != "orange" {
		t.Errorf("orange color: got %q, want orange", got)
	}
}

func TestLoadSessionMetas_NoFiles_NoPanic(t *testing.T) {
	home := t.TempDir()
	metas := LoadSessionMetas(home)
	if len(metas) != 0 {
		t.Errorf("expected empty metas, got %+v", metas)
	}
}

func TestAnsiForColor(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"red", "\033[31m"},
		{"green", "\033[32m"},
		{"yellow", "\033[33m"},
		{"GREEN", "\033[32m"},
		{"  cyan  ", "\033[36m"},
		{"orange", "\033[38;5;208m"},
		{"blue", "\033[34m"},
		{"magenta", "\033[35m"},
		{"purple", "\033[35m"}, // alias for magenta
		{"white", "\033[37m"},
		{"gray", "\033[90m"},
		{"grey", "\033[90m"}, // alias for gray
		{"chartreuse", ""},   // unknown → empty (no color)
		{"", ""},
	}
	for _, c := range cases {
		if got := ansiForColor(c.in); got != c.want {
			t.Errorf("ansiForColor(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestColorizeDataRows_OnlyDataLinesWrapped(t *testing.T) {
	table := "STATUS  SID  TASK\n▶  aaa  one\n▶  bbb  two\n▶  ccc  three"
	sids := []string{"sid-a", "sid-b", "sid-c"}
	metas := map[string]SessionMeta{
		"sid-a": {Color: "red"},
		"sid-c": {Color: "blue"},
	}
	out := colorizeDataRows(table, sids, metas, 1)
	lines := splitLines(out)
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d: %q", len(lines), out)
	}
	// Header unchanged.
	if lines[0] != "STATUS  SID  TASK" {
		t.Errorf("header was modified: %q", lines[0])
	}
	// Row 0 (sid-a): red wrap.
	if want := "\033[31m▶  aaa  one\033[0m"; lines[1] != want {
		t.Errorf("row 0: got %q want %q", lines[1], want)
	}
	// Row 1 (sid-b): no color → unchanged.
	if lines[2] != "▶  bbb  two" {
		t.Errorf("row 1 should be unchanged: got %q", lines[2])
	}
	// Row 2 (sid-c): blue wrap.
	if want := "\033[34m▶  ccc  three\033[0m"; lines[3] != want {
		t.Errorf("row 2: got %q want %q", lines[3], want)
	}
}

func TestColorizeDataRows_NilMetas_NoOp(t *testing.T) {
	table := "STATUS  SID\n▶  aaa\n▶  bbb"
	out := colorizeDataRows(table, []string{"sid-a", "sid-b"}, nil, 1)
	if out != "STATUS  SID\n▶  aaa\n▶  bbb" {
		t.Errorf("nil metas should leave table untouched, got %q", out)
	}
}

func splitLines(s string) []string {
	var lines []string
	cur := ""
	for _, r := range s {
		if r == '\n' {
			lines = append(lines, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}
