package tmux

import (
	"reflect"
	"testing"
)

func TestNewSessionArgs(t *testing.T) {
	got := newSessionArgs("ws", []string{"COCKPIT_STATE_HOME=/x", "COCKPIT_WORKSPACE_NAME=ws"})
	want := []string{
		"new-session", "-d", "-s", "ws",
		"-e", "COCKPIT_STATE_HOME=/x",
		"-e", "COCKPIT_WORKSPACE_NAME=ws",
		"cc-cockpit", "dashboard",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestSplitControlArgs_DefaultsToBareBash(t *testing.T) {
	got := splitControlArgs("ws", []string{"FOO=bar"})
	want := []string{
		"split-window", "-h", "-t", "ws:0",
		"-e", "FOO=bar",
		"bash",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestSplitControlArgs_UsesProvidedControlCmd(t *testing.T) {
	got := splitControlArgs("ws", nil, "bash", "--rcfile", "/x/control.bashrc")
	want := []string{
		"split-window", "-h", "-t", "ws:0",
		"bash", "--rcfile", "/x/control.bashrc",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestServerOptionArgs(t *testing.T) {
	got := serverOptionArgs()
	want := [][]string{
		{"set-option", "-g", "mouse", "on"},
		{"set-option", "-g", "pane-border-status", "top"},
		{"set-option", "-g", "pane-border-format", " #{pane_title} "},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestSpawnPaneArgs_FirstSpawn(t *testing.T) {
	// 2 existing panes (dashboard + control) → full-width bottom row.
	got := spawnPaneArgs(
		"ws",
		"/repos/api",
		[]string{"COCKPIT_TASK_NAME=fix bug", "COCKPIT_PRIMARY_REPO=api"},
		[]string{"%0", "%1"},
		[]string{"claude"},
	)
	want := []string{
		"split-window", "-v", "-f", "-t", "ws:0",
		"-c", "/repos/api",
		"-P", "-F", "#{pane_id}",
		"-e", "COCKPIT_TASK_NAME=fix bug",
		"-e", "COCKPIT_PRIMARY_REPO=api",
		"claude",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestSpawnPaneArgs_SubsequentSpawn(t *testing.T) {
	// 3 existing panes (dashboard + control + one claude) → split last horizontally.
	got := spawnPaneArgs(
		"ws",
		"/repos/web",
		nil,
		[]string{"%0", "%1", "%5"},
		[]string{"claude"},
	)
	want := []string{
		"split-window", "-h", "-t", "%5",
		"-c", "/repos/web",
		"-P", "-F", "#{pane_id}",
		"claude",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

func TestSpawnPaneArgs_TargetsMostRecentPane(t *testing.T) {
	// 4 panes — split target must be the LAST entry, not any earlier claude pane.
	got := spawnPaneArgs("ws", "/cwd", nil, []string{"%0", "%1", "%2", "%7"}, []string{"claude"})
	// The "-t" value sits immediately after "split-window -h".
	if len(got) < 4 || got[0] != "split-window" || got[1] != "-h" || got[2] != "-t" || got[3] != "%7" {
		t.Errorf("expected split-window -h -t %%7 prefix; got %v", got)
	}
}
