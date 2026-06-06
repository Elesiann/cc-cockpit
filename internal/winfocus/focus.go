package winfocus

import (
	"errors"
	"os/exec"
	"strings"
)

// Focus raises the Windows Terminal window with the given decimal HWND to the
// foreground. It is meant to be called from the cockpit window (the one the
// operator is looking at), which holds foreground rights — so the
// AttachThreadInput dance in the script reliably wins over Windows'
// foreground-stealing guard.
func Focus(hwnd string) error {
	hwnd = strings.TrimSpace(hwnd)
	if !validHWND(hwnd) {
		return errors.New("winfocus: invalid HWND")
	}
	cmd := exec.Command("powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encodePS(buildFocusScript(hwnd)))
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

// buildFocusScript interpolates the (validated, numeric) hwnd into the raise
// script. Restores a minimized window, attaches to the current foreground
// thread's input queue, then SetForegroundWindow. Exits 0 on success.
func buildFocusScript(hwnd string) string {
	return `$ErrorActionPreference='SilentlyContinue'
Add-Type @"
using System; using System.Runtime.InteropServices;
public static class F {
  [DllImport("user32.dll")] public static extern bool SetForegroundWindow(IntPtr h);
  [DllImport("user32.dll")] public static extern bool ShowWindow(IntPtr h, int c);
  [DllImport("user32.dll")] public static extern bool BringWindowToTop(IntPtr h);
  [DllImport("user32.dll")] public static extern IntPtr GetForegroundWindow();
  [DllImport("user32.dll")] public static extern bool IsIconic(IntPtr h);
  [DllImport("user32.dll")] public static extern uint GetWindowThreadProcessId(IntPtr h, out uint pid);
  [DllImport("user32.dll")] public static extern bool AttachThreadInput(uint a, uint b, bool f);
  [DllImport("kernel32.dll")] public static extern uint GetCurrentThreadId();
}
"@
$h=[IntPtr][int64]` + hwnd + `
if([F]::IsIconic($h)){[void][F]::ShowWindow($h,9)}
$fg=[F]::GetForegroundWindow(); $p=[uint32]0; $ft=[F]::GetWindowThreadProcessId($fg,[ref]$p); $mt=[F]::GetCurrentThreadId()
$att=$false; if($ft -ne $mt){$att=[F]::AttachThreadInput($mt,$ft,$true)}
[void][F]::BringWindowToTop($h); $ok=[F]::SetForegroundWindow($h); [void][F]::ShowWindow($h,5)
if($att){[void][F]::AttachThreadInput($mt,$ft,$false)}
if($ok){exit 0}else{exit 1}
`
}
