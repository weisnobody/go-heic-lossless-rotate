// Package synth builds minimal, valid-enough synthetic HEIC files for
// testing without depending on any real photo (← tests/synth_heic.py).
//
// These files have no real HEVC-coded pixel data (mdat is just placeholder
// bytes) - heicrotate never interprets that payload, only the box structure
// around it, so a placeholder is sufficient to exercise every code path: box
// parsing, iloc offset patching, provenance insertion, and the two layout
// orders (mdat-before-meta and meta-before-mdat) that upstream's real-world
// bug turned out to depend on.
//
// Output is byte-identical to upstream's synth_heic.py for the same
// parameters; the differential suite (difftest/) enforces this.
package synth

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strings"
)

// Layout is the top-level box order. Values match synth_heic.py's strings.
type Layout string

const (
	MdatFirst Layout = "mdat_first" // ftyp, mdat, meta
	MetaFirst Layout = "meta_first" // ftyp, meta, mdat
)

// ExifPosition is where the Exif blob sits inside mdat's content.
type ExifPosition string

const (
	ExifEnd    ExifPosition = "end"    // immediately after the payload, at mdat's tail
	ExifMiddle ExifPosition = "middle" // after the payload, padded so it sits strictly inside mdat
)

// Options mirrors build_synthetic_heic's keyword arguments. Use
// DefaultOptions for Python's defaults - the zero value is not equivalent
// (Python defaults MdatPayload to non-empty and ExifOrientation to 1).
type Options struct {
	// Layout: MdatFirst or MetaFirst - the two orderings real HEIC files use
	// in the wild, and the distinction that exposed the real bug upstream's
	// test suite guards against.
	Layout      Layout
	MdatPayload []byte
	// IncludeIrot: if true, adds an 'irot' property (fast-path case). If
	// false, no 'irot' property exists at all (slow-path case).
	IncludeIrot bool
	IrotValue   int
	// IncludeExif: if true, adds a minimal embedded 'Exif' item with an
	// Orientation tag, stored via an absolute offset inside mdat
	// (construction_method 0), same as the real Samsung files upstream was
	// built against.
	IncludeExif     bool
	ExifOrientation uint16
	// ExifPosition: ExifEnd places the Exif blob immediately after the
	// payload, i.e. at the tail end of mdat. ExifMiddle places it after the
	// payload too, but pads additional bytes after it (equal to its own
	// length) so the Exif item's absolute offset lands strictly inside
	// mdat's content with real bytes on both sides - not at either edge.
	ExifPosition ExifPosition
	// ExtraMdat: if true, adds a second, unreferenced top-level 'mdat' box
	// (placeholder content, no iloc extent points into it) immediately after
	// the real one, regardless of layout. The format technically permits
	// more than one top-level 'mdat' box (rare in practice, but legal per
	// ISO/IEC 14496-12).
	ExtraMdat bool
}

// DefaultOptions returns build_synthetic_heic's default arguments.
func DefaultOptions() Options {
	return Options{
		Layout:          MdatFirst,
		MdatPayload:     bytes.Repeat([]byte("FAKE_PIXEL_DATA"), 20),
		ExifOrientation: 1,
		ExifPosition:    ExifEnd,
	}
}

func box(boxtype string, content []byte) []byte {
	out := binary.BigEndian.AppendUint32(nil, uint32(len(content)+8))
	out = append(out, boxtype...)
	return append(out, content...)
}

func fullbox(boxtype string, version uint8, flags uint32, content []byte) []byte {
	header := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return box(boxtype, append(header, content...))
}

func be16(v uint16) []byte { return binary.BigEndian.AppendUint16(nil, v) }
func be32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func le16(v uint16) []byte { return binary.LittleEndian.AppendUint16(nil, v) }
func le32(v uint32) []byte { return binary.LittleEndian.AppendUint32(nil, v) }

func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

type infeItem struct {
	id       uint16
	itemType string
}

