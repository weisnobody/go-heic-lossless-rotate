package pybytes

import (
	"bytes"
	"testing"
)

func TestSlice(t *testing.T) {
	data := []byte("abcdef")
	tests := []struct {
		lo, hi int
		want   string
	}{
		{0, 6, "abcdef"}, {2, 4, "cd"}, {4, 100, "ef"}, {100, 200, ""},
		{4, 2, ""}, // lo > hi: empty, like Python
		{6, 6, ""},
	}
	for _, tt := range tests {
		if got := Slice(data, tt.lo, tt.hi); string(got) != tt.want {
			t.Errorf("Slice(%d, %d) = %q, want %q", tt.lo, tt.hi, got, tt.want)
		}
	}
	// Slice aliases; its capacity is capped so appends can't clobber data.
	s := Slice(data, 0, 2)
	_ = append(s, 'X')
	if string(data) != "abcdef" {
		t.Errorf("append to Slice result overwrote data: %q", data)
	}
	c := SliceCopy(data, 0, 3)
	c[0] = 'Z'
	if data[0] != 'a' {
		t.Error("SliceCopy result aliases data")
	}
}

func TestIndexAndUnpack(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09}
	if b, ok := Index(data, 8); !ok || b != 0x09 {
		t.Errorf("Index(8) = %d, %v", b, ok)
	}
	if _, ok := Index(data, 9); ok {
		t.Error("Index(9) past end: want !ok")
	}
	if v, ok := UnpackU16(data, 7); !ok || v != 0x0809 {
		t.Errorf("UnpackU16(7) = %#x, %v", v, ok)
	}
	if v, ok := UnpackU32(data, 5); !ok || v != 0x06070809 {
		t.Errorf("UnpackU32(5) = %#x, %v", v, ok)
	}
	if v, ok := UnpackU64(data, 1); !ok || v != 0x0203040506070809 {
		t.Errorf("UnpackU64(1) = %#x, %v", v, ok)
	}
	for name, ok := range map[string]bool{
		"u16": second(UnpackU16(data, 8)),
		"u32": second(UnpackU32(data, 6)),
		"u64": second(UnpackU64(data, 2)),
	} {
		if ok {
			t.Errorf("%s past end: want !ok", name)
		}
	}
}

func second[T any](_ T, ok bool) bool { return ok }

func TestIntFromBytes(t *testing.T) {
	data := []byte{0x00, 0x00, 0x12, 0x34, 0xff}
	tests := []struct {
		pos, n int
		want   uint64
	}{
		{2, 2, 0x1234},
		{0, 0, 0},
		{3, 4, 0x34ff}, // short read past the end: Python silently reads fewer bytes
		{10, 4, 0},     // entirely past the end: empty slice, value 0
		{0, 5, 0x1234ff},
	}
	for _, tt := range tests {
		got, ok := IntFromBytes(data, tt.pos, tt.n)
		if !ok || got != tt.want {
			t.Errorf("IntFromBytes(%d, %d) = %#x, %v; want %#x", tt.pos, tt.n, got, ok, tt.want)
		}
	}
	// Widths over 8 bytes are accepted when the value fits (COMPAT D5)...
	wide := append(make([]byte, 7), 0x01, 0, 0, 0, 0, 0, 0, 0x02) // 15 bytes
	if got, ok := IntFromBytes(wide, 0, 15); !ok || got != 0x0100000000000002 {
		t.Errorf("15-byte field = %#x, %v", got, ok)
	}
	// ...and refused only when the value itself exceeds 64 bits.
	tooBig := append([]byte{0x01}, make([]byte, 8)...) // 2^64
	if _, ok := IntFromBytes(tooBig, 0, 9); ok {
		t.Error("2^64: want !ok")
	}
}

func TestSplice(t *testing.T) {
	tests := []struct {
		name string
		a, b int
		repl string
		want string
	}{
		{"same size", 1, 3, "XY", "aXYdef"},
		{"grow", 1, 2, "XYZ", "aXYZcdef"},
		{"shrink", 1, 5, "X", "aXf"},
		{"insert", 3, 3, "XY", "abcXYdef"},
		{"insert at end", 6, 6, "XY", "abcdefXY"},
		{"delete", 2, 4, "", "abef"},
		{"clamped past end", 4, 100, "Z", "abcdZ"},
		{"b < a inserts at a", 4, 1, "Z", "abcdZef"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Exercise both the in-place (spare capacity) and reallocating paths.
			for _, extraCap := range []int{0, 16} {
				buf := make([]byte, 6, 6+extraCap)
				copy(buf, "abcdef")
				if got := Splice(buf, tt.a, tt.b, []byte(tt.repl)); string(got) != tt.want {
					t.Errorf("cap+%d: Splice = %q, want %q", extraCap, got, tt.want)
				}
			}
		})
	}
}

func TestSpliceReusesCapacity(t *testing.T) {
	buf := make([]byte, 1000, 1000+SpliceSlack)
	buf = Splice(buf, 10, 10, bytes.Repeat([]byte{1}, 100))
	if cap(buf) != 1000+SpliceSlack {
		t.Errorf("growing within capacity reallocated (cap %d)", cap(buf))
	}
	grown := Splice(buf, 0, 0, make([]byte, SpliceSlack))
	if cap(grown) < len(grown)+SpliceSlack {
		t.Errorf("reallocation didn't add slack: len %d cap %d", len(grown), cap(grown))
	}
}
