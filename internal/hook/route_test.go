package hook

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/elesiann/cc-cockpit/internal/workspace"
)

// fixture holds the injected state for one Resolve call.
type fixture struct {
	homeDir string
	env     map[string]string
	files   map[string][]byte
	roots   map[string]string // cwd → workspace root (or "" for no match)
	wsNames map[string]string // workspace root → workspace name
	sleeps  int
	reads   []string
}

func (f *fixture) resolver() *Resolver {
	return &Resolver{
		HomeDir: f.homeDir,
		Getenv:  func(k string) string { return f.env[k] },
		ReadFile: func(p string) ([]byte, error) {
			f.reads = append(f.reads, p)
			if data, ok := f.files[p]; ok {
				return data, nil
			}
			return nil, os.ErrNotExist
		},
		EvalLinks: func(p string) (string, error) { return p, nil },
		FindRoot: func(p string) string {
			if r, ok := f.roots[p]; ok {
				return r
			}
			return ""
		},
		LoadWS: func(root string) (*workspace.Workspace, error) {
			if name, ok := f.wsNames[root]; ok {
				return &workspace.Workspace{Name: name}, nil
			}
			return nil, errors.New("workspace not found")
		},
		Sleep: func(time.Duration) { f.sleeps++ },
	}
}

const sid = "deadbeef-cafe-0000-0000-000000000000"

func statePath(home string) string {
	return filepath.Join(home, ".claude", "jobs", sid, "state.json")
}

func globalSH(home string) string {
	return filepath.Join(home, ".local", "state", "cc-cockpit", GlobalWorkspaceName)
}

func TestResolve_NoStateNoEnvNoCwd_FallsBackToGlobal(t *testing.T) {
	f := &fixture{homeDir: "/home/u", env: map[string]string{}}
	sh := f.resolver().Resolve(sid, "")
	if sh != globalSH("/home/u") {
		t.Fatalf("expected global stateHome, got %q", sh)
	}
	if f.sleeps != 3 {
		t.Fatalf("expected 3 retry sleeps, got %d", f.sleeps)
	}
}

func TestResolve_AgentView_OriginCwdInsideWorkspace(t *testing.T) {
	f := &fixture{
		homeDir: "/home/u",
		env:     map[string]string{"XDG_STATE_HOME": "/xdg"},
		files: map[string][]byte{
			statePath("/home/u"): []byte(`{"originCwd":"/home/u/work/repo","cwd":"/home/u/work/repo/.claude/worktrees/x"}`),
		},
		roots:   map[string]string{"/home/u/work/repo": "/home/u/work"},
		wsNames: map[string]string{"/home/u/work": "work-ws"},
	}
	sh := f.resolver().Resolve(sid, "")
	want := "/xdg/cc-cockpit/work-ws"
	if sh != want {
		t.Fatalf("stateHome: got %q want %q", sh, want)
	}
}

func TestResolve_AgentView_FallsBackToCwdWhenOriginMissing(t *testing.T) {
	f := &fixture{
		homeDir: "/home/u",
		env:     map[string]string{},
		files: map[string][]byte{
			statePath("/home/u"): []byte(`{"cwd":"/home/u/work/repo"}`),
		},
		roots:   map[string]string{"/home/u/work/repo": "/home/u/work"},
		wsNames: map[string]string{"/home/u/work": "work-ws"},
	}
	sh := f.resolver().Resolve(sid, "")
	want := "/home/u/.local/state/cc-cockpit/work-ws"
	if sh != want {
		t.Fatalf("stateHome: got %q want %q", sh, want)
	}
}

func TestResolve_AgentView_OutsideAnyWorkspace_RoutesToGlobal(t *testing.T) {
	// Agent View session whose declared cwd isn't inside any workspace tree.
	// Previously this dropped silently; now it routes to the synthetic _global
	// workspace so `cc-cockpit watch` can still see it.
	f := &fixture{
		homeDir: "/home/u",
		env:     map[string]string{},
		files: map[string][]byte{
			statePath("/home/u"): []byte(`{"originCwd":"/tmp/scratch"}`),
		},
		// no roots entry → FindRoot returns ""
	}
	sh := f.resolver().Resolve(sid, "")
	if sh != globalSH("/home/u") {
		t.Fatalf("expected global stateHome, got %q", sh)
	}
}

func TestResolve_RetryCoversLateStateWrite(t *testing.T) {
	// state.json appears on the third read attempt.
	f := &fixture{
		homeDir: "/home/u",
		env:     map[string]string{},
		roots:   map[string]string{"/home/u/work/repo": "/home/u/work"},
		wsNames: map[string]string{"/home/u/work": "work-ws"},
	}
	// Wrap ReadFile manually to deliver the file only on attempt 3.
	r := f.resolver()
	attempts := 0
	r.ReadFile = func(p string) ([]byte, error) {
		attempts++
		if attempts < 3 {
			return nil, os.ErrNotExist
		}
		return []byte(`{"originCwd":"/home/u/work/repo"}`), nil
	}
	sh := r.Resolve(sid, "")
	want := "/home/u/.local/state/cc-cockpit/work-ws"
	if sh != want {
		t.Fatalf("retry: stateHome=%q want %q (attempts=%d)", sh, want, attempts)
	}
	if attempts != 3 {
		t.Fatalf("expected 3 read attempts, got %d", attempts)
	}
}

func TestResolve_InteractiveCwd_RoutesToWorkspaceAncestor(t *testing.T) {
	// The headline case for 0.6.1: a vanilla `claude` started in a directory
	// whose ancestor has a workspace.json. No state.json, no env vars.
	f := &fixture{
		homeDir: "/home/u",
		env:     map[string]string{},
		roots:   map[string]string{"/home/u/projects/api": "/home/u/projects"},
		wsNames: map[string]string{"/home/u/projects": "projects-ws"},
	}
	sh := f.resolver().Resolve(sid, "/home/u/projects/api")
	want := "/home/u/.local/state/cc-cockpit/projects-ws"
	if sh != want {
		t.Fatalf("stateHome: got %q want %q", sh, want)
	}
}

func TestResolve_InteractiveCwd_NoWorkspaceAncestor_FallsBackToGlobal(t *testing.T) {
	// claude run in /tmp or anywhere without a workspace.json ancestor lands
	// in _global so `watch` still sees it.
	f := &fixture{
		homeDir: "/home/u",
		env:     map[string]string{},
		// no roots entry for /tmp/anywhere → FindRoot returns ""
	}
	sh := f.resolver().Resolve(sid, "/tmp/anywhere")
	if sh != globalSH("/home/u") {
		t.Fatalf("expected global stateHome, got %q", sh)
	}
}

func TestComputeStateHome_XDGPreferred(t *testing.T) {
	got := ComputeStateHome("/home/u", func(k string) string {
		if k == "XDG_STATE_HOME" {
			return "/xdg"
		}
		return ""
	}, "ws-1")
	if got != "/xdg/cc-cockpit/ws-1" {
		t.Fatalf("got %q", got)
	}
}

func TestComputeStateHome_FallsBackToHome(t *testing.T) {
	got := ComputeStateHome("/home/u", func(string) string { return "" }, "ws-1")
	if got != "/home/u/.local/state/cc-cockpit/ws-1" {
		t.Fatalf("got %q", got)
	}
}
