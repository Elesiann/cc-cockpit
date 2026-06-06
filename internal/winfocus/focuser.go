package winfocus

import (
	"errors"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"sync"
)

// Focuser keeps a warm powershell.exe with UI Automation already loaded and
// drives it over stdin, so each focus is a pipe write instead of a fresh
// ~1s powershell + assembly cold start. It lives for the duration of an
// interactive watch and is killed on Close.
type Focuser struct {
	mu    sync.Mutex
	cmd   *exec.Cmd
	stdin io.WriteCloser
}

// NewFocuser spawns the warm helper. The caller must Close it.
func NewFocuser() (*Focuser, error) {
	cmd := exec.Command("powershell.exe",
		"-NoProfile", "-NonInteractive", "-ExecutionPolicy", "Bypass",
		"-EncodedCommand", encodePS(focuserLoopScript()))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &Focuser{cmd: cmd, stdin: stdin}, nil
}

// Focus sends one binding to the warm helper. Returns quickly: the actual raise
// happens in the helper. Safe for concurrent callers.
func (f *Focuser) Focus(b Binding) error {
	if f == nil {
		return errors.New("winfocus: nil focuser")
	}
	hwnd := strings.TrimSpace(b.HWND)
	if !validHWND(hwnd) {
		return errors.New("winfocus: invalid HWND")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	_, err := io.WriteString(f.stdin, hwnd+" "+strconv.Itoa(b.Tab)+"\n")
	return err
}

// Close stops the helper (closing stdin ends its read loop) and reaps it.
func (f *Focuser) Close() error {
	if f == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.stdin != nil {
		_ = f.stdin.Close()
	}
	if f.cmd != nil && f.cmd.Process != nil {
		go func(c *exec.Cmd) { _ = c.Wait() }(f.cmd)
	}
	return nil
}

// focuserLoopScript reads "<hwnd> <tab>" lines from stdin and focuses each via
// managed UI Automation. Assemblies are loaded once, up front, so per-line work
// is just the UIA calls.
func focuserLoopScript() string {
	return `$ErrorActionPreference='SilentlyContinue'
Add-Type -AssemblyName UIAutomationClient
Add-Type -AssemblyName UIAutomationTypes
$si=[System.Windows.Automation.SelectionItemPattern]::Pattern
$wpat=[System.Windows.Automation.WindowPattern]::Pattern
$cond=[System.Windows.Automation.Condition]::TrueCondition
$scope=[System.Windows.Automation.TreeScope]::Descendants
while($true){
  $line=[Console]::In.ReadLine()
  if($null -eq $line){ break }
  $line=$line.Trim()
  if($line -eq ''){ continue }
  $parts=$line.Split(' ')
  $hwnd=[int64]0
  if(-not [int64]::TryParse($parts[0],[ref]$hwnd)){ continue }
  $tab=-1
  if($parts.Length -ge 2){ $t=0; if([int]::TryParse($parts[1],[ref]$t)){ $tab=$t } }
  $el=[System.Windows.Automation.AutomationElement]::FromHandle([IntPtr]$hwnd)
  if($null -eq $el){ continue }
  if($tab -ge 0){ $idx=0; foreach($e in $el.FindAll($scope,$cond)){ $s=$null; try{$s=$e.GetCurrentPattern($si)}catch{}; if($s){ if($idx -eq $tab){ try{$s.Select()}catch{}; break }; $idx++ } } }
  try{ $w=$el.GetCurrentPattern($wpat); if($w){ $w.SetWindowVisualState([System.Windows.Automation.WindowVisualState]::Normal) } }catch{}
  try{ $el.SetFocus() }catch{}
}
`
}
