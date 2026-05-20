package dashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/elesiann/cc-cockpit/internal/state"
)

func writeTranscript(t *testing.T, path, text string) {
	t.Helper()
	line := `{"type":"system","subtype":"away_summary","content":` + strconvQuote(text) + `,"timestamp":"2026-05-20T12:00:00.000Z"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestRecapCache_LoadsAndRefreshesOnMtimeChange(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	writeTranscript(t, path, "First recap.")

	cache := newRecapCache()
	samples := []TaggedState{{State: state.State{Sessions: map[string]*state.Session{
		"sid123": {TranscriptPath: json.RawMessage(strconvQuote(path))},
	}}}}

	got := cache.load(samples)
	if got["sid123"] != "First recap." {
		t.Fatalf("initial recap = %q", got["sid123"])
	}

	// Force a distinct mtime on filesystems with coarse timestamp resolution.
	next := time.Now().Add(2 * time.Second)
	writeTranscript(t, path, "Second recap.")
	if err := os.Chtimes(path, next, next); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	got = cache.load(samples)
	if got["sid123"] != "Second recap." {
		t.Fatalf("refreshed recap = %q", got["sid123"])
	}
}

func TestRecapCache_OmitsMissingRecap(t *testing.T) {
	cache := newRecapCache()
	samples := []TaggedState{{State: state.State{Sessions: map[string]*state.Session{
		"sid123": {TranscriptPath: json.RawMessage(`"/no/such/transcript.jsonl"`)},
	}}}}
	got := cache.load(samples)
	if _, ok := got["sid123"]; ok {
		t.Fatalf("missing transcript should not render a placeholder: %#v", got)
	}
}
