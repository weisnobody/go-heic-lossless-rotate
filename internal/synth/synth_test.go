package synth

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// topBox is a minimal, independent box reader for these tests - deliberately
// not heicrotate's own parser, so a bug shared by both can't hide here.
type topBox struct {
	typ                string
	start, cStart, end int
}

func children(t *testing.T, data []byte, start, end int) []topBox {
	t.Helper()
	var out []topBox
	for pos := start; pos < end; {
		if pos+8 > end {
			t.Fatalf("truncated box header at %d", pos)
		}
		size := int(binary.BigEndian.Uint32(data[pos:]))
		if size < 8 || pos+size > end {
			t.Fatalf("bad box size %d at %d", size, pos)
		}
		out = append(out, topBox{string(data[pos+4 : pos+8]), pos, pos + 8, pos + size})
		pos += size
	}
	return out
}

func types(bs []topBox) []string {
	var s []string
	for _, b := range bs {
		s = append(s, b.typ)
	}
	return s
}

func find(t *testing.T, bs []topBox, typ string) topBox {
	t.Helper()
	for _, b := range bs {
		if b.typ == typ {
			return b
		}
	}
	t.Fatalf("no %q box among %v", typ, types(bs))
	return topBox{}
}

type ilocItem struct{ id, offset, length uint32 }

// ilocItems decodes synth's fixed iloc layout: version 1, offset/length/
// base_offset 4 bytes, index_size 0, base_offset always 0.
func ilocItems(t *testing.T, data []byte) []ilocItem {
	t.Helper()
	top := children(t, data, 0, len(data))
	meta := find(t, top, "meta")
	iloc := find(t, children(t, data, meta.cStart+4, meta.end), "iloc")
	p := iloc.cStart
	if data[p] != 1 || data[p+4] != 0x44 || data[p+5] != 0x40 {
		t.Fatalf("unexpected iloc header % x", data[p:p+6])
	}
	n := int(binary.BigEndian.Uint16(data[p+6:]))
	p += 8
	var items []ilocItem
	for range n {
		id := binary.BigEndian.Uint16(data[p:])
		base := binary.BigEndian.Uint32(data[p+6:])
		if extents := binary.BigEndian.Uint16(data[p+10:]); base != 0 || extents != 1 {
			t.Fatalf("item %d: base_offset=%d extent_count=%d, want 0/1", id, base, extents)
		}
		items = append(items, ilocItem{uint32(id), binary.BigEndian.Uint32(data[p+12:]), binary.BigEndian.Uint32(data[p+16:])})
		p += 20
	}
	if p != iloc.end {
		t.Fatalf("iloc parse consumed to %d, box ends at %d", p, iloc.end)
	}
	return items
}

func mustBuild(t *testing.T, o Options) []byte {
	t.Helper()
	data, err := BuildSyntheticHEIC(o)
	if err != nil {
		t.Fatalf("BuildSyntheticHEIC: %v", err)
	}
	return data
}

func TestDefaultOptionsMatchPython(t *testing.T) {
	o := DefaultOptions()
	if o.Layout != MdatFirst || o.IncludeIrot || o.IrotValue != 0 || o.IncludeExif ||
		o.ExifOrientation != 1 || o.ExifPosition != ExifEnd || o.ExtraMdat {
		t.Errorf("DefaultOptions() = %+v", o)
	}
	if want := bytes.Repeat([]byte("FAKE_PIXEL_DATA"), 20); !bytes.Equal(o.MdatPayload, want) {
		t.Errorf("default payload = %q", o.MdatPayload)
	}
}

