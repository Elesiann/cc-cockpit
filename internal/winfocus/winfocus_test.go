package winfocus

import "testing"

func TestParsePPID(t *testing.T) {
	cases := []struct {
		name string
		stat string
		want int
	}{
		{"simple comm", "1234 (bash) S 1200 1234 1234 34816 1234 4194304 ...", 1200},
		{"comm with spaces", "44921 (Claude Code) S 44900 44921 44900 0 -1 ...", 44900},
		{"comm with parens", "55 (weird (proc) name) R 42 55 55 0 -1 ...", 42},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parsePPID(c.stat)
			if !ok || got != c.want {
				t.Fatalf("parsePPID(%q) = (%d,%v), want (%d,true)", c.stat, got, ok, c.want)
			}
		})
	}
}

func TestParsePPIDGarbage(t *testing.T) {
	for _, s := range []string{"", "()", "no parens here", "1 (x)", "1 (x) S"} {
		if _, ok := parsePPID(s); ok {
			t.Errorf("parsePPID(%q) unexpectedly ok", s)
		}
	}
}
