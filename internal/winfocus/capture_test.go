package winfocus

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"
)

func TestEncodePSRoundTrip(t *testing.T) {
	script := "Write-Output 'olá'  # non-ASCII"
	enc := encodePS(script)
	raw, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		t.Fatalf("not valid base64: %v", err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("UTF-16LE payload must be even length, got %d", len(raw))
	}
	u := make([]uint16, len(raw)/2)
	for i := range u {
		u[i] = uint16(raw[2*i]) | uint16(raw[2*i+1])<<8
	}
	if got := string(utf16.Decode(u)); got != script {
		t.Fatalf("round trip = %q, want %q", got, script)
	}
}

func TestBuildScanScriptEscapesMarker(t *testing.T) {
	s := buildScanScript("[[cc-cockpit-focus:abc-123]]")
	if !strings.Contains(s, "$marker = '[[cc-cockpit-focus:abc-123]]'") {
		t.Fatalf("marker not embedded as expected:\n%s", s)
	}
	// A single quote in the marker must be doubled for PowerShell.
	s2 := buildScanScript("a'b")
	if !strings.Contains(s2, "'a''b'") {
		t.Fatalf("single quote not escaped: %s", s2)
	}
}

func TestSidecarRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := writeSidecar(dir, "sess-1", "590104"); err != nil {
		t.Fatalf("writeSidecar: %v", err)
	}
	got, ok := ReadHWND(dir, "sess-1")
	if !ok || got != "590104" {
		t.Fatalf("ReadHWND = (%q,%v), want (590104,true)", got, ok)
	}
	if _, ok := ReadHWND(dir, "missing"); ok {
		t.Fatalf("ReadHWND for missing session should be false")
	}
}
