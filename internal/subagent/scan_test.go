package subagent

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanParentTranscriptSummarizesSubagents(t *testing.T) {
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
	writeAgent := func(id, desc string, mod time.Time) {
		t.Helper()
		meta := filepath.Join(subdir, id+".meta.json")
		log := filepath.Join(subdir, id+".jsonl")
		if err := os.WriteFile(meta, []byte(`{"description":"`+desc+`","agentType":"general"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(log, []byte(`{"type":"assistant","message":{"content":"ok"}}`+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(log, mod, mod); err != nil {
			t.Fatal(err)
		}
	}
	writeAgent("agent-old", "Explore devportal-distro state", now.Add(-20*time.Minute))
	writeAgent("agent-new", "Design duplicate-plugin detection", now.Add(-30*time.Second))

	rollup, ok := ScanParentTranscript(parent, now)
	if !ok {
		t.Fatal("expected subagent rollup")
	}
	if rollup.Total != 2 || rollup.Active != 1 || rollup.Done != 1 {
		t.Fatalf("counts: got total=%d active=%d done=%d", rollup.Total, rollup.Active, rollup.Done)
	}
	if rollup.LatestDescription != "Design duplicate-plugin detection" {
		t.Fatalf("latest description: got %q", rollup.LatestDescription)
	}
}

func TestScanParentTranscriptMissingSubagentsReturnsFalse(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "parent-session.jsonl")
	if err := os.WriteFile(parent, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := ScanParentTranscript(parent, time.Now()); ok {
		t.Fatal("expected no rollup when subagents dir is missing")
	}
}
