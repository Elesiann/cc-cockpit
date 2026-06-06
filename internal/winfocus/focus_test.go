package winfocus

import (
	"strings"
	"testing"
)

func TestValidHWND(t *testing.T) {
	ok := []string{"0", "590104", "123537342"}
	bad := []string{"", " ", "59x104", "0x1234", "-5", "12 34", "12;calc"}
	for _, s := range ok {
		if !validHWND(s) {
			t.Errorf("validHWND(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if validHWND(s) {
			t.Errorf("validHWND(%q) = true, want false", s)
		}
	}
}

func TestBuildFocusScriptInterpolatesHWND(t *testing.T) {
	s := buildFocusScript("590104")
	if !strings.Contains(s, "[IntPtr][int64]590104") {
		t.Fatalf("hwnd not interpolated as numeric literal:\n%s", s)
	}
}
