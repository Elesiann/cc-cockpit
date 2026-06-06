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

func TestValidRID(t *testing.T) {
	ok := []string{"42.67196.4.142", "0", "1.2.3"}
	bad := []string{"", " ", "42.x.1", "42 1", "42;calc", "42:1", "42.-1"}
	for _, s := range ok {
		if !validRID(s) {
			t.Errorf("validRID(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if validRID(s) {
			t.Errorf("validRID(%q) = true, want false", s)
		}
	}
}

func TestBuildFocusScriptInterpolatesHWND(t *testing.T) {
	s := buildFocusScript("590104", "")
	if !strings.Contains(s, "[IntPtr][int64]590104") {
		t.Fatalf("hwnd not interpolated as numeric literal:\n%s", s)
	}
	// No tab → no UIA tab-selection block.
	if strings.Contains(s, "SelectionItemPattern") {
		t.Fatalf("empty tabRID should not emit a tab-select block:\n%s", s)
	}
}

func TestBuildFocusScriptGuardsStaleHandle(t *testing.T) {
	s := buildFocusScript("590104", "")
	// FromHandle resolves a dead handle to another live window, so the script
	// must reject any element whose reported handle differs from the requested
	// one before acting.
	if !strings.Contains(s, "NativeWindowHandle -ne 590104") {
		t.Fatalf("missing stale-handle guard:\n%s", s)
	}
}

func TestFocuserLoopGuardsStaleHandle(t *testing.T) {
	s := focuserLoopScript()
	if !strings.Contains(s, "NativeWindowHandle -ne $hwnd") {
		t.Fatalf("warm focuser missing stale-handle guard:\n%s", s)
	}
}

func TestBuildFocusScriptSelectsTabByRID(t *testing.T) {
	s := buildFocusScript("590104", "42.67196.4.142")
	if !strings.Contains(s, "SelectionItemPattern") {
		t.Fatalf("non-empty tabRID should emit a tab-select block:\n%s", s)
	}
	// The tab is matched by RuntimeId equality, not positional index.
	if !strings.Contains(s, "GetRuntimeId() -join '.') -eq '42.67196.4.142'") {
		t.Fatalf("tab RuntimeId not interpolated into a match:\n%s", s)
	}
}