func TestTopLevelBoxOrder(t *testing.T) {
	tests := []struct {
		layout Layout
		extra  bool
		want   []string
	}{
		{MdatFirst, false, []string{"ftyp", "mdat", "meta"}},
		{MdatFirst, true, []string{"ftyp", "mdat", "mdat", "meta"}},
		{MetaFirst, false, []string{"ftyp", "meta", "mdat"}},
		{MetaFirst, true, []string{"ftyp", "meta", "mdat", "mdat"}},
	}
	for _, tt := range tests {
		t.Run(string(tt.layout), func(t *testing.T) {
			o := DefaultOptions()
			o.Layout, o.ExtraMdat = tt.layout, tt.extra
			data := mustBuild(t, o)
			got := types(children(t, data, 0, len(data)))
			if !equalStrings(got, tt.want) {
				t.Errorf("top-level boxes = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIlocOffsetsPointAtPayloadAndExif(t *testing.T) {
	// Upstream's synth once used a mdat-relative Exif offset instead of the
	// absolute file offset the spec requires, silently making Exif sync
	// unreachable in every synthetic test. Check the offsets land on the
	// real bytes, for every layout/position combination.
	for _, layout := range []Layout{MdatFirst, MetaFirst} {
		for _, pos := range []ExifPosition{ExifEnd, ExifMiddle} {
			for _, extra := range []bool{false, true} {
				o := DefaultOptions()
				o.Layout, o.ExifPosition, o.ExtraMdat = layout, pos, extra
				o.IncludeExif, o.ExifOrientation = true, 6
				name := string(layout) + "/" + string(pos)
				if extra {
					name += "/extra_mdat"
				}
				t.Run(name, func(t *testing.T) {
					data := mustBuild(t, o)
					items := ilocItems(t, data)
					if len(items) != 2 {
						t.Fatalf("got %d iloc items, want 2", len(items))
					}
					main, exif := items[0], items[1]
					if got := data[main.offset : main.offset+main.length]; !bytes.Equal(got, o.MdatPayload) {
						t.Errorf("main item extent doesn't point at the payload")
					}
					blob := data[exif.offset : exif.offset+exif.length]
					if !bytes.HasPrefix(blob, []byte("\x00\x00\x00\x06Exif\x00\x00II*\x00")) {
						t.Errorf("exif extent doesn't point at the Exif blob: % x", blob[:min(16, len(blob))])
					}
					// Orientation value: tiff header(8) + count(2) + tag/type/count(8)
					if v := binary.LittleEndian.Uint16(blob[10+8+2+8:]); v != 6 {
						t.Errorf("Exif orientation = %d, want 6", v)
					}

					mdat := find(t, children(t, data, 0, len(data)), "mdat") // first mdat is the real one
					if exif.offset < uint32(mdat.cStart) || exif.offset+exif.length > uint32(mdat.end) {
						t.Fatalf("exif extent [%d,%d) outside mdat content [%d,%d)",
							exif.offset, exif.offset+exif.length, mdat.cStart, mdat.end)
					}
					tailGap := uint32(mdat.end) - (exif.offset + exif.length)
					switch pos {
					case ExifEnd:
						if tailGap != 0 {
							t.Errorf("ExifEnd: %d bytes after the blob, want 0", tailGap)
						}
					case ExifMiddle:
						if tailGap != exif.length || exif.offset == uint32(mdat.cStart) {
							t.Errorf("ExifMiddle: blob not strictly inside mdat (tail gap %d, blob len %d)",
								tailGap, exif.length)
						}
					}
				})
			}
		}
	}
}

func TestIrotProperty(t *testing.T) {
	for _, value := range []int{0, 1, 2, 3, 5, -1} {
		o := DefaultOptions()
		o.IncludeIrot, o.IrotValue = true, value
		data := mustBuild(t, o)
		meta := find(t, children(t, data, 0, len(data)), "meta")
		iprp := find(t, children(t, data, meta.cStart+4, meta.end), "iprp")
		ipco := find(t, children(t, data, iprp.cStart, iprp.end), "ipco")
		props := children(t, data, ipco.cStart, ipco.end)
		if got := types(props); !equalStrings(got, []string{"ispe", "irot"}) {
			t.Fatalf("ipco children = %v", got)
		}
		// Python: bytes([irot_value & 0x3])
		if got, want := data[props[1].cStart], byte(value&0x3); got != want {
			t.Errorf("IrotValue %d: irot byte = %d, want %d", value, got, want)
		}
	}
}

func TestInvalidOptions(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Options)
		wantErr string
	}{
		{"layout", func(o *Options) { o.Layout = "sideways" }, "unknown layout 'sideways'"},
		{"exif_position", func(o *Options) { o.ExifPosition = "top" }, "unknown exif_position 'top'"},
		// Python checks exif_position before layout.
		{"both", func(o *Options) { o.Layout, o.ExifPosition = "x", "y" }, "unknown exif_position 'y'"},
		{"quote", func(o *Options) { o.Layout = "it's" }, `unknown layout "it's"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			o := DefaultOptions()
			tt.mutate(&o)
			_, err := BuildSyntheticHEIC(o)
			if err == nil || err.Error() != tt.wantErr {
				t.Errorf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
