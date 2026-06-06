package winfocus

import (
	"encoding/base64"
	"errors"
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

func TestCaptureStable(t *testing.T) {
	A := Binding{HWND: "100", TabRID: "42.1.4.7"}
	B := Binding{HWND: "200", TabRID: "42.2.4.9"}
	errRead := errors.New("not a WT window")

	// seq drives successive reads; an entry is either a Binding or an error.
	type rd struct {
		b   Binding
		err error
	}
	reader := func(seq []rd) func() (Binding, error) {
		i := 0
		return func() (Binding, error) {
			r := seq[i]
			if i < len(seq)-1 {
				i++
			}
			return r.b, r.err
		}
	}

	cases := []struct {
		name   string
		seq    []rd
		wantB  Binding
		wantOK bool
		wantEr error
	}{
		{"stable immediately", []rd{{b: A}, {b: A}}, A, true, nil},
		{"transient error then stable", []rd{{err: errRead}, {b: A}, {b: A}}, A, true, nil},
		{"focus moved then settled", []rd{{b: A}, {b: B}, {b: B}}, B, true, nil},
		{"error mid-streak holds it", []rd{{b: A}, {err: errRead}, {b: A}}, A, true, nil},
		{"never settles", []rd{{b: A}, {b: B}, {b: A}, {b: B}}, Binding{}, false, nil},
		{"all errors", []rd{{err: errRead}, {err: errRead}, {err: errRead}, {err: errRead}}, Binding{}, false, errRead},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, ok, err := captureStable(reader(tc.seq), 0)
			if b != tc.wantB || ok != tc.wantOK || err != tc.wantEr {
				t.Fatalf("captureStable = (%+v, %v, %v), want (%+v, %v, %v)", b, ok, err, tc.wantB, tc.wantOK, tc.wantEr)
			}
		})
	}
}

func TestSafeSessionID(t *testing.T) {
	ok := []string{"441e8bc1-39dc-4b66-bf5b-b46dcb7a875e", "abc", "a.b"}
	bad := []string{"", ".", "..", "../etc", "a/b", `a\b`, "/abs"}
	for _, s := range ok {
		if !safeSessionID(s) {
			t.Errorf("safeSessionID(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if safeSessionID(s) {
			t.Errorf("safeSessionID(%q) = true, want false", s)
		}
	}
}

func TestReadBindingRejectsUnsafeID(t *testing.T) {
	if _, ok := ReadBinding(t.TempDir(), "../escape"); ok {
		t.Fatalf("ReadBinding should reject a path-traversing session id")
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
