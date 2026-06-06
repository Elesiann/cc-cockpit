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

// markerSettleDelay gives the marker time to render into the Windows Terminal
// buffer before UI Automation reads it. powershell.exe cold-start usually
// covers this on its own, but a small explicit wait makes capture reliable.
const markerSettleDelay = 250 * time.Millisecond

// captureAttempts re-stamps and re-scans this many times before giving up. At
// SessionStart Claude is actively repainting and can wipe the marker before UI
// Automation reads it; restamping on each attempt eventually catches a quiet
// moment. retryDelay spaces the attempts.
const (
	captureAttempts = 6
	retryDelay      = 400 * time.Millisecond
)

// Binding is a session's resolved Windows Terminal location: the window handle
// plus the index of its tab (Tab < 0 means "window only / tab unknown").
type Binding struct {
	HWND string
	Tab  int
}

// Capture binds the current session to its Windows Terminal window+tab: it
// writes a unique marker to the session's pts, asks Windows which WT window's
// buffer contains it (and which tab is selected — at SessionStart that's this
// session's tab), and records the binding in a sidecar under stateHome. No-op
// (nil) when the environment can't support focus or the pts can't be found.
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
		Debugf("capture skip: enabled=%v sid=%q", Enabled(), sessionID)
		return nil
	}
	if pts == "" {
		pts = FindSessionPTS()
	}
	if pts == "" {
		Debugf("capture sid=%s: no controlling pts in ancestry", sessionID)
		return errors.New("winfocus: no controlling pts found in ancestry")
	}

	marker := markerFor(sessionID)
	var b Binding
	var scanErr error
	// Order matters: stamp the marker, let it render, scan for it, THEN clear
	// it. Retry because at SessionStart Claude's repaint can wipe the marker
	// before UI Automation reads it; each attempt re-stamps a fresh marker.
	for attempt := 1; attempt <= captureAttempts; attempt++ {
		if err := writeToPTS(pts, "\r"+marker); err != nil {
			return err
		}
		time.Sleep(markerSettleDelay)
		b, scanErr = scanForMarker(marker)
		_ = writeToPTS(pts, "\r\033[2K") // best-effort clear
		Debugf("capture sid=%s pts=%s attempt=%d hwnd=%q tab=%d err=%v", sessionID, pts, attempt, b.HWND, b.Tab, scanErr)
		if scanErr == nil {
			break
		}
		time.Sleep(retryDelay)
	}
	if scanErr != nil {
		return scanErr
	}
	return writeBinding(stateHome, sessionID, b)
}

// markerFor builds a token unlikely to collide with real terminal content.
func markerFor(sessionID string) string {
	return "[[cc-cockpit-focus:" + sessionID + "]]"
}

// writeToPTS writes s to the pseudo-terminal device. Writing to the pts slave
// outputs to the terminal display, so the bytes land in the buffer that UI
// Automation reads.
func writeToPTS(pts, s string) error {
	f, err := os.OpenFile(pts, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(s)
	return err
}

// scanForMarker runs the embedded PowerShell + UI Automation scan and returns
// the binding for the Windows Terminal window whose buffer contains marker. A
// non-match exits the script with code 1, surfaced here as an error.
func scanForMarker(marker string) (Binding, error) {
	cmd := exec.Command("powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encodePS(buildScanScript(marker)))
	out, err := cmd.Output()
	if err != nil {
		return Binding{Tab: -1}, err
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) == 0 || fields[0] == "" {
		return Binding{Tab: -1}, errors.New("winfocus: no WT window matched the marker")
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

// buildScanScript embeds marker (single-quoted, with PS escaping) into the
// window-scan script. It enumerates Windows Terminal windows, reads each
// window's terminal buffer via UI Automation TextPattern, and on a match prints
// "<hwnd> <selectedTabIndex>" — the selected tab being this session's tab at
// SessionStart.
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
$si=[System.Windows.Automation.SelectionItemPattern]::Pattern
$cond=[System.Windows.Automation.Condition]::TrueCondition
$scope=[System.Windows.Automation.TreeScope]::Descendants
foreach($h in [W]::T('CASCADIA_HOSTING_WINDOW_CLASS')){
  try{
    $el=[System.Windows.Automation.AutomationElement]::FromHandle($h)
    $els=@($el)+$el.FindAll($scope,$cond)
    $found=$false
    foreach($e in $els){
      $p=$null; try{$p=$e.GetCurrentPattern($tp)}catch{}
      if($p){ $txt=$p.DocumentRange.GetText(-1); if($txt -and $txt.Contains($marker)){ $found=$true; break } }
    }
    if($found){
      $tab=-1; $idx=0
      foreach($e in $els){ $s=$null; try{$s=$e.GetCurrentPattern($si)}catch{}; if($s){ if($s.Current.IsSelected){$tab=$idx}; $idx++ } }
      [Console]::Out.Write(([int64]$h).ToString()+' '+$tab.ToString()); exit 0
    }
  }catch{}
}
exit 1
`
}
