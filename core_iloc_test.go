package heicrotate

import (
	"encoding/hex"
	"reflect"
	"testing"
)

// Expected values in this file were captured from upstream core.py.

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func u16p(v uint16) *uint16 { return &v }
func u64p(v uint64) *uint64 { return &v }

func TestParseIlocVersions(t *testing.T) {
	tests := []struct {
		name string
		hex  string
		want *Iloc
	}{
		{"v0", "0000000044000001000100000001000000640000000a", &Iloc{
			Version: 0, OffsetSize: 4, LengthSize: 4,
			Items: []IlocItem{{ItemID: 1, Extents: []IlocExtent{{Offset: 100, Length: 10}}}},
		}},
		{"v2 with index", "020000008442000000010001117000010000000000050002000300000000000000c80000000a0004000000000000012c00000014", &Iloc{
			Version: 2, OffsetSize: 8, LengthSize: 4, BaseOffsetSize: 4, IndexSize: 2,
			Items: []IlocItem{{ItemID: 70000, ConstrMethod: 1, RawConstrField: u16p(1), BaseOffset: 5,
				Extents: []IlocExtent{{Index: u64p(3), Offset: 200, Length: 10}, {Index: u64p(4), Offset: 300, Length: 20}}}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := mustHex(t, tt.hex)
			got, err := ParseIloc(content, 0, len(content))
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("ParseIloc = %+v, %v\nwant %+v", got, err, tt.want)
			}
			rebuilt, err := BuildIlocContent(got)
			if err != nil || !reflect.DeepEqual(rebuilt, content) {
				t.Errorf("rebuild = %x, %v; want %x", rebuilt, err, content)
			}
		})
	}
}

func TestParseIlocMismatch(t *testing.T) {
	content := append(mustHex(t, "0000000044000001000100000001000000640000000a"), 'Z')
	_, err := ParseIloc(content, 0, len(content))
	wantBoxParseError(t, err, "iloc parse mismatch: consumed 22, expected 23")
}

func TestParseIlocWideFields(t *testing.T) {
	// COMPAT D5: a 9-byte field is fine while its value fits in 64 bits...
	ok := ilocContent(9, 0, 5)
	if got, err := ParseIloc(ok, 0, len(ok)); err != nil || got.Items[0].Extents[0].Offset != 5 {
		t.Fatalf("ParseIloc(9-byte offset 5) = %+v, %v", got, err)
	}
	// ...but 2^64+5 (which Python accepts) is refused.
	// The Python-captured fixture: 9-byte offset 0x01_0000000000000005.
	tooBig := mustHex(t, "0100000094000001000100000000000101000000000000000500000001")
	_, err := ParseIloc(tooBig, 0, len(tooBig))
	wantBoxParseError(t, err, "")
}

func TestParseIlocExtentCap(t *testing.T) {
	// COMPAT D7: zero-width fields + huge counts must not build billions of
	// extents. v1, all widths 0, n items each claiming 65535 extents.
	content := func(items int) []byte {
		c := []byte{1, 0, 0, 0, 0x00, 0x00, byte(items >> 8), byte(items)}
		for range items {
			c = append(c, 0, 1, 0, 0, 0, 0, 0xff, 0xff) // id, constr, dri, extent_count
		}
		return c
	}
	// 4 x 65535 is held (Python parses it too)...
	ok := content(4)
	if iloc, err := ParseIloc(ok, 0, len(ok)); err != nil || len(iloc.Items[3].Extents) != 65535 {
		t.Fatalf("4 items: %v", err)
	}
	// ...17 x 65535 is past the cap.
	big := content(17)
	_, err := ParseIloc(big, 0, len(big))
	wantBoxParseError(t, err, "iloc declares 1114095 zero-width extents, more than the 1048576 this port will hold in memory")

	// The refusal is deferred: where Python's own parse fails, its error wins.
	_, err = ParseIloc(big, 0, len(big)-1)
	wantBoxParseError(t, err, "iloc parse mismatch: consumed 144, expected 143")
}

