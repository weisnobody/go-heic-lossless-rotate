package core_test

import (
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/weisnobody/go-heic-lossless-rotate/pkg/core"
)

// Expected values in this file were captured from upstream core.py.

func TestParsePitm(t *testing.T) {
	for _, data := range [][]byte{{0, 0, 0, 0, 0, 5}, {1, 0, 0, 0, 0, 0, 0, 5}} {
		if id, err := core.ParsePitm(data, 0, len(data)); err != nil || id != 5 {
			t.Errorf("ParsePitm(% x) = %d, %v; want 5", data, id, err)
		}
	}
	if _, err := core.ParsePitm([]byte{0, 0, 0, 0, 0}, 0, 5); err == nil {
		t.Error("truncated pitm: want error")
	}
}

func TestParseIpma(t *testing.T) {
	c := []byte{0, 0, 0, 0, 0, 0, 0, 2, 0, 1, 2, 0x81, 0x02, 0, 7, 0}
	version, flags, entries, err := core.ParseIpma(c, 0, len(c))
	want := []core.IpmaEntry{{1, []core.IpmaAssoc{{1, true}, {2, false}}}, {7, nil}}
	if err != nil || version != 0 || flags != 0 || !reflect.DeepEqual(entries, want) {
		t.Errorf("ParseIpma = %d, %d, %+v, %v", version, flags, entries, err)
	}

	_, _, _, err = core.ParseIpma(append(c, 'X'), 0, len(c)+1)
	wantBoxParseError(t, err, "ipma parse mismatch: consumed 16, expected 17")

	wide := []byte{1, 0, 0, 1, 0, 0, 0, 1, 0, 0, 0, 9, 1, 0x81, 0x23}
	version, flags, entries, err = core.ParseIpma(wide, 0, len(wide))
	if err != nil || version != 1 || flags != 1 || !reflect.DeepEqual(entries, []core.IpmaEntry{{9, []core.IpmaAssoc{{0x123, true}}}}) {
		t.Errorf("ParseIpma(wide) = %d, %d, %+v, %v", version, flags, entries, err)
	}

	// Python: struct.error (traceback) - Go: *BoxParseError (COMPAT D2).
	_, _, _, err = core.ParseIpma([]byte{0, 0, 0}, 0, 3)
	wantBoxParseError(t, err, "")
}

func TestBuildIpma(t *testing.T) {
	tests := []struct {
		version uint8
		flags   uint32
		entries []core.IpmaEntry
		hex     string
	}{
		{1, 1, []core.IpmaEntry{{9, []core.IpmaAssoc{{0x123, true}}}}, "0000001769706d61010000010000000100000009018123"},
		{0, 0, []core.IpmaEntry{{1, []core.IpmaAssoc{{1, true}, {2, false}}}}, "0000001569706d6100000000000000010001028102"},
	}
	for _, tt := range tests {
		got, err := core.BuildIpma(tt.version, tt.flags, tt.entries)
		if err != nil || hex.EncodeToString(got) != tt.hex {
			t.Errorf("BuildIpma = %x, %v; want %s", got, err, tt.hex)
		}
	}
	_, err := core.BuildIpma(0, 0, []core.IpmaEntry{{1, []core.IpmaAssoc{{128, true}}}})
	wantBoxParseError(t, err, "property index too large for 7-bit ipma field (file would need 16-bit ipma flag - not auto-upgraded)")
	_, err = core.BuildIpma(0, 1, []core.IpmaEntry{{1, []core.IpmaAssoc{{0x8000, true}}}})
	wantBoxParseError(t, err, "property index too large for 15-bit ipma field")
	_, err = core.BuildIpma(0, 1, []core.IpmaEntry{{1, make([]core.IpmaAssoc, 256)}})
	wantBoxParseError(t, err, "") // Python: ValueError (COMPAT D2)
}
