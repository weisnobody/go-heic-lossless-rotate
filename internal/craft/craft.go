// Package craft builds HEIF files with the structural variations the
// internal/synth fixtures hold fixed - box versions, field widths, wide
// ipma indexes, big item ids, Exif variants, duplicate boxes, unusual
// headers - for differential and cross-engine tests. Whether a given
// variant is "valid" doesn't matter, only that the implementations being
// compared agree on it.
package craft

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// Options parameterizes the HEIF structures internal/synth holds fixed, to
// reach core.go branches synth can't (field widths, box versions, wide ipma
// indexes, big item ids, Exif variants, unusual box order). Every crafted
// file is run through both implementations; whether a given variant is
// "valid" doesn't matter, only that Python and Go agree on it.
type Options struct {
	Layout          string // "mdat_first" or "meta_first"
	PitmVersion     uint8
	IpmaVersion     uint8
	IpmaWide        bool // ipma flags bit 0: 15-bit property indexes
	IpmaFirst       bool // ipma before ipco inside iprp
	ExtraProps      int  // extra dummy properties after ispe (127 forces a wide upgrade on insert)
	IprpExtra       bool // an unrelated extra child box inside iprp
	Irot            int  // -1 for none, else the irot byte
	IlocVersion     uint8
	OffsetSize      int
	LengthSize      int
	BaseOffsetSize  int
	IndexSize       int // only meaningful for iloc v1/v2
	InfeVersion     uint8
	BigIDs          bool // 32-bit item ids (needs pitm v1, ipma v1, iloc v2, infe v3)
	Exif            bool
	ExifConstr      uint16 // iloc construction method for the Exif item
	ExifBigEndian   bool   // 'MM' TIFF
	ExifNoOrient    bool   // IFD0 entry is some other tag
	ExifOrientation uint16
	MetaLargesize   bool // meta uses a 64-bit size header
	ExtendsToEOF    bool // last top-level box has size field 0
	DupIpma         bool // a second ipma inside iprp (refused)
	DupIpco         bool // a second ipco inside iprp (refused)
	DupIprp         bool // a second iprp inside meta (refused)
	AssocHighIndex  bool // the primary item also associates with the last extra property
	// ExtraItem adds an unreferenced iloc item (id 99, construction method
	// 0) with ExtraItemExtents extents whose offset field holds
	// ExtraItemOffset (OffsetSize bytes, big-endian; shorter is left-padded).
	ExtraItem        bool
	ExtraItemOffset  []byte
	ExtraItemExtents int
}

// Default is the baseline Options every variant starts from.
func Default() Options {
	return Options{
		Layout: "meta_first", IpmaVersion: 0, Irot: -1, IlocVersion: 1,
		OffsetSize: 4, LengthSize: 4, InfeVersion: 2, ExifOrientation: 6,
	}
}

// Box builds a box with an 8-byte header.
func Box(boxtype string, content ...[]byte) []byte {
	body := bytes.Join(content, nil)
	return append(append(binary.BigEndian.AppendUint32(nil, uint32(len(body)+8)), boxtype...), body...)
}

func fullBox(boxtype string, version uint8, flags uint32, content ...[]byte) []byte {
	return Box(boxtype, append([]byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}, bytes.Join(content, nil)...))
}

func beN(v uint64, n int) []byte {
	out := make([]byte, n)
	for i := n - 1; i >= 0 && v > 0; i-- {
		out[i] = byte(v)
		v >>= 8
	}
	return out
}

