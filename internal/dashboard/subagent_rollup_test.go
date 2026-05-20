package dashboard

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/elesiann/cc-cockpit/internal/state"
)

func TestLoadSubagentRollupsUsesTranscriptPath(t *testing.T) {
	now := time.Date(2026, 5, 20, 16, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	parent := filepath.Join(dir, "parent-session.jsonl")
	if err := os.WriteFile(parent, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(dir, "parent-session", "subagents")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "agent-one.meta.json"), []byte(`{"description":"Fix preset composition"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(subdir, "agent-one.jsonl")
	if err := os.WriteFile(log, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(log, now.Add(-30*time.Second), now.Add(-30*time.Second)); err != nil {
		t.Fatal(err)
	}

	rollups := loadSubagentRollups([]TaggedState{{State: state.State{Sessions: map[string]*state.Session{
		"parent-session": {
			SessionID:      "parent-session",
			Status:         state.StatusWaitingInput,
			TranscriptPath: json.RawMessage(`"` + parent + `"`),
		},
	}}}}, now)

	r, ok := rollups["parent-session"]
	if !ok {
		t.Fatalf("missing rollup: %#v", rollups)
	}
	if r.Active != 1 || r.Done != 0 || r.LatestDescription != "Fix preset composition" {
		t.Fatalf("bad rollup: %#v", r)
	}
}
