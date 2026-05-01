package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestAppend_FreshState_AssignsSeqOne(t *testing.T) {
	dir := t.TempDir()
	if err := Append(dir, map[string]any{"event_type": "Test", "session_id": "s1"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got := readLog(t, dir)
	if len(got) != 1 || got[0].Seq != 1 {
		t.Errorf("first append should have seq=1, got %+v", got)
	}
	if got[0].EventType != "Test" || got[0].SessionID != "s1" {
		t.Errorf("event fields not preserved: got %+v", got[0])
	}
	if got[0].WallClockISO8601 == "" {
		t.Errorf("wall_clock_iso8601 not set")
	}
}

func TestAppend_Sequential_MonotonicSeqs(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := Append(dir, map[string]any{"event_type": "Test", "session_id": "s1"}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	got := readLog(t, dir)
	if len(got) != 5 {
		t.Fatalf("want 5 events, got %d", len(got))
	}
	for i, ev := range got {
		want := int64(i + 1)
		if ev.Seq != want {
			t.Errorf("event %d: seq=%d, want %d", i, ev.Seq, want)
		}
	}
}

func TestAppend_RecoversFromCorruptCounter(t *testing.T) {
	dir := t.TempDir()
	// Corrupt the counter; append should fall back to scanning the log (also empty here).
	if err := os.WriteFile(filepath.Join(dir, "seq.counter"), []byte("not-a-number\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Append(dir, map[string]any{"event_type": "Test", "session_id": "s1"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got := readLog(t, dir)
	if len(got) != 1 || got[0].Seq != 1 {
		t.Errorf("recovery from corrupt counter failed: got %+v", got)
	}
}

func TestAppend_RecoversFromMissingCounter(t *testing.T) {
	dir := t.TempDir()
	// Pre-seed events.jsonl with some events but NO counter file (simulates state corruption).
	preseed := `{"seq":7,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"X","session_id":"s1"}
{"seq":8,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"Y","session_id":"s1"}
`
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(preseed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Append(dir, map[string]any{"event_type": "Z", "session_id": "s1"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got := readLog(t, dir)
	if len(got) != 3 {
		t.Fatalf("want 3 events, got %d", len(got))
	}
	if got[2].Seq != 9 {
		t.Errorf("recovered seq should be max(log)+1 = 9, got %d", got[2].Seq)
	}
}

func TestAppend_TakesMaxOfCounterAndLog(t *testing.T) {
	dir := t.TempDir()
	// Counter says 2, log has events up to seq=5 (from a hypothetical bash gap-bug scenario
	// inverted). Next seq should be 6, not 3.
	if err := os.WriteFile(filepath.Join(dir, "seq.counter"), []byte("2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	preseed := `{"seq":4,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"X","session_id":"s1"}
{"seq":5,"wall_clock_iso8601":"2026-04-20T15:00:01Z","event_type":"Y","session_id":"s1"}
`
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(preseed), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Append(dir, map[string]any{"event_type": "Z", "session_id": "s1"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	got := readLog(t, dir)
	if got[2].Seq != 6 {
		t.Errorf("next seq should be max(counter=2, log=5)+1 = 6, got %d", got[2].Seq)
	}
}

func TestAppend_UpdatesCounter(t *testing.T) {
	dir := t.TempDir()
	if err := Append(dir, map[string]any{"event_type": "X", "session_id": "s1"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "seq.counter"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(raw)) != "1" {
		t.Errorf("counter should be 1, got %q", string(raw))
	}
}

func TestAppend_ConcurrentAppendsAllUnique(t *testing.T) {
	dir := t.TempDir()
	const N = 50
	var wg sync.WaitGroup
	wg.Add(N)
	errs := make(chan error, N)
	for i := 0; i < N; i++ {
		go func(i int) {
			defer wg.Done()
			if err := Append(dir, map[string]any{"event_type": "Concurrent", "session_id": "s1"}); err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent Append: %v", err)
	}
	got := readLog(t, dir)
	if len(got) != N {
		t.Fatalf("want %d events after concurrent appends, got %d", N, len(got))
	}
	seen := make(map[int64]bool)
	for _, ev := range got {
		if seen[ev.Seq] {
			t.Errorf("duplicate seq=%d under concurrent appends", ev.Seq)
		}
		seen[ev.Seq] = true
		if ev.Seq < 1 || ev.Seq > N {
			t.Errorf("seq out of range: %d", ev.Seq)
		}
	}
}

// readLog parses events.jsonl into a slice of Events. Only fields needed by
// the tests are populated; full validation lives in the reducer.
func readLog(t *testing.T, dir string) []Event {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []Event
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("parse %q: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}
