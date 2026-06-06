package dashboard

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// chunkReader returns its chunks one Read at a time, simulating a terminal that
// delivers an escape sequence split across reads (VMIN=1 behavior).
type chunkReader struct {
	chunks [][]byte
	i      int
}

func (c *chunkReader) Read(p []byte) (int, error) {
	if c.i >= len(c.chunks) {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[c.i])
	c.i++
	return n, nil
}

func TestReadKeysSplitEscape(t *testing.T) {
	// ESC, '[', 'A' across three reads, then ESC '[' / 'B' across two, Enter.
	r := &chunkReader{chunks: [][]byte{{0x1b}, {'['}, {'A'}, {0x1b, '['}, {'B'}, {'\r'}}}
	ch := make(chan key, 16)
	readKeys(r, ch)
	close(ch)

	var got []key
	for k := range ch {
		got = append(got, k)
	}
	want := []key{keyUp, keyDown, keyEnter}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("key %d: got %d, want %d", i, got[i], want[i])
		}
	}
}

func TestReadKeysDecodes(t *testing.T) {
	// Up, Down (CSI), Enter, then vim j/k, then q. readKeys returns at EOF.
	in := bytes.NewReader([]byte{0x1b, '[', 'A', 0x1b, '[', 'B', '\r', 'j', 'k', 'q'})
	ch := make(chan key, 16)
	readKeys(in, ch)
	close(ch)

	var got []key
	for k := range ch {
		got = append(got, k)
	}
	want := []key{keyUp, keyDown, keyEnter, keyDown, keyUp, keyQuit}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("key %d: got %d, want %d", i, got[i], want[i])
		}
	}
}

func TestMarkSelectedRow(t *testing.T) {
	Selected = "s2"
	defer func() { Selected = "" }()

	table := "─── active ───\n  STATUS\n  rowA\n  rowB\n"
	sids := []string{"s1", "s2"}
	out := markSelectedRow(table, sids, 2)

	if !strings.Contains(out, "▸ rowB") {
		t.Fatalf("selected row not marked:\n%s", out)
	}
	if !strings.Contains(out, "  rowA") {
		t.Fatalf("unselected row should keep its indent:\n%s", out)
	}
}

func TestMarkSelectedRowNoneSelected(t *testing.T) {
	Selected = ""
	table := "  STATUS\n  rowA\n"
	if got := markSelectedRow(table, []string{"s1"}, 1); got != table {
		t.Fatalf("with no selection the table must be unchanged, got:\n%s", got)
	}
}
