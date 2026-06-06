package winfocus

import (
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

// sidecarDir is the per-workspace subdirectory holding session->window bindings.
const sidecarDir = "windows"

// captureAttempts bounds the focused-window reads; retryDelay spaces them.
// stableReads is how many consecutive reads must agree before a binding is
// persisted: the detached capture only proves "some WT window is focused now",
// so requiring the same window+tab twice in a row rejects the case where focus
// moved between reads (e.g. the user alt-tabbed during powershell cold start),
// which would otherwise bind the session to whatever window happened to be up.
const (
	captureAttempts = 4
	retryDelay      = 300 * time.Millisecond
	stableReads     = 2
)

// Binding is a session's resolved Windows Terminal location: the window handle
// plus the UI Automation RuntimeId of its tab (dot-joined digits). A tab's
// positional index drifts as sibling tabs open and close, so the RuntimeId —
// stable for the element's lifetime, which is bounded by the WT process that
// also owns the HWND — is what re-finds the right tab later. TabRID == ""
// means "window only / tab unknown".
type Binding struct {
	HWND   string
	TabRID string
}

// Capture binds the current session to its Windows Terminal window+tab and
// records it in a sidecar under stateHome. It reads the focused window via UI
// Automation — at SessionStart the session's tab is the focused one, right
// after `claude` launches — so it never writes anything to the terminal (zero
// injection). No-op when the environment can't support focus; skipped if the
// session is already bound (delete the sidecar to force a re-bind).
//
// The read runs in a detached child off the hook's critical path, so a slow
// powershell cold start never blocks Claude's startup. The cost of the
// zero-injection approach is timing: if focus has already moved off the
// session's window when the read runs, Capture binds nothing rather than the
// wrong window (it only accepts a focused Windows Terminal window).
func Capture(stateHome, sessionID string) error {
	if !Enabled() || !safeSessionID(sessionID) {
		Debugf("capture skip: enabled=%v sid=%q", Enabled(), sessionID)
		return nil
	}
	if _, ok := ReadBinding(stateHome, sessionID); ok {
		Debugf("capture sid=%s: already bound, skipping", sessionID)
		return nil
	}

	b, ok, err := captureStable(focusedWindow, retryDelay)
	Debugf("capture sid=%s stable=%v hwnd=%q tabRID=%q err=%v", sessionID, ok, b.HWND, b.TabRID, err)
	if err != nil {
		return err
	}
	if !ok {
		// Focus never settled on one window across reads — ambiguous, so bind
		// nothing rather than risk the wrong window.
		return nil
	}
	return writeBinding(stateHome, sessionID, b)
}

// captureStable reads the focused window up to captureAttempts times, spacing
// reads by delay, and returns a binding only once the same window+tab is seen
// stableReads times in a row. ok is false when focus never settled (return the
// zero Binding); err is the last read error and is only meaningful when every
// read failed (ok false, streak never started). A successful read is never the
// zero Binding (focusedWindow errors on an empty HWND), so the initial
// last == Binding{} can't false-match.
func captureStable(read func() (Binding, error), delay time.Duration) (b Binding, ok bool, err error) {
	var (
		last    Binding
		streak  int
		lastErr error
	)
	for attempt := 1; attempt <= captureAttempts; attempt++ {
		got, rerr := read()
		if rerr != nil {
			lastErr = rerr
		} else {
			if got == last {
				streak++
			} else {
				last, streak = got, 1
			}
			if streak >= stableReads {
				return last, true, nil
			}
		}
		if attempt < captureAttempts {
			time.Sleep(delay)
		}
	}
	if streak == 0 {
		return Binding{}, false, lastErr // every attempt errored
	}
	return Binding{}, false, nil
}

// focusedWindow returns the binding for the currently focused Windows Terminal
// window+tab via the embedded managed-UIA script, or an error if the focused
// top-level window is not a Windows Terminal window.
func focusedWindow() (Binding, error) {
	cmd := exec.Command("powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encodePS(focusedScript()))
	out, err := cmd.Output()
	if err != nil {
		return Binding{}, err
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 || fields[0] == "" {
		return Binding{}, errors.New("winfocus: focused window is not Windows Terminal")
	}
	b := Binding{HWND: fields[0]}
	if len(fields) >= 2 && validRID(fields[1]) {
		b.TabRID = fields[1]
	}
	return b, nil
}

// writeBinding atomically records the binding at <stateHome>/windows/<sessionID>
// as "HWND:TABRID" (the ":TABRID" suffix omitted when the tab is unknown).
func writeBinding(stateHome, sessionID string, b Binding) error {
	dir := filepath.Join(stateHome, sidecarDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	val := b.HWND
	if b.TabRID != "" {
		val += ":" + b.TabRID
	}
	path := filepath.Join(dir, sessionID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(val+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// safeSessionID rejects empty IDs and anything that could escape the sidecar
// directory when used as a filename. Session IDs are UUIDs from Claude Code's
// hook payload; this is a cheap guard against a crafted value writing or reading
// outside <stateHome>/windows.
func safeSessionID(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, `/\`)
}

// ReadBinding returns the bound window/tab for sessionID, or ok=false if unbound.
func ReadBinding(stateHome, sessionID string) (Binding, bool) {
	if !safeSessionID(sessionID) {
		return Binding{}, false
	}
	data, err := os.ReadFile(filepath.Join(stateHome, sidecarDir, sessionID))
	if err != nil {
		return Binding{}, false
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return Binding{}, false
	}
	hwnd, rid, hasTab := strings.Cut(s, ":")
	b := Binding{HWND: hwnd}
	if hasTab && validRID(rid) {
		b.TabRID = rid
	}
	return b, true
}

// encodePS encodes a PowerShell script for -EncodedCommand: UTF-16LE then
// base64. Sidesteps all the cmd/WSL quoting pitfalls of passing a script inline.
func encodePS(script string) string {
	u := utf16.Encode([]rune(script))
	buf := make([]byte, 0, len(u)*2)
	for _, c := range u {
		buf = append(buf, byte(c), byte(c>>8))
	}
	return base64.StdEncoding.EncodeToString(buf)
}

// focusedScript walks from the UIA focused element up to its top-level window
// and, if that window is a Windows Terminal window, prints
// "<hwnd> <selectedTabIndex>". Managed UIA only — no inline C#, so no per-call
// compiler cost. Exits 1 if the focused window isn't Windows Terminal.
func focusedScript() string {
	return `$ErrorActionPreference='SilentlyContinue'
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
$A=[System.Windows.Automation.AutomationElement]
$root=$A::RootElement
$f=$A::FocusedElement
if($null -eq $f){ exit 1 }
$w=[System.Windows.Automation.TreeWalker]::ControlViewWalker
$top=$f; $cur=$f
while($true){ $p=$w.GetParent($cur); if($null -eq $p -or $p -eq $root){ break }; $top=$p; $cur=$p }
if($top.Current.ClassName -ne 'CASCADIA_HOSTING_WINDOW_CLASS'){ exit 1 }
$hwnd=[int64]$top.Current.NativeWindowHandle
$si=[System.Windows.Automation.SelectionItemPattern]::Pattern
$cond=[System.Windows.Automation.Condition]::TrueCondition
$scope=[System.Windows.Automation.TreeScope]::Descendants
$rid=''
foreach($e in $top.FindAll($scope,$cond)){ $s=$null; try{$s=$e.GetCurrentPattern($si)}catch{}; if($s -and $s.Current.IsSelected){ $rid=($e.GetRuntimeId() -join '.'); break } }
[Console]::Out.Write($hwnd.ToString()+' '+$rid); exit 0
`
}

// validRID accepts only a non-empty run of ASCII digits and dots — the shape of
// a dot-joined UIA RuntimeId. It is interpolated into a PowerShell string
// comparison, so this both rejects garbage and prevents script injection via
// the sidecar.
func validRID(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if (s[i] < '0' || s[i] > '9') && s[i] != '.' {
			return false
		}
	}
	return true
}
