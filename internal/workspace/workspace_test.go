package workspace

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestValidSlug(t *testing.T) {
	cases := map[string]bool{
		"foo":           true,
		"my-workspace":  true,
		"my.workspace":  true,
		"a":             true,
		"a1":            true,
		"":              false,
		".hidden":       false,
		"foo/bar":       false,
		"../evil":       false,
		"a b":           false,
		"-leading-dash": false,
	}
	for s, want := range cases {
		if got := ValidSlug(s); got != want {
			t.Errorf("ValidSlug(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestSlugFromPath(t *testing.T) {
	cases := map[string]string{
		"/home/user/my-workspace": "my-workspace",
		"/home/user/My Workspace": "My-Workspace",
		"/home/user/.hidden":      "hidden",
		"/":                       "workspace",
		"/foo/bar/123repo":        "123repo",
	}
	for in, want := range cases {
		if got := SlugFromPath(in); got != want {
			t.Errorf("SlugFromPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindRoot(t *testing.T) {
	tmp := t.TempDir()
	deep := filepath.Join(tmp, "a", "b", "c")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	wsDir := filepath.Join(tmp, ".cc-cockpit")
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "workspace.json"), []byte(`{"name":"x","repos":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	tmpReal, _ := filepath.EvalSymlinks(tmp)
	deepReal, _ := filepath.EvalSymlinks(deep)
	if got := FindRoot(deepReal); got != tmpReal {
		t.Errorf("FindRoot from deep dir: got %q, want %q", got, tmpReal)
	}

	// No workspace anywhere up the tree.
	other := t.TempDir()
	if got := FindRoot(other); got != "" {
		t.Errorf("FindRoot with no workspace: got %q, want empty", got)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	ws := &Workspace{
		Name:  "rt-test",
		Repos: map[string]string{"api": "packages/api", "web": "web"},
	}
	if err := ws.Save(tmp); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(tmp)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Name != ws.Name || len(got.Repos) != len(ws.Repos) || got.Repos["api"] != "packages/api" {
		t.Errorf("round trip mismatch: got %+v want %+v", got, ws)
	}
}

func TestAddRepo_Containment(t *testing.T) {
	tmp := t.TempDir()
	tmp, _ = filepath.EvalSymlinks(tmp)
	good := filepath.Join(tmp, "good")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", good, "init", "-q").Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}

	ws := &Workspace{Name: "ct"}

	// Inside-root, real git repo: accepted.
	if err := ws.AddRepo(tmp, "good", "good"); err != nil {
		t.Errorf("AddRepo good: %v", err)
	}

	// Inside-root directories whose names start with ".." are valid as long
	// as filepath.Rel did not actually escape to ".." or "../...".
	dotdotName := filepath.Join(tmp, "..repo")
	if err := os.MkdirAll(dotdotName, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("git", "-C", dotdotName, "init", "-q").Run(); err != nil {
		t.Skipf("git not available: %v", err)
	}
	if err := ws.AddRepo(tmp, "dotdot", "..repo"); err != nil {
		t.Errorf("AddRepo ..repo inside root: %v", err)
	}

	// Outside root: rejected.
	outside := filepath.Join(filepath.Dir(tmp), "outside")
	_ = os.MkdirAll(outside, 0o755)
	if err := ws.AddRepo(tmp, "esc", "../"+filepath.Base(outside)); err == nil {
		t.Errorf("AddRepo for outside path should fail")
	}

	// Non-git dir: rejected.
	notgit := filepath.Join(tmp, "notgit")
	_ = os.MkdirAll(notgit, 0o755)
	if err := ws.AddRepo(tmp, "ng", "notgit"); err == nil {
		t.Errorf("AddRepo for non-git dir should fail")
	}

	// Duplicate label: rejected.
	if err := ws.AddRepo(tmp, "good", "good"); err == nil {
		t.Errorf("AddRepo duplicate label should fail")
	}

	// Invalid slug: rejected.
	if err := ws.AddRepo(tmp, "../evil", "good"); err == nil {
		t.Errorf("AddRepo with bad label should fail")
	}
}

func TestDiscoverRepos(t *testing.T) {
	tmp := t.TempDir()
	tmp, _ = filepath.EvalSymlinks(tmp)
	for _, p := range []string{
		filepath.Join(tmp, "api"),                     // depth-1 repo
		filepath.Join(tmp, "packages", "web"),         // depth-2 repo
		filepath.Join(tmp, "deep", "a", "b", "infra"), // depth-4: too deep, won't be found
	} {
		if err := os.MkdirAll(p, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := exec.Command("git", "-C", p, "init", "-q").Run(); err != nil {
			t.Skipf("git not available: %v", err)
		}
	}
	got, err := DiscoverRepos(tmp)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		filepath.Join(tmp, "api"):             true,
		filepath.Join(tmp, "packages", "web"): true,
	}
	if len(got) != len(want) {
		t.Errorf("DiscoverRepos: got %d repos %v, want %d %v", len(got), got, len(want), want)
	}
	for _, p := range got {
		if !want[p] {
			t.Errorf("DiscoverRepos returned unexpected %q", p)
		}
	}
}
