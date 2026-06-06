package winfocus

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// debugLogPath is where Debugf appends. The window-binding path runs detached
// and silent (spawned by the SessionStart hook), so a log file is the only way
// to see why a bind succeeded or failed.
var debugLogPath = filepath.Join(os.TempDir(), "cc-cockpit-winfocus.log")

// Debugf appends a timestamped line to the winfocus debug log. Best-effort: any
// error (including a read-only tmp) is swallowed — diagnostics must never break
// the hook.
func Debugf(format string, args ...any) {
	f, err := os.OpenFile(debugLogPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s "+format+"\n", append([]any{time.Now().Format(time.RFC3339)}, args...)...)
}