// Build builds the file o describes.
func Build(o Options) []byte {
	mainID, exifID := uint64(1), uint64(2)
	if o.BigIDs {
		mainID, exifID = 70001, 70002
	}
	ftyp := Box("ftyp", []byte("heicmif1heic"))
	payload := bytes.Repeat([]byte("PIXELS"), 40)

	ord := binary.AppendByteOrder(binary.LittleEndian)
	tiff := []byte("II*\x00")
	if o.ExifBigEndian {
		ord, tiff = binary.BigEndian, []byte("MM\x00*")
	}
	tag := uint16(0x0112)
	if o.ExifNoOrient {
		tag = 0x0110
	}
	tiff = ord.AppendUint32(tiff, 8)
	tiff = ord.AppendUint16(tiff, 1)
	tiff = ord.AppendUint16(tiff, tag)
	tiff = ord.AppendUint16(tiff, 3)
	tiff = ord.AppendUint32(tiff, 1)
	tiff = ord.AppendUint16(tiff, o.ExifOrientation)
	tiff = append(tiff, 0, 0, 0, 0, 0, 0)
	exifBlob := append(append(binary.BigEndian.AppendUint32(nil, 6), "Exif\x00\x00"...), tiff...)

	mdatContent := bytes.Clone(payload)
	if o.Exif {
		mdatContent = append(mdatContent, exifBlob...)
	}
	mdat := Box("mdat", mdatContent)

	idWidth := func(wide bool) int {
		if wide {
			return 4
		}
		return 2
	}
	hdlr := fullBox("hdlr", 0, 0, make([]byte, 4), []byte("pict"), make([]byte, 13))
	pitm := fullBox("pitm", o.PitmVersion, 0, beN(mainID, idWidth(o.PitmVersion != 0)))

	type infe struct {
		id  uint64
		typ string
	}
	items := []infe{{mainID, "hvc1"}}
	if o.Exif {
		items = append(items, infe{exifID, "Exif"})
	}
	var infes []byte
	for _, it := range items {
		if o.InfeVersion >= 3 {
			infes = append(infes, fullBox("infe", o.InfeVersion, 0, beN(it.id, 4), []byte{0, 0}, []byte(it.typ), []byte{0})...)
		} else {
			infes = append(infes, fullBox("infe", o.InfeVersion, 0, beN(it.id, 2), []byte{0, 0}, []byte(it.typ), []byte{0})...)
		}
	}
	iinf := fullBox("iinf", 0, 0, beN(uint64(len(items)), 2), infes)

	props := [][]byte{fullBox("ispe", 0, 0, beN(100, 4), beN(200, 4))}
	for i := range o.ExtraProps {
		props = append(props, Box("pasp", beN(uint64(i), 4), beN(1, 4)))
	}
	assocIdx := []int{1}
	if o.AssocHighIndex && o.ExtraProps > 0 {
		assocIdx = append(assocIdx, 1+o.ExtraProps)
	}
	if o.Irot >= 0 {
		props = append(props, Box("irot", []byte{byte(o.Irot)}))
		assocIdx = append(assocIdx, len(props))
	}
	ipco := Box("ipco", props...)
	var flags uint32
	if o.IpmaWide {
		flags = 1
	}
	entry := append(beN(mainID, idWidth(o.IpmaVersion >= 1)), byte(len(assocIdx)))
	for _, idx := range assocIdx {
		if o.IpmaWide {
			entry = binary.BigEndian.AppendUint16(entry, 0x8000|uint16(idx))
		} else {
			entry = append(entry, 0x80|byte(idx))
		}
	}
	ipma := fullBox("ipma", o.IpmaVersion, flags, beN(1, 4), entry)
	iprpParts := [][]byte{ipco, ipma}
	if o.IpmaFirst {
		iprpParts = [][]byte{ipma, ipco}
	}
	if o.DupIpma {
		iprpParts = append(iprpParts, ipma)
	}
	if o.DupIpco {
		iprpParts = append(iprpParts, ipco)
	}
	if o.IprpExtra {
		iprpParts = append(iprpParts, Box("free", []byte("unrelated")))
	}
	iprp := Box("iprp", iprpParts...)

	v12 := o.IlocVersion == 1 || o.IlocVersion == 2
	buildMeta := func(mdatContentStart uint64) []byte {
		type ilocItem struct {
			id          uint64
			constr      uint16
			off, length uint64
		}
		ilocItems := []ilocItem{{mainID, 0, mdatContentStart, uint64(len(payload))}}
		if o.Exif {
			ilocItems = append(ilocItems, ilocItem{exifID, o.ExifConstr, mdatContentStart + uint64(len(payload)), uint64(len(exifBlob))})
		}
		idx := 0
		if v12 {
			idx = o.IndexSize
		}
		body := []byte{byte(o.OffsetSize<<4 | o.LengthSize), byte(o.BaseOffsetSize<<4 | idx)}
		body = append(body, beN(uint64(len(ilocItems)), idWidth(o.IlocVersion >= 2))...)
		for _, it := range ilocItems {
			body = append(body, beN(it.id, idWidth(o.IlocVersion >= 2))...)
			if v12 {
				body = binary.BigEndian.AppendUint16(body, it.constr)
			}
			body = append(body, 0, 0) // data_reference_index
			base, off := uint64(0), it.off
			if o.BaseOffsetSize > 0 { // split the absolute offset between base and extent
				base, off = it.off-16, 16
			}
			if o.OffsetSize == 0 { // all of it in base_offset
				base, off = it.off, 0
			}
			body = append(body, beN(base, o.BaseOffsetSize)...)
			body = append(body, 0, 1) // extent_count
			if v12 && o.IndexSize > 0 {
				body = append(body, beN(0, o.IndexSize)...)
			}
			body = append(body, beN(off, o.OffsetSize)...)
			body = append(body, beN(it.length, o.LengthSize)...)
		}
		if o.ExtraItem {
			// Patch the item count, then append item 99.
			countAt := 2
			if o.IlocVersion >= 2 {
				binary.BigEndian.PutUint32(body[countAt:], uint32(len(ilocItems)+1))
			} else {
				binary.BigEndian.PutUint16(body[countAt:], uint16(len(ilocItems)+1))
			}
			body = append(body, beN(99, idWidth(o.IlocVersion >= 2))...)
			if v12 {
				body = append(body, 0, 0)
			}
			body = append(body, 0, 0)
			body = append(body, beN(0, o.BaseOffsetSize)...)
			body = binary.BigEndian.AppendUint16(body, uint16(o.ExtraItemExtents))
			offset := make([]byte, o.OffsetSize)
			copy(offset[max(0, o.OffsetSize-len(o.ExtraItemOffset)):], o.ExtraItemOffset)
			for range o.ExtraItemExtents {
				if v12 && o.IndexSize > 0 {
					body = append(body, beN(0, o.IndexSize)...)
				}
				body = append(body, offset...)
				body = append(body, beN(1, o.LengthSize)...)
			}
		}
		iloc := fullBox("iloc", o.IlocVersion, 0, body)
		parts := [][]byte{{0, 0, 0, 0}, hdlr, iinf, pitm, iprp, iloc}
		if o.DupIprp {
			parts = append(parts, iprp)
		}
		content := bytes.Join(parts, nil)
		if o.MetaLargesize {
			return append(append(append(binary.BigEndian.AppendUint32(nil, 1), "meta"...), beN(uint64(len(content)+16), 8)...), content...)
		}
		return Box("meta", content)
	}

	metaLen := len(buildMeta(0))
	var out []byte
	if o.Layout == "mdat_first" {
		out = bytes.Join([][]byte{ftyp, mdat, buildMeta(uint64(len(ftyp) + 8))}, nil)
	} else {
		out = bytes.Join([][]byte{ftyp, buildMeta(uint64(len(ftyp) + metaLen + 8)), mdat}, nil)
	}
	if o.ExtendsToEOF {
		// Rewrite the last top-level box's size field to 0 ("extends to EOF").
		last := len(ftyp) + metaLen
		if o.Layout == "mdat_first" {
			last = len(ftyp) + len(mdat)
		}
		binary.BigEndian.PutUint32(out[last:], 0)
	}
	return out
}

