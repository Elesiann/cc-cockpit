//go:build linux

package dashboard

import "golang.org/x/sys/unix"

// enableCharInput switches stdin to character-at-a-time, no-echo input while
// leaving output processing (ONLCR) and signal generation (ISIG) intact — so
// the existing "\n"-terminated frame printing still works and Ctrl-C still
// raises SIGINT for the clean-exit path. Returns a restore func, or an error
// if fd is not a terminal.
func enableCharInput(fd int) (func(), error) {
	old, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return nil, err
	}
	raw := *old
	raw.Lflag &^= unix.ICANON | unix.ECHO
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, unix.TCSETS, &raw); err != nil {
		return nil, err
	}
	return func() { _ = unix.IoctlSetTermios(fd, unix.TCSETS, old) }, nil
}

// termSize returns the terminal's row/column count for fd, or ok=false if fd is
// not a terminal. Used to clamp the frame so it never overflows the viewport
// (overflow scrolls the alt-screen on every repaint, which reads as flicker).
func termSize(fd int) (rows, cols int, ok bool) {
	ws, err := unix.IoctlGetWinsize(fd, unix.TIOCGWINSZ)
	if err != nil {
		return 0, 0, false
	}
	return int(ws.Row), int(ws.Col), true
}
