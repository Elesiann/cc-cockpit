package dashboard

import (
	"os"
	"path/filepath"
	"testing"
)

func writeEvents(t *testing.T, dir string, lines ...string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var buf []byte
	for _, l := range lines {
		buf = append(buf, []byte(l+"\n")...)
	}
	if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), buf, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestAggregateSource_Sample_FindsAllWorkspaces(t *testing.T) {
	root := t.TempDir()
	writeEvents(t, filepath.Join(root, "alpha"),
		`{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"sid-a","payload":{"cwd":"/repos/api"}}`,
	)
	writeEvents(t, filepath.Join(root, "beta"),
		`{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"sid-b","payload":{"cwd":"/repos/web"}}`,
	)
	// Empty dir without events.jsonl must not crash the glob.
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatal(err)
	}

	src := AggregateSource{StateRoot: root}
	samples, err := src.Sample()
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("samples len: got %d want 2 (got %+v)", len(samples), samples)
	}
	names := []string{samples[0].Name, samples[1].Name}
	if !(names[0] == "alpha" && names[1] == "beta") {
		t.Errorf("expected sorted alpha,beta got %v", names)
	}
	// Each sample has its own session — confirm they didn't get mixed.
	if _, ok := samples[0].State.Sessions["sid-a"]; !ok {
		t.Errorf("alpha missing sid-a")
	}
	if _, ok := samples[1].State.Sessions["sid-b"]; !ok {
		t.Errorf("beta missing sid-b")
	}
	if _, ok := samples[0].State.Sessions["sid-b"]; ok {
		t.Errorf("alpha leaked sid-b across workspaces")
	}
}

func TestAggregateSource_Sample_PerWorkspaceSeqIsIndependent(t *testing.T) {
	// Each workspace numbers seq from 1; the per-source Reduce keeps them
	// separate so a low-seq event in workspace B doesn't get "outdated" by
	// a high-seq event in workspace A.
	root := t.TempDir()
	writeEvents(t, filepath.Join(root, "ws1"),
		`{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"sid-1","payload":{"cwd":"/r1"}}`,
		`{"seq":2,"wall_clock_iso8601":"2026-04-20T15:00:05Z","event_type":"UserPromptSubmit","session_id":"sid-1","payload":{"prompt_preview":"p"}}`,
	)
	writeEvents(t, filepath.Join(root, "ws2"),
		`{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"sid-2","payload":{"cwd":"/r2"}}`,
	)
	samples, err := (AggregateSource{StateRoot: root}).Sample()
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if samples[0].State.LastSeq != 2 || samples[1].State.LastSeq != 1 {
		t.Errorf("per-workspace LastSeq wrong: ws1=%d ws2=%d (want 2,1)",
			samples[0].State.LastSeq, samples[1].State.LastSeq)
	}
}

func TestAggregateSource_HeaderName(t *testing.T) {
	a := AggregateSource{}
	got := a.HeaderName(make([]TaggedState, 3))
	if got != "watch · 3 workspace(s)" {
		t.Errorf("header: got %q", got)
	}
}

func TestAggregateSource_HeaderName_WithFilter(t *testing.T) {
	a := AggregateSource{AllowedWorkspaces: []string{"api", "web"}}
	got := a.HeaderName(make([]TaggedState, 2))
	if got != "watch · 2/api,web" {
		t.Errorf("header: got %q", got)
	}
}

func TestAggregateSource_HeaderName_ShowsNonDefaultSort(t *testing.T) {
	defer func(prev string) { ActiveSort = prev }(ActiveSort)
	ActiveSort = SortAttention
	a := AggregateSource{}
	got := a.HeaderName(make([]TaggedState, 3))
	if got != "watch · 3 workspace(s) · sort=attention" {
		t.Errorf("header: got %q", got)
	}
}

func TestAggregateSource_Sample_AppliesAllowList(t *testing.T) {
	root := t.TempDir()
	writeEvents(t, filepath.Join(root, "alpha"),
		`{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"sid-a","payload":{"cwd":"/r/a"}}`,
	)
	writeEvents(t, filepath.Join(root, "beta"),
		`{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"sid-b","payload":{"cwd":"/r/b"}}`,
	)
	writeEvents(t, filepath.Join(root, "gamma"),
		`{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"sid-g","payload":{"cwd":"/r/g"}}`,
	)

	src := AggregateSource{StateRoot: root, AllowedWorkspaces: []string{"alpha", "gamma"}}
	samples, err := src.Sample()
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if len(samples) != 2 {
		t.Fatalf("samples len: got %d want 2 (got %+v)", len(samples), samples)
	}
	names := []string{samples[0].Name, samples[1].Name}
	if !(names[0] == "alpha" && names[1] == "gamma") {
		t.Errorf("expected alpha,gamma (beta filtered out) got %v", names)
	}
}

func TestAggregateSource_Sample_EmptyAllowList_IncludesEverything(t *testing.T) {
	// Defensive: AllowedWorkspaces == nil means no filtering (the default).
	root := t.TempDir()
	writeEvents(t, filepath.Join(root, "alpha"),
		`{"seq":1,"wall_clock_iso8601":"2026-04-20T15:00:00Z","event_type":"SessionStart","session_id":"sid-a","payload":{}}`,
	)
	src := AggregateSource{StateRoot: root}
	samples, err := src.Sample()
	if err != nil {
		t.Fatalf("sample: %v", err)
	}
	if len(samples) != 1 {
		t.Fatalf("expected 1 sample, got %d", len(samples))
	}
}

func TestDefaultStateRoot_PrefersXDG(t *testing.T) {
	got := DefaultStateRoot("/home/u", func(k string) string {
		if k == "XDG_STATE_HOME" {
			return "/xdg"
		}
		return ""
	})
	if got != "/xdg/cc-cockpit" {
		t.Errorf("got %q", got)
	}
}

func TestDefaultStateRoot_FallsBackToHome(t *testing.T) {
	got := DefaultStateRoot("/home/u", func(string) string { return "" })
	want := "/home/u/.local/state/cc-cockpit"
	if got != want {
		t.Errorf("got %q want %q", got, want)
	}
}
