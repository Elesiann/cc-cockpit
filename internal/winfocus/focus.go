package winfocus

import (
	"errors"
	"os/exec"
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
	// Drop an unvalidated RID rather than interpolate it: the RID is embedded in
	// the script as a string literal, so this matches the warm focuser's guard
	// and keeps the cold path injection-safe even via the focus-window CLI.
	rid := b.TabRID
	if rid != "" && !validRID(rid) {
		rid = ""
	}
	cmd := exec.Command("powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encodePS(buildFocusScript(hwnd, rid)))
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

// buildFocusScript interpolates the (validated, numeric) hwnd and tab RuntimeId
// into a raise script that uses only managed UI Automation — no inline C#
// Add-Type, which would invoke the C# compiler on every call and add ~half a
// second. When tabRID is non-empty it first selects the tab whose RuntimeId
// matches (so tab reorders/closes can't land on the wrong one), un-minimizes
// via WindowPattern, then SetFocus() to bring the window forward.
func buildFocusScript(hwnd, tabRID string) string {
	tabBlock := ""
	if tabRID != "" {
		// Fail closed: if the bound tab's RuntimeId isn't present (tab closed,
		// stale sidecar, UIA elements recreated), exit WITHOUT focusing rather
		// than fall through and SetFocus on whatever sibling tab is active —
		// raising the wrong session is worse than doing nothing.
		tabBlock = `
$si=[System.Windows.Automation.SelectionItemPattern]::Pattern
$found=$false
foreach($e in $el.FindAll($scope,$cond)){ if(($e.GetRuntimeId() -join '.') -eq '` + tabRID + `'){ $found=$true; $s=$null; try{$s=$e.GetCurrentPattern($si)}catch{}; if($s){ try{$s.Select()}catch{} }; break } }
if(-not $found){ exit 2 }
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
