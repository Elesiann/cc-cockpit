package winfocus

import (
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

// sidecarDir is the per-workspace subdirectory holding session->window bindings.
const sidecarDir = "windows"

// captureAttempts retries the focused-window read in case focus is momentarily
// elsewhere when the detached capture runs. retryDelay spaces the attempts.
const (
	captureAttempts = 3
	retryDelay      = 300 * time.Millisecond
)

// Binding is a session's resolved Windows Terminal location: the window handle
// plus the index of its tab (Tab < 0 means "window only / tab unknown").
type Binding struct {
	HWND string
	Tab  int
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
	if !Enabled() || sessionID == "" {
		Debugf("capture skip: enabled=%v sid=%q", Enabled(), sessionID)
		return nil
	}
	if _, ok := ReadBinding(stateHome, sessionID); ok {
		Debugf("capture sid=%s: already bound, skipping", sessionID)
		return nil
	}

	var b Binding
	var err error
	for attempt := 1; attempt <= captureAttempts; attempt++ {
		b, err = focusedWindow()
		Debugf("capture sid=%s attempt=%d hwnd=%q tab=%d err=%v", sessionID, attempt, b.HWND, b.Tab, err)
		if err == nil {
			break
		}
		time.Sleep(retryDelay)
	}
	if err != nil {
		return err
	}
	return writeBinding(stateHome, sessionID, b)
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
		return Binding{Tab: -1}, err
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 || fields[0] == "" {
		return Binding{Tab: -1}, errors.New("winfocus: focused window is not Windows Terminal")
	}
	b := Binding{HWND: fields[0], Tab: -1}
	if len(fields) >= 2 {
		if n, e := strconv.Atoi(fields[1]); e == nil {
			b.Tab = n
		}
	}
	return b, nil
}

// writeBinding atomically records the binding at <stateHome>/windows/<sessionID>
// as "HWND:TAB" (TAB omitted when < 0).
func writeBinding(stateHome, sessionID string, b Binding) error {
	dir := filepath.Join(stateHome, sidecarDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	val := b.HWND
	if b.Tab >= 0 {
		val += ":" + strconv.Itoa(b.Tab)
	}
	path := filepath.Join(dir, sessionID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(val+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadBinding returns the bound window/tab for sessionID, or ok=false if unbound.
func ReadBinding(stateHome, sessionID string) (Binding, bool) {
	data, err := os.ReadFile(filepath.Join(stateHome, sidecarDir, sessionID))
	if err != nil {
		return Binding{}, false
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return Binding{}, false
	}
	hwnd, tabStr, hasTab := strings.Cut(s, ":")
	b := Binding{HWND: hwnd, Tab: -1}
	if hasTab {
		if n, e := strconv.Atoi(tabStr); e == nil {
			b.Tab = n
		}
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
$tab=-1; $idx=0
foreach($e in $top.FindAll($scope,$cond)){ $s=$null; try{$s=$e.GetCurrentPattern($si)}catch{}; if($s){ if($s.Current.IsSelected){$tab=$idx}; $idx++ } }
[Console]::Out.Write($hwnd.ToString()+' '+$tab.ToString()); exit 0
`
}
