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
	s := buildFocusScript("590104", -1)
	if !strings.Contains(s, "[IntPtr][int64]590104") {
		t.Fatalf("hwnd not interpolated as numeric literal:\n%s", s)
	}
	// No tab → no UIA tab-selection block.
	if strings.Contains(s, "SelectionItemPattern") {
		t.Fatalf("tab<0 should not emit a tab-select block:\n%s", s)
	}
}

func TestBuildFocusScriptSelectsTab(t *testing.T) {
	s := buildFocusScript("590104", 3)
	if !strings.Contains(s, "SelectionItemPattern") {
		t.Fatalf("tab>=0 should emit a tab-select block:\n%s", s)
	}
	if !strings.Contains(s, "-eq 3)") {
		t.Fatalf("tab index 3 not interpolated:\n%s", s)
	}
}
