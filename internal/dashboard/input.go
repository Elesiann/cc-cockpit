package dashboard

import "io"

// key is a decoded keypress from the interactive watch input loop.
type key int

const (
	keyNone key = iota
	keyUp
	keyDown
	keyEnter
	keyQuit
)

// readKeys decodes keypresses from r and sends them on ch until r errors
// (e.g. on shutdown). It recognizes arrow keys (CSI A/B), Enter, vim-style
// j/k, and q. Escape sequences are expected to arrive within a single Read,
// which holds for terminal input in practice; a sequence split across reads is
// simply ignored rather than mis-decoded.
func readKeys(r io.Reader, ch chan<- key) {
	buf := make([]byte, 16)
	for {
		n, err := r.Read(buf)
		if err != nil {
			return
		}
		for i := 0; i < n; i++ {
			switch b := buf[i]; {
			case b == 0x1b: // ESC: maybe a CSI arrow sequence
				if i+2 < n && buf[i+1] == '[' {
					switch buf[i+2] {
					case 'A':
						ch <- keyUp
					case 'B':
						ch <- keyDown
					}
					i += 2
				}
			case b == '\r' || b == '\n':
				ch <- keyEnter
			case b == 'k':
				ch <- keyUp
			case b == 'j':
				ch <- keyDown
			case b == 'q':
				ch <- keyQuit
			}
		}
	}
}
