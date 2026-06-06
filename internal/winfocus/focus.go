package winfocus

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

// Focus brings the bound Windows Terminal window to the foreground, first
// selecting the session's tab (when known) so the right session is shown — not
// just whatever tab happened to be active. Meant to be called from the cockpit
// window (which holds foreground rights), so the AttachThreadInput dance in the
// script reliably wins over Windows' foreground-stealing guard.
func Focus(b Binding) error {
	hwnd := strings.TrimSpace(b.HWND)
	if !validHWND(hwnd) {
		return errors.New("winfocus: invalid HWND")
	}
	cmd := exec.Command("powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encodePS(buildFocusScript(hwnd, b.Tab)))
	return cmd.Run()
}

// validHWND accepts only a non-empty run of ASCII digits. The value is
// interpolated into a PowerShell script, so this both rejects garbage and
// prevents script injection via the sidecar.
func validHWND(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// buildFocusScript interpolates the (validated, numeric) hwnd and tab index into
// a raise script that uses only managed UI Automation — no inline C# Add-Type,
// which would invoke the C# compiler on every call and add ~half a second.
// When tab >= 0 it first selects that tab (same Descendants+SelectionItem
// ordering capture used), un-minimizes via WindowPattern, then SetFocus() to
// bring the window forward.
func buildFocusScript(hwnd string, tab int) string {
	tabBlock := ""
	if tab >= 0 {
		tabBlock = `
$si=[System.Windows.Automation.SelectionItemPattern]::Pattern
$idx=0
foreach($e in $el.FindAll($scope,$cond)){ $s=$null; try{$s=$e.GetCurrentPattern($si)}catch{}; if($s){ if($idx -eq ` + strconv.Itoa(tab) + `){ try{$s.Select()}catch{}; break }; $idx++ } }
`
	}
	return `$ErrorActionPreference='SilentlyContinue'
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
$cond=[System.Windows.Automation.Condition]::TrueCondition
$scope=[System.Windows.Automation.TreeScope]::Descendants
$h=[IntPtr][int64]` + hwnd + `
$el=[System.Windows.Automation.AutomationElement]::FromHandle($h)
if(-not $el){ exit 1 }
# FromHandle silently resolves a dead/reused handle to some other live window,
# so verify the element actually reports the handle we asked for before acting —
# otherwise a stale binding focuses an arbitrary window.
if([int64]$el.Current.NativeWindowHandle -ne ` + hwnd + `){ exit 1 }` + tabBlock + `
try{ $wp=$el.GetCurrentPattern([System.Windows.Automation.WindowPattern]::Pattern); if($wp){ $wp.SetWindowVisualState([System.Windows.Automation.WindowVisualState]::Normal) } }catch{}
# Focus the active tab's terminal content so the keyboard goes to the prompt.
$tc=$null
foreach($e in $el.FindAll($scope,$cond)){ if($e.Current.ClassName -eq 'TermControl' -and -not $e.Current.IsOffscreen){ $tc=$e; break } }
if($null -ne $tc){ try{$tc.SetFocus()}catch{} } else { try{$el.SetFocus()}catch{} }
exit 0
`
}
