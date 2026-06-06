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

// sidecarDir is the per-workspace subdirectory holding session->HWND bindings.
const sidecarDir = "windows"

// markerSettleDelay gives the marker time to render into the Windows Terminal
// buffer before UI Automation reads it. powershell.exe cold-start usually
// covers this on its own, but a small explicit wait makes capture reliable.
const markerSettleDelay = 250 * time.Millisecond

// Capture binds the current session to its Windows Terminal window: it writes a
// unique marker to the session's pts, asks Windows which WT window's buffer
// contains it, and records that HWND in a sidecar under stateHome. No-op (nil)
// when the environment can't support focus or the pts can't be found.
//
// pts may be empty, in which case it is resolved from the process ancestry. The
// hook passes it explicitly because it resolves the pts while still parented to
// claude, then runs Capture in a detached child whose ancestry no longer leads
// there.
//
// This is slow (cold powershell.exe + UIA enumeration), so callers should run
// it off the hook's critical path.
func Capture(stateHome, sessionID, pts string) error {
	if !Enabled() || sessionID == "" {
		return nil
	}
	if pts == "" {
		pts = FindSessionPTS()
	}
	if pts == "" {
		return errors.New("winfocus: no controlling pts found in ancestry")
	}

	marker := markerFor(sessionID)
	if err := stampAndClear(pts, marker); err != nil {
		return err
	}

	hwnd, err := scanForMarker(marker)
	if err != nil {
		return err
	}
	return writeSidecar(stateHome, sessionID, hwnd)
}

// markerFor builds a token unlikely to collide with real terminal content.
func markerFor(sessionID string) string {
	return "[[cc-cockpit-focus:" + sessionID + "]]"
}

// stampAndClear writes the marker to the pts, waits for it to render, and then
// clears the line. The marker stays visible only for markerSettleDelay plus the
// scan; the surrounding save/restore-cursor + clear-line keeps the disturbance
// to a single transient line. Best-effort: a live TUI may redraw over it, which
// is fine — the scan reads the buffer in between.
func stampAndClear(pts, marker string) error {
	f, err := os.OpenFile(pts, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	// DECSC (save cursor) + marker, so we can restore afterwards.
	if _, err := f.WriteString("\0337" + marker); err != nil {
		return err
	}
	time.Sleep(markerSettleDelay)
	// CR + clear whole line + DECRC (restore cursor).
	_, err = f.WriteString("\r\033[2K\0338")
	return err
}

// scanForMarker runs the embedded PowerShell + UI Automation scan and returns
// the decimal HWND of the Windows Terminal window whose buffer contains marker.
// A non-match exits the script with code 1, surfaced here as an error.
func scanForMarker(marker string) (string, error) {
	cmd := exec.Command("powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encodePS(buildScanScript(marker)))
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	hwnd := strings.TrimSpace(string(out))
	if hwnd == "" {
		return "", errors.New("winfocus: no WT window matched the marker")
	}
	return hwnd, nil
}

// writeSidecar atomically records hwnd at <stateHome>/windows/<sessionID>.
func writeSidecar(stateHome, sessionID, hwnd string) error {
	dir := filepath.Join(stateHome, sidecarDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, sessionID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(hwnd+"\n"), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ReadHWND returns the bound HWND for sessionID, or ("", false) if unbound.
func ReadHWND(stateHome, sessionID string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(stateHome, sidecarDir, sessionID))
	if err != nil {
		return "", false
	}
	hwnd := strings.TrimSpace(string(data))
	if hwnd == "" {
		return "", false
	}
	return hwnd, true
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

// buildScanScript embeds marker (single-quoted, with PS escaping) into the
// window-scan script. It enumerates Windows Terminal windows, reads each
// window's terminal buffer via UI Automation TextPattern, and prints the HWND
// of the first whose buffer contains the marker.
func buildScanScript(marker string) string {
	q := strings.ReplaceAll(marker, "'", "''")
	return `$ErrorActionPreference='SilentlyContinue'
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
$marker = '` + q + `'
Add-Type @"
using System; using System.Text; using System.Collections.Generic; using System.Runtime.InteropServices;
public static class W {
  public delegate bool E(IntPtr h, IntPtr l);
  [DllImport("user32.dll")] public static extern bool EnumWindows(E cb, IntPtr p);
  [DllImport("user32.dll", CharSet=CharSet.Unicode)] public static extern int GetClassName(IntPtr h, StringBuilder s, int n);
  public static List<IntPtr> T(string cls){ var r=new List<IntPtr>(); EnumWindows(delegate(IntPtr h, IntPtr l){ var sb=new StringBuilder(256); GetClassName(h,sb,sb.Capacity); if(sb.ToString()==cls) r.Add(h); return true;}, IntPtr.Zero); return r; }
}
"@
$tp=[System.Windows.Automation.TextPattern]::Pattern
$cond=[System.Windows.Automation.Condition]::TrueCondition
$scope=[System.Windows.Automation.TreeScope]::Descendants
foreach($h in [W]::T('CASCADIA_HOSTING_WINDOW_CLASS')){
  try{
    $el=[System.Windows.Automation.AutomationElement]::FromHandle($h)
    $els=@($el)+$el.FindAll($scope,$cond)
    foreach($e in $els){
      $p=$null; try{$p=$e.GetCurrentPattern($tp)}catch{}
      if($p){ $txt=$p.DocumentRange.GetText(-1); if($txt -and $txt.Contains($marker)){ [Console]::Out.Write([int64]$h); exit 0 } }
    }
  }catch{}
}
exit 1
`
}
