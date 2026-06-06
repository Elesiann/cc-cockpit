// Package winfocus brings a Claude session's Windows Terminal window to the
// foreground from WSL. Everything here is a no-op unless the process is running
// under WSL and was launched from Windows Terminal.
//
// The mechanism (validated by the spike under spike/wt-focus) is:
//
//   - capture: from inside the session, write a unique marker to the session's
//     pseudo-terminal, then ask Windows (via powershell.exe + UI Automation) which
//     Windows Terminal window's buffer contains the marker. That window's HWND is
//     the session's window.
//   - focus: SetForegroundWindow(HWND) (with the AttachThreadInput trick) raises it.
//
// Windows Terminal runs all its windows in a single process and does not expose
// its tab title via Win32/UIA, so neither PID nor window title can identify the
// owning window — only buffer content can. Hence the marker dance.
package winfocus

import (
	"os"
	"path/filepath"
	"strconv"
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

// FindSessionPTS walks the process ancestry and returns the controlling
// pseudo-terminal of the session (e.g. "/dev/pts/3"), or "" if none is found.
//
// Claude Code spawns its children (including this hook) with setsid, so the
// caller has no controlling terminal of its own — but its parent `claude` still
// owns the Windows Terminal pts. We climb parents and return the first
// /dev/pts/N any of them has open on stdio. setsid changes the session, not the
// parent link, so the PPID chain stays intact.
func FindSessionPTS() string {
	pid := os.Getppid()
	for i := 0; i < 16 && pid > 1; i++ {
		if pts := ptsOfPID(pid); pts != "" {
			return pts
		}
		ppid, ok := parentPID(pid)
		if !ok {
			break
		}
		pid = ppid
	}
	return ""
}

func ptsOfPID(pid int) string {
	for _, fd := range []string{"0", "1", "2"} {
		target, err := os.Readlink(filepath.Join("/proc", strconv.Itoa(pid), "fd", fd))
		if err == nil && strings.HasPrefix(target, "/dev/pts/") {
			return target
		}
	}
	return ""
}

func parentPID(pid int) (int, bool) {
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, false
	}
	return parsePPID(string(data))
}

// parsePPID extracts the parent PID (field 4) from a /proc/<pid>/stat line. The
// comm field (field 2) is wrapped in parens and can itself contain spaces or
// parens, so we parse the fixed fields after the final ')'.
func parsePPID(stat string) (int, bool) {
	closeParen := strings.LastIndexByte(stat, ')')
	if closeParen < 0 || closeParen+1 >= len(stat) {
		return 0, false
	}
	// After ')': field[0]=state, field[1]=ppid, ...
	fields := strings.Fields(stat[closeParen+1:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}
