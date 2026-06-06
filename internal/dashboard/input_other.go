//go:build !linux

package dashboard

import "errors"

// enableCharInput is unsupported off Linux. The interactive selector only runs
// under WSL (gated by winfocus.Enabled), so non-Linux builds never reach a
// working path; this stub keeps them compiling.
func enableCharInput(fd int) (func(), error) {
	return nil, errors.New("interactive input unsupported on this platform")
}
