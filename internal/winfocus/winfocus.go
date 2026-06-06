// Package winfocus brings a Claude session's Windows Terminal window to the
// foreground from WSL. Everything here is a no-op unless the process is running
// under WSL and was launched from Windows Terminal.
//
// The mechanism (validated by the spike under spike/wt-focus) is:
//
//   - capture: at SessionStart the session's tab is the focused UI element, so
//     UI Automation FocusedElement → its top-level window gives the owning
//     Windows Terminal window HWND and selected tab index. No terminal output.
//   - focus: select the tab via UIA and SetFocus() the window to raise it.
//
// Windows Terminal runs all its windows in one process and exposes neither
// WT_SESSION nor the OSC title via a readable UIA property, so the focused
// window at launch is the only zero-injection handle on the right window.
package winfocus

import (
	"os"
	"strings"
)

// Enabled reports whether window focus is possible in this environment: running
// inside WSL (so the Windows-side powershell.exe interop exists) and launched
// from Windows Terminal (which sets WT_SESSION). Both are required.
func Enabled() bool {
	return isWSL() && os.Getenv("WT_SESSION") != ""
}

func isWSL() bool {
	if os.Getenv("WSL_DISTRO_NAME") != "" {
		return true
	}
	// Fallback: the WSL2 kernel release string carries "microsoft".
	data, err := os.ReadFile("/proc/sys/kernel/osrelease")
	if err != nil {
		return false
	}
	return strings.Contains(strings.ToLower(string(data)), "microsoft")
}
