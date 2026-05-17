package dashboard

import (
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// notifier sends a desktop notification. Resolved once at Run start so the
// per-tick cost is one nil-check + one exec.
type notifier struct {
	// describe is a short string the dashboard logs at boot so the operator
	// can tell what was chosen (or "none" when no backend was found).
	describe string
	// fn is the actual sender; nil means no-op.
	fn func(msg string)
}

func (n notifier) Notify(msg string) {
	if n.fn == nil {
		return
	}
	n.fn(msg)
}

// resolveNotifier picks the first available desktop-notification backend.
// Detection order:
//   - macOS → osascript (always present).
//   - WSL2 → wsl-notify-send.exe, then powershell BurntToast.
//   - Other Linux → notify-send.
//
// Falls back to no-op if nothing usable is on PATH. Probing happens once;
// per-tick cost after that is a single exec at attention time.
func resolveNotifier() notifier {
	switch runtime.GOOS {
	case "darwin":
		if p, err := exec.LookPath("osascript"); err == nil {
			return notifier{
				describe: "osascript",
				fn: func(msg string) {
					_ = exec.Command(p, "-e",
						`display notification "`+escapeAppleScript(msg)+`" with title "cc-cockpit"`,
					).Run()
				},
			}
		}
	case "linux":
		if isWSL() {
			if p, err := exec.LookPath("wsl-notify-send.exe"); err == nil {
				return notifier{
					describe: "wsl-notify-send.exe",
					fn: func(msg string) {
						_ = exec.Command(p, "--category", "cc-cockpit", msg).Run()
					},
				}
			}
			if p, err := exec.LookPath("powershell.exe"); err == nil {
				return notifier{
					describe: "powershell.exe (BurntToast)",
					fn: func(msg string) {
						script := `New-BurntToastNotification -Text 'cc-cockpit', '` + escapePowerShell(msg) + `'`
						_ = exec.Command(p, "-NoProfile", "-Command", script).Run()
					},
				}
			}
		}
		if p, err := exec.LookPath("notify-send"); err == nil {
			return notifier{
				describe: "notify-send",
				fn: func(msg string) {
					_ = exec.Command(p, "cc-cockpit", msg).Run()
				},
			}
		}
	}
	return notifier{describe: "none"}
}

// isWSL is a separate function so tests can stub the file read via
// detectWSLFromContent.
func isWSL() bool {
	data, err := os.ReadFile("/proc/version")
	if err != nil {
		return false
	}
	return detectWSLFromContent(string(data))
}

func detectWSLFromContent(s string) bool {
	low := strings.ToLower(s)
	return strings.Contains(low, "microsoft") || strings.Contains(low, "wsl")
}

// escapeAppleScript double-quotes are the AppleScript string delimiter; the
// notification text comes from session repo/task fields, which are largely
// safe ASCII but may contain quotes. Escape them and strip newlines.
func escapeAppleScript(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}

// escapePowerShell single-quoted PS strings only need single-quote doubling.
func escapePowerShell(s string) string {
	s = strings.ReplaceAll(s, "'", "''")
	s = strings.ReplaceAll(s, "\n", " ")
	return s
}