// Variants returns named variants, each varying one Options dimension
// from Default, for both layouts.
func Variants() map[string]Options {
	type v struct {
		name string
		mut  func(*Options)
	}
	vs := []v{
		{"default", func(*Options) {}},
		{"irot=1", func(o *Options) { o.Irot = 1 }},
		{"irot=0xfe(reserved bits)", func(o *Options) { o.Irot = 0xfe }},
		{"pitm_v1", func(o *Options) { o.PitmVersion = 1 }},
		{"ipma_v1", func(o *Options) { o.IpmaVersion = 1 }},
		{"ipma_wide", func(o *Options) { o.IpmaWide = true }},
		{"ipma_first", func(o *Options) { o.IpmaFirst = true }},
		{"wide_upgrade_on_insert", func(o *Options) { o.ExtraProps = 126 }},
		{"wide_upgrade_with_irot", func(o *Options) { o.ExtraProps = 126; o.Irot = 2 }},
		{"iprp_extra_child", func(o *Options) { o.IprpExtra = true }},
		{"iloc_v0", func(o *Options) { o.IlocVersion = 0 }},
		{"iloc_v2", func(o *Options) { o.IlocVersion = 2 }},
		{"iloc_8byte_offsets", func(o *Options) { o.OffsetSize, o.LengthSize = 8, 8 }},
		{"iloc_base_offset", func(o *Options) { o.BaseOffsetSize = 4 }},
		{"iloc_base8_index4", func(o *Options) { o.BaseOffsetSize, o.IndexSize = 8, 4 }},
		{"iloc_zero_offset_size", func(o *Options) { o.OffsetSize, o.BaseOffsetSize = 0, 4 }},
		{"iloc_9byte_offsets", func(o *Options) { o.OffsetSize = 9 }},
		{"infe_v3", func(o *Options) { o.InfeVersion = 3 }},
		{"big_ids", func(o *Options) {
			o.BigIDs, o.PitmVersion, o.IpmaVersion, o.IlocVersion, o.InfeVersion = true, 1, 1, 2, 3
		}},
		{"exif_II", func(o *Options) { o.Exif = true }},
		{"exif_MM", func(o *Options) { o.Exif, o.ExifBigEndian = true, true }},
		{"exif_no_orientation", func(o *Options) { o.Exif, o.ExifNoOrient = true, true }},
		{"exif_constr1", func(o *Options) { o.Exif, o.ExifConstr = true, 1 }},
		{"exif_with_irot", func(o *Options) { o.Exif, o.Irot = true, 3 }},
		{"exif_iloc_v0", func(o *Options) { o.Exif, o.IlocVersion = true, 0 }},
		{"meta_largesize", func(o *Options) { o.MetaLargesize = true }},
		{"meta_largesize_irot", func(o *Options) { o.MetaLargesize, o.Irot = true, 1 }},
		{"extends_to_eof", func(o *Options) { o.ExtendsToEOF = true }},
		{"dup_ipma", func(o *Options) { o.DupIpma = true }},
		{"dup_ipco", func(o *Options) { o.DupIpco = true }},
		{"dup_iprp", func(o *Options) { o.DupIprp = true }},
		{"infe_v1", func(o *Options) { o.InfeVersion, o.Exif = 1, true }},
		{"wide_high_assoc", func(o *Options) { o.ExtraProps, o.IpmaWide, o.AssocHighIndex = 200, true, true }},
		{"wide_high_assoc_irot", func(o *Options) { o.ExtraProps, o.IpmaWide, o.AssocHighIndex, o.Irot = 200, true, true, 1 }},
		{"iloc_offset_near_max4", func(o *Options) {
			o.ExtraItem, o.ExtraItemExtents, o.ExtraItemOffset = true, 1, []byte{0xff, 0xff, 0xff, 0xfe}
		}},
		{"iloc_offset_near_max8", func(o *Options) {
			o.OffsetSize, o.ExtraItem, o.ExtraItemExtents = 8, true, 1
			o.ExtraItemOffset = []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xfe}
		}},
		{"iloc_9byte_value_over_64_bits", func(o *Options) { // COMPAT D5
			o.OffsetSize, o.ExtraItem, o.ExtraItemExtents = 9, true, 1
			o.ExtraItemOffset = []byte{0x01, 0, 0, 0, 0, 0, 0, 0, 0x05}
		}},
		{"iloc_zero_width_extent_flood", func(o *Options) { // COMPAT D7
			o.OffsetSize, o.LengthSize, o.BaseOffsetSize = 0, 0, 4
			o.ExtraItem, o.ExtraItemExtents = true, 65535
		}},
	}
	out := map[string]Options{}
	for _, layout := range []string{"mdat_first", "meta_first"} {
		for _, x := range vs {
			o := Default()
			o.Layout = layout
			x.mut(&o)
			out[fmt.Sprintf("%s/%s", layout, x.name)] = o
		}
	}
	return out
}
