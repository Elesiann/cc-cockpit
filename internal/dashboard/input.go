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
// j/k, and q. Because stdin is in VMIN=1 mode, a multi-byte escape sequence can
// arrive split across reads, so unconsumed bytes are carried over to the next
// read rather than dropped — otherwise arrow presses would intermittently go
// missing and feel laggy.
func readKeys(r io.Reader, ch chan<- key) {
	var pending []byte
	buf := make([]byte, 16)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			pending = parseKeys(append(pending, buf[:n]...), ch)
		}
		if err != nil {
			return
		}
	}
}

// parseKeys consumes complete key tokens from b, emits them on ch, and returns
// the unconsumed tail (an incomplete escape sequence awaiting more bytes).
func parseKeys(b []byte, ch chan<- key) []byte {
	i := 0
	for i < len(b) {
		switch c := b[i]; {
		case c == 0x1b: // ESC — possibly a CSI arrow sequence
			if i+1 >= len(b) {
				return b[i:] // incomplete: wait for more
			}
			if b[i+1] == '[' {
				if i+2 >= len(b) {
					return b[i:] // incomplete CSI: wait for more
				}
				switch b[i+2] {
				case 'A':
					ch <- keyUp
				case 'B':
					ch <- keyDown
				}
				i += 3
				continue
			}
			i++ // lone ESC or unhandled sequence
		case c == '\r' || c == '\n':
			ch <- keyEnter
			i++
		case c == 'k':
			ch <- keyUp
			i++
		case c == 'j':
			ch <- keyDown
			i++
		case c == 'q':
			ch <- keyQuit
			i++
		default:
			i++
		}
	}
	return nil
}
