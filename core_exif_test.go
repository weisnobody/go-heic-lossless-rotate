package heicrotate

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/pjg/go-heic-lossless-rotate/internal/synth"
)

// metaBounds returns (meta_body_start, meta content end) without
// LocateStructures, so these tests don't depend on later sections.
func metaBounds(t *testing.T, data []byte) (int, int) {
	t.Helper()
	meta, ok, err := FindChild(data, 0, len(data), "meta")
	if err != nil || !ok {
		t.Fatalf("no meta: %v", err)
	}
	return meta.ContentStart + 4, meta.ContentEnd
}

func TestFindExifOrientationField(t *testing.T) {
	for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
		for _, pos := range []synth.ExifPosition{synth.ExifEnd, synth.ExifMiddle} {
			t.Run(string(layout)+"/"+string(pos), func(t *testing.T) {
				data := mustSynth(t, func(o *synth.Options) {
					o.Layout, o.ExifPosition, o.IncludeExif, o.ExifOrientation = layout, pos, true, 6
				})
				mbs, mce := metaBounds(t, data)
				loc, ok, err := FindExifItemLocation(data, mbs, mce)
				if err != nil || !ok || loc.ItemID != 2 {
					t.Fatalf("FindExifItemLocation = %+v, %v, %v", loc, ok, err)
				}
				if !bytes.HasPrefix(data[loc.AbsStart:], []byte("\x00\x00\x00\x06Exif\x00\x00II*\x00")) {
					t.Fatalf("location doesn't point at the Exif blob")
				}
				field, ok, err := FindExifOrientationField(data, mbs, mce)
				// 4-byte header offset + "Exif\0\0" + TIFF header (8) + count (2) + tag/type/count (8)
				wantOffset := int(loc.AbsStart) + 4 + 6 + 8 + 2 + 8
				if err != nil || !ok || field.Value != 6 || field.AbsOffset != wantOffset || field.ByteOrder != binary.LittleEndian {
					t.Errorf("FindExifOrientationField = %+v, %v, %v; want value 6 at %d (II)", field, ok, err, wantOffset)
				}
			})
		}
	}
}

// rewriteTIFF replaces the synth fixture's 26-byte TIFF block in place.
func rewriteTIFF(t *testing.T, data []byte, tiff []byte) []byte {
	t.Helper()
	i := bytes.Index(data, []byte("Exif\x00\x00II*\x00"))
	if i < 0 || len(tiff) != 26 {
		t.Fatal("bad fixture or replacement")
	}
	out := bytes.Clone(data)
	copy(out[i+6:], tiff)
	return out
}

func TestFindExifOrientationFieldVariants(t *testing.T) {
	base := mustSynth(t, func(o *synth.Options) { o.IncludeExif = true })
	mbs, mce := metaBounds(t, base)

	be := binary.BigEndian
	mm := []byte("MM\x00*")
	mm = be.AppendUint32(mm, 8)
	mm = be.AppendUint16(mm, 1)
	mm = be.AppendUint16(mm, 0x0112)
	mm = be.AppendUint16(mm, 3)
	mm = be.AppendUint32(mm, 1)
	mm = append(be.AppendUint16(mm, 8), 0, 0, 0, 0, 0, 0)
	field, ok, err := FindExifOrientationField(rewriteTIFF(t, base, mm), mbs, mce)
	if err != nil || !ok || field.Value != 8 || field.ByteOrder != binary.BigEndian {
		t.Errorf("MM: %+v, %v, %v; want value 8 (MM)", field, ok, err)
	}

	noTag := bytes.Clone(mm)
	be.PutUint16(noTag[10:], 0x0110) // a different tag
	if _, ok, err := FindExifOrientationField(rewriteTIFF(t, base, noTag), mbs, mce); err != nil || ok {
		t.Errorf("no Orientation tag: ok=%v err=%v; want not found", ok, err)
	}

	badMagic := bytes.Clone(mm)
	copy(badMagic, "XX")
	if _, ok, err := FindExifOrientationField(rewriteTIFF(t, base, badMagic), mbs, mce); err != nil || ok {
		t.Errorf("bad byte-order mark: ok=%v err=%v; want not found", ok, err)
	}

	farIFD := bytes.Clone(mm)
	be.PutUint32(farIFD[4:], 1<<31) // IFD0 offset past the blob
	if _, ok, err := FindExifOrientationField(rewriteTIFF(t, base, farIFD), mbs, mce); err != nil || ok {
		t.Errorf("IFD0 past blob: ok=%v err=%v; want not found", ok, err)
	}
}

func TestPySlice64(t *testing.T) {
	data := []byte("abcdef")
	tests := []struct {
		start, length uint64
		want          string
	}{
		{2, 2, "cd"}, {4, 100, "ef"}, {6, 1, ""}, {1 << 63, 5, ""}, {3, 1<<64 - 1, "def"},
	}
	for _, tt := range tests {
		if got := pySlice64(data, tt.start, tt.length); string(got) != tt.want {
			t.Errorf("pySlice64(%d, %d) = %q, want %q", tt.start, tt.length, got, tt.want)
		}
	}
}
