package winfocus

import (
	"errors"
	"testing"
)

type discardWC struct{}

func (discardWC) Write(p []byte) (int, error) { return len(p), nil }
func (discardWC) Close() error                { return nil }

// A Focuser used after Close must report ErrClosed (not a generic write error)
// so the caller can tell a shutdown race apart from a real focus failure and
// skip the cold-path fallback.
func TestFocuserClosedReturnsErrClosed(t *testing.T) {
	f := &Focuser{stdin: discardWC{}}
	if err := f.Focus(Binding{HWND: "1"}); err != nil {
		t.Fatalf("Focus before Close = %v, want nil", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close = %v, want nil", err)
	}
	if err := f.Focus(Binding{HWND: "1"}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Focus after Close = %v, want ErrClosed", err)
	}
}
