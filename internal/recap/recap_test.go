package recap

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeJSONL writes lines to a temp transcript file and returns its path.
func writeJSONL(t *testing.T, lines ...string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "transcript.jsonl")
	content := ""
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	return path
}

func TestRead_NoFile(t *testing.T) {
	if _, ok := Read("/nonexistent/transcript.jsonl"); ok {
		t.Fatal("expected ok=false for a missing transcript")
	}
}

func TestRead_EmptyPath(t *testing.T) {
	if _, ok := Read(""); ok {
		t.Fatal("expected ok=false for an empty path")
	}
}

func TestRead_NoRecapYet(t *testing.T) {
	// A transcript with normal events but no away_summary — the common
	// state for a session whose first quiet period hasn't elapsed.
	path := writeJSONL(t,
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":"hello"}}`,
	)
	if _, ok := Read(path); ok {
		t.Fatal("expected ok=false when no away_summary is present")
	}
}

func TestRead_SingleRecap(t *testing.T) {
	path := writeJSONL(t,
		`{"type":"user","message":{"role":"user","content":"hi"}}`,
		`{"type":"system","subtype":"away_summary","content":"Goal: ship session recaps. Next: wire the watch render. (disable recaps in /config)","timestamp":"2026-05-20T14:30:00.000Z"}`,
	)
	r, ok := Read(path)
	if !ok {
		t.Fatal("expected ok=true")
	}
	want := "Goal: ship session recaps. Next: wire the watch render."
	if r.Text != want {
		t.Fatalf("text mismatch:\n got: %q\nwant: %q", r.Text, want)
	}
	wantAt, _ := time.Parse(time.RFC3339, "2026-05-20T14:30:00.000Z")
	if !r.At.Equal(wantAt) {
		t.Fatalf("timestamp mismatch: got %v want %v", r.At, wantAt)
	}
}

func TestRead_LastRecapWins(t *testing.T) {
	// Multiple away_summary events: the final one is the freshest recap.
	path := writeJSONL(t,
		`{"type":"system","subtype":"away_summary","content":"First recap.","timestamp":"2026-05-20T10:00:00.000Z"}`,
		`{"type":"user","message":{"role":"user","content":"more work"}}`,
		`{"type":"system","subtype":"away_summary","content":"Second recap.","timestamp":"2026-05-20T12:00:00.000Z"}`,
	)
	r, ok := Read(path)
	if !ok {
		t.Fatal("expected ok=true")
	}
	if r.Text != "Second recap." {
		t.Fatalf("expected the last recap, got %q", r.Text)
	}
}

func TestRead_SkipsMalformedLines(t *testing.T) {
	path := writeJSONL(t,
		`{"type":"system","subtype":"away_summary"`, // truncated JSON, contains "away_summary"
		`not json at all`,
		`{"type":"system","subtype":"away_summary","content":"Valid recap.","timestamp":"2026-05-20T12:00:00.000Z"}`,
	)
	r, ok := Read(path)
	if !ok {
		t.Fatal("expected ok=true — one valid recap line should survive malformed neighbors")
	}
	if r.Text != "Valid recap." {
		t.Fatalf("got %q", r.Text)
	}
}

func TestClean(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Goal: x. (disable recaps in /config)", "Goal: x."},
		{"  spaced  out   text  ", "spaced out text"},
		{"line one\nline two", "line one line two"},
		{"no hint here", "no hint here"},
		{"", ""},
	}
	for _, c := range cases {
		if got := Clean(c.in); got != c.want {
			t.Errorf("Clean(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
