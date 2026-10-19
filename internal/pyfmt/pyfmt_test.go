package pyfmt

import "testing"

// Expected values below were captured from CPython 3.14 (repr()/f-strings);
// difftest's repr op re-checks a wider table against the live interpreter.

func TestBytesRepr(t *testing.T) {
	tests := []struct {
		in   []byte
		want string
	}{
		{[]byte("irot"), `b'irot'`},
		{nil, `b''`},
		{[]byte(`a'b`), `b"a'b"`},
		{[]byte(`a"b`), `b'a"b'`},
		{[]byte(`a'b"c`), `b'a\'b"c'`},
		{[]byte(`\`), `b'\\'`},
		{[]byte("\x00\x01\t\n\r\x1f\x7f\x80\xff"), `b'\x00\x01\t\n\r\x1f\x7f\x80\xff'`},
		{[]byte(" ~"), `b' ~'`},
	}
	for _, tt := range tests {
		if got := BytesRepr(tt.in); got != tt.want {
			t.Errorf("BytesRepr(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestStrRepr(t *testing.T) {
	tests := []struct{ in, want string }{
		{"ispe", `'ispe'`},
		{"", `''`},
		{"it's", `"it's"`},
		{`a"b`, `'a"b'`},
		{`a'b"c`, `'a\'b"c'`},
		{`\`, `'\\'`},
		{"\x00\t\n\r\x1f\x7f", `'\x00\t\n\r\x1f\x7f'`},
		{"is�\x00", "'is�\\x00'"},
		{"\u00a0", `'\xa0'`},
		{"\u2028", "'\\u2028'"},
		{"é", "'é'"},
	}
	for _, tt := range tests {
		if got := StrRepr(tt.in); got != tt.want {
			t.Errorf("StrRepr(%q) = %s, want %s", tt.in, got, tt.want)
		}
	}
}

func TestDecodeASCIIReplace(t *testing.T) {
	if got, want := DecodeASCIIReplace([]byte("is\x80\x00")), "is�\x00"; got != want {
		t.Errorf("DecodeASCIIReplace = %q, want %q", got, want)
	}
}

func TestHex08(t *testing.T) {
	for in, want := range map[uint32]string{0xab: "0x000000ab", 0xdeadbeef: "0xdeadbeef", 0: "0x00000000"} {
		if got := Hex08(in); got != want {
			t.Errorf("Hex08(%#x) = %s, want %s", in, got, want)
		}
	}
}

func TestLists(t *testing.T) {
	if got, want := EnumerateList([]string{"ispe", "is�\x00"}), "[(1, 'ispe'), (2, 'is�\\x00')]"; got != want {
		t.Errorf("EnumerateList = %s, want %s", got, want)
	}
	if got := EnumerateList(nil); got != "[]" {
		t.Errorf("EnumerateList(nil) = %s", got)
	}
	if got, want := IntBoolList([]IntBool{{1, true}, {130, false}}), "[(1, True), (130, False)]"; got != want {
		t.Errorf("IntBoolList = %s, want %s", got, want)
	}
	if got := IntBoolList(nil); got != "[]" {
		t.Errorf("IntBoolList(nil) = %s", got)
	}
}
