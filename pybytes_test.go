package heicrotate

import (
	"bytes"
	"errors"
	"testing"
)

func TestPySlice(t *testing.T) {
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
		if got := pySlice(data, tt.lo, tt.hi); string(got) != tt.want {
			t.Errorf("pySlice(%d, %d) = %q, want %q", tt.lo, tt.hi, got, tt.want)
		}
	}
	// pySlice aliases; its capacity is capped so appends can't clobber data.
	s := pySlice(data, 0, 2)
	_ = append(s, 'X')
	if string(data) != "abcdef" {
		t.Errorf("append to pySlice result overwrote data: %q", data)
	}
	c := pySliceCopy(data, 0, 3)
	c[0] = 'Z'
	if data[0] != 'a' {
		t.Error("pySliceCopy result aliases data")
	}
}

func TestPyIndexAndUnpack(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09}
	if b, err := pyIndex(data, 8); err != nil || b != 0x09 {
		t.Errorf("pyIndex(8) = %d, %v", b, err)
	}
	if _, err := pyIndex(data, 9); err == nil {
		t.Error("pyIndex(9) past end: want error")
	}
	if v, err := unpackU16(data, 7); err != nil || v != 0x0809 {
		t.Errorf("unpackU16(7) = %#x, %v", v, err)
	}
	if v, err := unpackU32(data, 5); err != nil || v != 0x06070809 {
		t.Errorf("unpackU32(5) = %#x, %v", v, err)
	}
	if v, err := unpackU64(data, 1); err != nil || v != 0x0203040506070809 {
		t.Errorf("unpackU64(1) = %#x, %v", v, err)
	}
	for name, err := range map[string]error{
		"u16": second(unpackU16(data, 8)),
		"u32": second(unpackU32(data, 6)),
		"u64": second(unpackU64(data, 2)),
	} {
		var be *BoxParseError
		if !errors.As(err, &be) {
			t.Errorf("%s past end: err = %v, want *BoxParseError", name, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }

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
		got, err := intFromBytes(data, tt.pos, tt.n)
		if err != nil || got != tt.want {
			t.Errorf("intFromBytes(%d, %d) = %#x, %v; want %#x", tt.pos, tt.n, got, err, tt.want)
		}
	}
	// Widths over 8 bytes are accepted when the value fits (COMPAT D5)...
	wide := append(make([]byte, 7), 0x01, 0, 0, 0, 0, 0, 0, 0x02) // 15 bytes
	if got, err := intFromBytes(wide, 0, 15); err != nil || got != 0x0100000000000002 {
		t.Errorf("15-byte field = %#x, %v", got, err)
	}
	// ...and refused only when the value itself exceeds 64 bits.
	tooBig := append([]byte{0x01}, make([]byte, 8)...) // 2^64
	if _, err := intFromBytes(tooBig, 0, 9); err == nil {
		t.Error("2^64: want error")
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
				if got := splice(buf, tt.a, tt.b, []byte(tt.repl)); string(got) != tt.want {
					t.Errorf("cap+%d: splice = %q, want %q", extraCap, got, tt.want)
				}
			}
		})
	}
}

func TestSpliceReusesCapacity(t *testing.T) {
	buf := make([]byte, 1000, 1000+spliceSlack)
	buf = splice(buf, 10, 10, bytes.Repeat([]byte{1}, 100))
	if cap(buf) != 1000+spliceSlack {
		t.Errorf("growing within capacity reallocated (cap %d)", cap(buf))
	}
	grown := splice(buf, 0, 0, make([]byte, spliceSlack))
	if cap(grown) < len(grown)+spliceSlack {
		t.Errorf("reallocation didn't add slack: len %d cap %d", len(grown), cap(grown))
	}
}
