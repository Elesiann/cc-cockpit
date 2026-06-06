package winfocus

import (
	"encoding/base64"
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

func TestBindingRoundTrip(t *testing.T) {
	dir := t.TempDir()
	if err := writeBinding(dir, "sess-1", Binding{HWND: "590104", TabRID: "42.67196.4.142"}); err != nil {
		t.Fatalf("writeBinding: %v", err)
	}
	got, ok := ReadBinding(dir, "sess-1")
	if !ok || got.HWND != "590104" || got.TabRID != "42.67196.4.142" {
		t.Fatalf("ReadBinding = (%+v,%v), want ({590104 42.67196.4.142},true)", got, ok)
	}
	if _, ok := ReadBinding(dir, "missing"); ok {
		t.Fatalf("ReadBinding for missing session should be false")
	}
}

func TestBindingRoundTripNoTab(t *testing.T) {
	dir := t.TempDir()
	if err := writeBinding(dir, "s", Binding{HWND: "42"}); err != nil {
		t.Fatalf("writeBinding: %v", err)
	}
	got, ok := ReadBinding(dir, "s")
	if !ok || got.HWND != "42" || got.TabRID != "" {
		t.Fatalf("ReadBinding = (%+v,%v), want ({42 },true)", got, ok)
	}
}
