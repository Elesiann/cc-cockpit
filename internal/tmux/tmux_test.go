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

func TestSplitControlArgs(t *testing.T) {
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

func TestNewWindowArgs(t *testing.T) {
	got := newWindowArgs("ws", "api: fix bug", "/repos/api",
		[]string{"COCKPIT_TASK_NAME=fix bug", "COCKPIT_PRIMARY_REPO=api"},
		[]string{"claude"})
	want := []string{
		"new-window", "-d",
		"-t", "ws",
		"-n", "api: fix bug",
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

func TestNewWindowArgs_NoEnv(t *testing.T) {
	got := newWindowArgs("ws", "name", "/cwd", nil, []string{"claude"})
	want := []string{
		"new-window", "-d",
		"-t", "ws",
		"-n", "name",
		"-c", "/cwd",
		"-P", "-F", "#{pane_id}",
		"claude",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}