func TestParseIlocOverrunMatchesPython(t *testing.T) {
	// A width nibble corrupted to 15 bytes makes Python read zeros far past
	// the end of the data before its mismatch check fails; Go must report
	// the identical consumed count instead of refusing the wide field early.
	// v1, offset_size=15, length_size=4: 1 item with 3 extents (19 bytes
	// each) in a 30-byte box.
	content := []byte{1, 0, 0, 0, 0xf4, 0, 0, 1, 0, 1, 0, 0, 0, 0, 0, 3}
	content = append(content, make([]byte, 14)...)
	_, err := ParseIloc(content, 0, len(content))
	wantBoxParseError(t, err, "iloc parse mismatch: consumed 73, expected 30")
}

// ilocContent builds v1 iloc content with one item and one extent.
func ilocContent(offsetSize int, constrRaw uint16, offset uint64) []byte {
	c := []byte{1, 0, 0, 0, byte(offsetSize<<4 | 4), 0, 0, 1, 0, 1}
	c = append(c, byte(constrRaw>>8), byte(constrRaw), 0, 0, 0, 1)
	for i := offsetSize - 1; i >= 0; i-- {
		if i < 8 {
			c = append(c, byte(offset>>(8*i)))
		} else {
			c = append(c, 0)
		}
	}
	return append(c, 0, 0, 0, 1)
}

func patchBox(content []byte, threshold, delta int) ([]byte, bool, error) {
	b := box("iloc", content)
	hdr := BoxHeader{0, 8, 8, len(b), [4]byte{'i', 'l', 'o', 'c'}}
	return PatchIlocOffsetsAtThreshold(b, &hdr, threshold, delta)
}

func TestPatchIlocOffsetErrors(t *testing.T) {
	overflow := func(v string, n int) string {
		return "patched iloc offset " + v + " overflows original field width (" + string(rune('0'+n)) +
			" bytes) - file needs a wider iloc field than the original, which this script does not (yet) upgrade"
	}
	tests := []struct {
		name      string
		content   []byte
		delta     int
		wantErr   string // "" with wantTyped: any *BoxParseError (Python crashes)
		wantTyped bool
	}{
		{"4-byte overflow", ilocContent(4, 0, 1<<32-2), 5, overflow("4294967299", 4), false},
		{"8-byte carry past 2^64", ilocContent(8, 0, 1<<64-2), 5, overflow("18446744073709551619", 8), false},
		{"zero width", ilocContent(0, 0, 0), 5, "value doesn't fit in zero-width iloc field", false},
		{"zero width, negative", ilocContent(0, 0, 0), -5, "value doesn't fit in zero-width iloc field", false},
		{"negative (Python OverflowError, D4)", ilocContent(4, 0, 3), -5, "", true},
		{"9-byte carry (Python succeeds, D5)", ilocContent(9, 0, 1<<64-2), 5, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := patchBox(tt.content, 0, tt.delta)
			wantBoxParseError(t, err, tt.wantErr)
		})
	}
}

func TestPatchIlocOffsets(t *testing.T) {
	v0 := mustHex(t, "0000000044000001000100000001000000640000000a")
	got, changed, err := patchBox(v0, 50, 7)
	if want := mustHex(t, "0000000044000001000100000001000000"+"6b"+"0000000a"); err != nil || !changed || !reflect.DeepEqual(got, want) {
		t.Errorf("patch v0 +7 = %x, %v, %v; want %x", got, changed, err, want)
	}
	// construction_method 1 (idat-relative) is never a file offset.
	if got, changed, err := patchBox(ilocContent(4, 1, 100), 0, 5); err != nil || changed || got != nil {
		t.Errorf("constr 1: %x, %v, %v; want unchanged", got, changed, err)
	}
	if got, changed, err := PatchIlocOffsetsAtThreshold(nil, nil, 0, 5); err != nil || changed || got != nil {
		t.Errorf("nil iloc header: %x, %v, %v", got, changed, err)
	}
}