// BuildSyntheticHEIC builds a minimal, single-image synthetic HEIC file
// (← build_synthetic_heic). It returns an error where Python raises
// ValueError, with the same message.
func BuildSyntheticHEIC(o Options) ([]byte, error) {
	const itemID = 1
	const exifItemID = 2

	ftyp := box("ftyp", cat([]byte("heic"), be32(0), []byte("mif1"), []byte("heic")))

	// --- Exif item payload (HEIF format: 4-byte tiff_header_offset, then
	// "Exif\x00\x00", then a minimal little-endian TIFF with one IFD0 entry:
	// tag 0x0112 (Orientation), type 3 (SHORT), count 1, value in-place.
	exifTIFF := cat(
		[]byte("II*\x00"), le32(8), // TIFF header, IFD0 @ offset 8
		le16(1),                        // 1 IFD0 entry
		le16(0x0112), le16(3), le32(1), // tag, type=SHORT, count=1
		le16(o.ExifOrientation), []byte{0, 0}, // value (2 bytes used) + pad
		le32(0), // next IFD offset = 0
	)
	exifBlob := cat(be32(6), []byte("Exif\x00\x00"), exifTIFF)

	if o.ExifPosition != ExifEnd && o.ExifPosition != ExifMiddle {
		return nil, fmt.Errorf("unknown exif_position %s", pyStrRepr(string(o.ExifPosition)))
	}

	mdatContent := bytes.Clone(o.MdatPayload)
	exifOffsetInMdat := 0
	if o.IncludeExif {
		exifOffsetInMdat = len(mdatContent)
		mdatContent = append(mdatContent, exifBlob...)
		if o.ExifPosition == ExifMiddle {
			// Pad with as many bytes as the Exif blob itself, so its offset
			// sits strictly inside mdat's content (real bytes before AND
			// after it), not coinciding with mdat's own final byte the way
			// ExifEnd always does.
			mdatContent = append(mdatContent, make([]byte, len(exifBlob))...)
		}
	}

	mdat := box("mdat", mdatContent)
	var extraMdatBox []byte
	if o.ExtraMdat {
		extraMdatBox = box("mdat", bytes.Repeat([]byte("UNREFERENCED_EXTRA_MDAT_PLACEHOLDER"), 4))
	}

	// --- meta box children ---
	hdlr := fullbox("hdlr", 0, 0, cat(be32(0), []byte("pict"), make([]byte, 12), []byte{0}))

	pitm := fullbox("pitm", 0, 0, be16(itemID))

	infeItems := []infeItem{{itemID, "hvc1"}}
	if o.IncludeExif {
		infeItems = append(infeItems, infeItem{exifItemID, "Exif"})
	}
	var infeBoxes []byte
	for _, it := range infeItems {
		infeContent := cat(be16(it.id), be16(0), []byte(it.itemType), []byte{0})
		infeBoxes = append(infeBoxes, fullbox("infe", 2, 0, infeContent)...)
	}
	iinf := fullbox("iinf", 0, 0, cat(be16(uint16(len(infeItems))), infeBoxes))

	// ipco: one 'ispe' (required-ish spatial extent), optionally 'irot'
	ispe := fullbox("ispe", 0, 0, cat(be32(100), be32(200)))
	ipcoChildren := ispe
	nProps := 1
	irotIndex := 0
	if o.IncludeIrot {
		ipcoChildren = cat(ipcoChildren, box("irot", []byte{byte(o.IrotValue & 0x3)}))
		nProps++
		irotIndex = nProps
	}
	ipco := box("ipco", ipcoChildren)

	type assoc struct {
		idx       int
		essential bool
	}
	assocs := []assoc{{1, true}} // ispe
	if o.IncludeIrot {
		assocs = append(assocs, assoc{irotIndex, true})
	}
	ipmaEntry := cat(be16(itemID), []byte{byte(len(assocs))})
	for _, a := range assocs {
		v := byte(a.idx)
		if a.essential {
			v |= 0x80
		}
		ipmaEntry = append(ipmaEntry, v)
	}
	ipma := fullbox("ipma", 0, 0, cat(be32(1), ipmaEntry))

	iprp := box("iprp", cat(ipco, ipma))

	// iloc: version 1 (so construction_method field exists), one extent per
	// item. Per ISO/IEC 14496-12, for construction_method 0 the item's data
	// lives at ABSOLUTE FILE OFFSET (base_offset + extent_offset) - not an
	// offset relative to mdat's own content. We use base_offset=0 and put
	// the true absolute file position directly in extent_offset, computed
	// below once we know where mdat's content will actually land (which
	// depends on Layout, and - for MetaFirst - on meta's own size, since
	// meta precedes mdat in that layout).
	buildMeta := func(mainItemAbsOffset, exifItemAbsOffset uint32) []byte {
		const ilocVersion = 1
		ilocItems := be16(uint16(len(infeItems)))
		ilocItemBytes := func(iid, constrMethod uint16, baseOffset, extentOffset, extentLength uint32) []byte {
			return cat(
				be16(iid),
				be16(constrMethod),
				be16(0),          // data_reference_index
				be32(baseOffset), // base_offset (4 bytes)
				be16(1),          // extent_count
				be32(extentOffset),
				be32(extentLength),
			)
		}
		itemsBytes := ilocItemBytes(itemID, 0, 0, mainItemAbsOffset, uint32(len(o.MdatPayload)))
		if o.IncludeExif {
			itemsBytes = append(itemsBytes,
				ilocItemBytes(exifItemID, 0, 0, exifItemAbsOffset, uint32(len(exifBlob)))...)
		}
		iloc := fullbox("iloc", ilocVersion, 0, cat([]byte{0x44}, []byte{0x40}, ilocItems, itemsBytes))
		metaContent := cat(hdlr, iinf, pitm, iprp, iloc)
		return fullbox("meta", 0, 0, metaContent)
	}

	// Pass 1: build with placeholder (0) offsets just to measure meta's
	// final size - the offset VALUE never changes iloc's byte length (both
	// fields are fixed-width), so this size is already final.
	metaPlaceholder := buildMeta(0, 0)

	var mdatContentAbsStart int
	switch o.Layout {
	case MdatFirst:
		mdatContentAbsStart = len(ftyp) + 8 // + mdat's own box header
	case MetaFirst:
		mdatContentAbsStart = len(ftyp) + len(metaPlaceholder) + 8
	default:
		return nil, fmt.Errorf("unknown layout %s", pyStrRepr(string(o.Layout)))
	}

	// Pass 2: rebuild with the real absolute offsets now that we know them.
	meta := buildMeta(uint32(mdatContentAbsStart), uint32(mdatContentAbsStart+exifOffsetInMdat))
	if len(meta) != len(metaPlaceholder) {
		return nil, fmt.Errorf("meta size must not change between placeholder and final offset values")
	}

	if o.Layout == MdatFirst {
		return cat(ftyp, mdat, extraMdatBox, meta), nil
	}
	return cat(ftyp, meta, mdat, extraMdatBox), nil
}

// pyStrRepr renders s the way Python's repr() renders a str, for the
// characters an option value can plausibly contain (error-message parity
// with synth_heic.py's ValueError text).
func pyStrRepr(s string) string {
	quote := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = `"`
	}
	var b strings.Builder
	b.WriteString(quote)
	for _, r := range s {
		switch {
		case r == '\\':
			b.WriteString(`\\`)
		case string(r) == quote:
			b.WriteByte('\\')
			b.WriteString(quote)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\x%02x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteString(quote)
	return b.String()
}
