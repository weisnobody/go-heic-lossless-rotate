package core_test

import (
	"encoding/binary"
	"errors"
	"testing"

	"github.com/weisnobody/go-heic-lossless-rotate/pkg/core"
)

// box builds a plain box with an 8-byte header (test helper).
func box(boxtype string, content []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(content)+8))
	return append(append(out, boxtype...), content...)
}

// rawHeader builds just a box header with an arbitrary size field.
func rawHeader(size uint32, boxtype string) []byte {
	return append(binary.BigEndian.AppendUint32(nil, size), boxtype...)
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func wantBoxParseError(t *testing.T, err error, msg string) {
	t.Helper()
	var be *core.BoxParseError
	if !errors.As(err, &be) {
		t.Fatalf("err = %v (%T), want *BoxParseError", err, err)
	}
	if msg != "" && be.Msg != msg {
		t.Errorf("message:\n  got  %s\n  want %s", be.Msg, msg)
	}
}

// Expected values in this file were captured from upstream core.py.

func TestReadBoxHeader(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want core.BoxHeader
		ok   bool
		err  string
	}{
		{"eof", box("free", nil)[:7], core.BoxHeader{}, false, ""},
		{"normal", box("pitm", []byte("abcd")), core.BoxHeader{0, 8, 8, 12, [4]byte{'p', 'i', 't', 'm'}}, true, ""},
		{"size 0 extends to EOF", cat(rawHeader(0, "mdat"), []byte("xyz")), core.BoxHeader{0, 8, 8, 11, [4]byte{'m', 'd', 'a', 't'}}, true, ""},
		{"size smaller than header", cat(rawHeader(3, "free"), []byte("xyz")), core.BoxHeader{0, 8, 8, 3, [4]byte{'f', 'r', 'e', 'e'}}, true, ""},
		{"64-bit size", cat(rawHeader(1, "mdat"), binary.BigEndian.AppendUint64(nil, 20), []byte("abcd")),
			core.BoxHeader{0, 16, 16, 20, [4]byte{'m', 'd', 'a', 't'}}, true, ""},
		{"truncated 64-bit size", cat(rawHeader(1, "mdat"), make([]byte, 4)), core.BoxHeader{}, false,
			"truncated 64-bit box size"},
		{"64-bit size beyond EOF", cat(rawHeader(1, "mdat"), binary.BigEndian.AppendUint64(nil, 1<<64-1)), core.BoxHeader{}, false,
			"box b'mdat' at 0 claims size 18446744073709551615 beyond EOF"},
		{"beyond EOF with odd type bytes", rawHeader(99, "a'\x00\xff"), core.BoxHeader{}, false,
			`box b"a'\x00\xff" at 0 claims size 99 beyond EOF`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := core.ReadBoxHeader(tt.data, 0)
			if tt.err != "" {
				wantBoxParseError(t, err, tt.err)
				return
			}
			if err != nil || ok != tt.ok || got != tt.want {
				t.Errorf("ReadBoxHeader = %+v, %v, %v; want %+v, %v", got, ok, err, tt.want, tt.ok)
			}
		})
	}
}

func TestIterBoxesIsLazy(t *testing.T) {
	// A malformed box AFTER the one find_child wants must not be reached.
	data := cat(box("pitm", nil), rawHeader(99, "junk"))
	hdr, ok, err := core.FindChild(data, 0, len(data), "pitm")
	if err != nil || !ok || hdr.Start != 0 {
		t.Errorf("FindChild(pitm) = %+v, %v, %v", hdr, ok, err)
	}
	_, _, err = core.FindChild(data, 0, len(data), "iloc")
	wantBoxParseError(t, err, "box b'junk' at 8 claims size 99 beyond EOF")
	// find_unique_child walks every child, so it does hit the bad box.
	_, _, err = core.FindUniqueChild(data, 0, len(data), "pitm", "x")
	wantBoxParseError(t, err, "box b'junk' at 8 claims size 99 beyond EOF")
}

func TestIterBoxesBoundsAgainstData(t *testing.T) {
	// Iteration stops at `end`, but a child may extend past it as long as
	// it stays inside data - exactly as in Python.
	data := cat(box("aaaa", nil), box("bbbb", nil))
	got, err := core.ListBoxes(data, 0, 4)
	if err != nil || len(got) != 1 || !got[0].Is("aaaa") {
		t.Errorf("listBoxes(0, 4) = %+v, %v", got, err)
	}
}

func TestZeroSize64BitBox(t *testing.T) {
	// COMPAT D6: size field 1 with a 64-bit size of 0 never advances;
	// Python's iter_boxes yields it forever.
	zero := cat(rawHeader(1, "free"), make([]byte, 8), make([]byte, 8))
	if hdr, ok, err := core.FindChild(zero, 0, len(zero), "free"); err != nil || !ok || hdr.ContentEnd != 0 {
		t.Errorf("FindChild of the zero-size box itself = %+v, %v, %v (Python returns it)", hdr, ok, err)
	}
	_, err := core.ListBoxes(zero, 0, len(zero))
	wantBoxParseError(t, err, "box b'free' at 0 has a 64-bit size of 0 and cannot be stepped over")
}

func TestFindUniqueChild(t *testing.T) {
	two := cat(box("ipma", nil), box("ipma", nil))
	_, _, err := core.FindUniqueChild(two, 0, len(two), "ipma", "test")
	wantBoxParseError(t, err, "found 2 'ipma' boxes in test, expected at most 1 - the standard permits more "+
		"than one in some cases, but this script does not support rebuilding such a file without risking "+
		"silently dropping data from the boxes beyond the first")
}
