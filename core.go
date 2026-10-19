// Container-level parsing/editing logic for lossless, reversible HEIC/HEIF
// rotation (← heic_rotate/core.py).
//
// This file holds everything that reads or rewrites ISOBMFF box bytes:
// generic box iteration, the individual box parsers/builders (pitm, ipma,
// ipco, iloc, iinf), the legacy embedded Exif Orientation sync, the private
// provenance ('uuid') box format, and the top-level ApplyRotation /
// ReverseRotation / GatherInfo / FormatInfo / VerifyStructure operations the
// CLI drives.
//
// It has no knowledge of argv, subcommands, or file I/O paths - it takes and
// returns []byte, and returns *BoxParseError / *ProvenanceError on malformed
// input. Sections and functions appear in the same order as core.py, so the
// two files can be read side by side.

package heicrotate

import (
	"encoding/binary"
	"fmt"
	"iter"
	"math/big"
	"math/bits"
	"strconv"
	"strings"

	"github.com/pjg/go-heic-lossless-rotate/internal/pyfmt"
)

// BoxParseError reports malformed or unsupported container structure
// (← class BoxParseError).
type BoxParseError struct {
	Msg string
	Err error // underlying cause, if any (Python's `raise ... from e`)
}

func (e *BoxParseError) Error() string { return e.Msg }
func (e *BoxParseError) Unwrap() error { return e.Err }

// ProvenanceError reports a missing, malformed, or inconsistent provenance
// record, or a failed reversibility check (← class ProvenanceError).
type ProvenanceError struct {
	Msg string
	Err error // underlying cause, if any (Python's `raise ... from e`)
}

func (e *ProvenanceError) Error() string { return e.Msg }
func (e *ProvenanceError) Unwrap() error { return e.Err }

func boxParseErrorf(format string, args ...any) *BoxParseError {
	return &BoxParseError{Msg: fmt.Sprintf(format, args...)}
}

func provenanceErrorf(format string, args ...any) *ProvenanceError {
	return &ProvenanceError{Msg: fmt.Sprintf(format, args...)}
}

// FormatVersion is the provenance/metadata format version this port writes.
const FormatVersion = 2

// SupportedReverseFormatVersions lists the provenance/metadata format
// versions ReverseRotation knows how to restore. v1: the pristine 'iprp' box
// (ipco+ipma) was saved in full (OPRP). v2: only the 'irot' property itself
// is saved (OIRT, +OIPF for the slow-path case). Kept as its own value so
// -V and ReverseRotation's check can never drift out of sync.
var SupportedReverseFormatVersions = []int{1, 2}

// versionTuple parses Version ('MAJOR.MINOR.PATCH') into three small ints,
// for compact fixed-width storage in the provenance record's TVER entry.
// Fixed-width (1 byte each) is deliberate: it lets TVER update in place on
// every edit as a same-size splice, the same way CCRC already does
// (← _version_tuple).
func versionTuple() ([3]uint8, error) {
	parts := strings.Split(Version, ".")
	if len(parts) != 3 {
		return [3]uint8{}, fmt.Errorf("VERSION must be MAJOR.MINOR.PATCH, got %q", Version)
	}
	var out [3]uint8
	for i, name := range []string{"major", "minor", "patch"} {
		v, err := strconv.Atoi(parts[i])
		if err != nil {
			return [3]uint8{}, fmt.Errorf("VERSION must be MAJOR.MINOR.PATCH, got %q", Version)
		}
		if v < 0 || v > 255 {
			return [3]uint8{}, fmt.Errorf("VERSION component %s=%d doesn't fit in the "+
				"provenance record's fixed 1-byte TVER field (0-255)", name, v)
		}
		out[i] = uint8(v)
	}
	return out, nil
}

// ProvenanceUUID is the deterministic, tool-specific UUID identifying the
// private provenance box: uuid5(NAMESPACE_URL,
// 'urn:heic_rotate.py:provenance:v1') = e850777c-3416-50a4-9fa8-6493cfc13f31.
//
// The seed string 'heic_rotate.py' is FROZEN, not a stale name: it is baked
// into the UUID of every provenance box ever written by any version of
// heic-lossless-rotate. Changing it would make already-edited files
// unrecognizable as this tool's own. Must never change.
var ProvenanceUUID = [16]byte{
	0xe8, 0x50, 0x77, 0x7c, 0x34, 0x16, 0x50, 0xa4,
	0x9f, 0xa8, 0x64, 0x93, 0xcf, 0xc1, 0x3f, 0x31,
}

// ---------------------------------------------------------------------------
// Generic ISOBMFF box reading helpers (read-only)
// ---------------------------------------------------------------------------

// BoxHeader is read_box_header's (start, header_len, content_start,
// content_end, boxtype) tuple. ContentEnd may be less than ContentStart for
// a malformed box whose declared size is smaller than its own header - as in
// Python, that is not an error by itself.
type BoxHeader struct {
	Start, HeaderLen, ContentStart, ContentEnd int
	Type                                       [4]byte
}

// Is reports whether the box's type is boxtype (a 4-character type code).
func (h BoxHeader) Is(boxtype string) bool { return string(h.Type[:]) == boxtype }

// ReadBoxHeader parses the box header at pos (← read_box_header). ok is
// false at EOF (fewer than 8 bytes left), which Python reports as None.
func ReadBoxHeader(data []byte, pos int) (hdr BoxHeader, ok bool, err error) {
	if pos+8 > len(data) {
		return BoxHeader{}, false, nil
	}
	size32, _ := unpackU32(data, pos)
	var boxtype [4]byte
	copy(boxtype[:], data[pos+4:pos+8])
	size := uint64(size32)
	headerLen := 8
	if size == 1 {
		if pos+16 > len(data) {
			return BoxHeader{}, false, boxParseErrorf("truncated 64-bit box size")
		}
		size, _ = unpackU64(data, pos+8)
		headerLen = 16
	} else if size == 0 {
		size = uint64(len(data) - pos) // extends to EOF
	}
	if size > uint64(len(data)-pos) {
		return BoxHeader{}, false, boxParseErrorf("box %s at %d claims size %d beyond EOF",
			pyfmt.BytesRepr(boxtype[:]), pos, size)
	}
	return BoxHeader{
		Start:        pos,
		HeaderLen:    headerLen,
		ContentStart: pos + headerLen,
		ContentEnd:   pos + int(size),
		Type:         boxtype,
	}, true, nil
}

// errZeroSizeBox is the error for a box whose 64-bit size is 0: it never
// advances, so Python's iteration over it loops forever (docs/COMPAT.md D6).
func errZeroSizeBox(hdr BoxHeader) *BoxParseError {
	return boxParseErrorf("box %s at %d has a 64-bit size of 0 and cannot be stepped over",
		pyfmt.BytesRepr(hdr.Type[:]), hdr.Start)
}

// IterBoxes yields each immediate child box header in data[start:end]
// (← iter_boxes). Like Python's generator it is lazy: a consumer that stops
// early never sees a malformed box further on. On error it yields the error
// once and stops. A box with a 64-bit size of 0 is yielded once, then the
// D6 error - where Python would yield it forever.
func IterBoxes(data []byte, start, end int) iter.Seq2[BoxHeader, error] {
	return func(yield func(BoxHeader, error) bool) {
		pos := start
		for pos < end {
			hdr, ok, err := ReadBoxHeader(data, pos)
			if err != nil {
				yield(BoxHeader{}, err)
				return
			}
			if !ok {
				return
			}
			if !yield(hdr, nil) {
				return
			}
			if hdr.ContentEnd <= pos {
				yield(BoxHeader{}, errZeroSizeBox(hdr))
				return
			}
			pos = hdr.ContentEnd
		}
	}
}

// listBoxes mirrors list(iter_boxes(data, start, end)).
func listBoxes(data []byte, start, end int) ([]BoxHeader, error) {
	var out []BoxHeader
	for hdr, err := range IterBoxes(data, start, end) {
		if err != nil {
			return nil, err
		}
		out = append(out, hdr)
	}
	return out, nil
}

// FindChild returns the first immediate child box of the given type
// (← find_child). ok is false if there is none.
func FindChild(data []byte, start, end int, boxtype string) (hdr BoxHeader, ok bool, err error) {
	for h, err := range IterBoxes(data, start, end) {
		if err != nil {
			return BoxHeader{}, false, err
		}
		if h.Is(boxtype) {
			return h, true, nil
		}
	}
	return BoxHeader{}, false, nil
}

// FindUniqueChild is like FindChild, but REFUSES rather than silently picking
// the first match if more than one is present (← find_unique_child). Some box
// types the standard allows to repeat in places we don't expect - notably,
// ISO/IEC 14496-12 allows an 'iprp' to contain more than one 'ipma' (real
// encoders essentially never do this, but real DECODERS are written to handle
// it, per e.g. the Nokia HEIF reference implementation's reader). Rebuilding
// 'iprp' from just the first match found would silently DISCARD any
// associations recorded in a second 'ipma' - real data loss, not just a
// missed offset patch - so we refuse loudly instead.
func FindUniqueChild(data []byte, start, end int, boxtype, context string) (hdr BoxHeader, ok bool, err error) {
	var matches []BoxHeader
	for h, err := range IterBoxes(data, start, end) {
		if err != nil {
			return BoxHeader{}, false, err
		}
		if h.Is(boxtype) {
			matches = append(matches, h)
		}
	}
	if len(matches) > 1 {
		return BoxHeader{}, false, boxParseErrorf(
			"found %d '%s' boxes in %s, expected at most 1 - the standard permits more "+
				"than one in some cases, but this script does not support "+
				"rebuilding such a file without risking silently dropping "+
				"data from the boxes beyond the first", len(matches), boxtype, context)
	}
	if len(matches) == 0 {
		return BoxHeader{}, false, nil
	}
	return matches[0], true, nil
}

// ---------------------------------------------------------------------------
// pitm - Primary Item box
// ---------------------------------------------------------------------------

// ParsePitm returns the primary item id (← parse_pitm).
func ParsePitm(data []byte, cStart, cEnd int) (uint32, error) {
	version, err := pyIndex(data, cStart)
	if err != nil {
		return 0, err
	}
	if version == 0 {
		id, err := unpackU16(data, cStart+4)
		return uint32(id), err
	}
	return unpackU32(data, cStart+4)
}

// ---------------------------------------------------------------------------
// ipma - Item Property Association box
// ---------------------------------------------------------------------------

// IpmaAssoc is one (prop_index, essential) association.
type IpmaAssoc struct {
	PropIndex int // 1-based index into ipco's children
	Essential bool
}

// IpmaEntry is one [item_id, [(prop_index, essential), ...]] entry.
type IpmaEntry struct {
	ItemID uint32
	Assocs []IpmaAssoc
}

// ParseIpma returns ipma's version, flags, and entries in file order
// (← parse_ipma).
func ParseIpma(data []byte, cStart, cEnd int) (version uint8, flags uint32, entries []IpmaEntry, err error) {
	if version, err = pyIndex(data, cStart); err != nil {
		return
	}
	f, _ := intFromBytes(data, cStart+1, 3) // 3 bytes never exceed 64 bits
	flags = uint32(f)
	pos := cStart + 4
	entryCount, err := unpackU32(data, pos)
	if err != nil {
		return
	}
	pos += 4
	// entryCount comes from the file: grow entries as bytes are actually
	// consumed rather than preallocating from it.
	for range entryCount {
		var itemID uint32
		if version < 1 {
			var id16 uint16
			if id16, err = unpackU16(data, pos); err != nil {
				return
			}
			itemID = uint32(id16)
			pos += 2
		} else {
			if itemID, err = unpackU32(data, pos); err != nil {
				return
			}
			pos += 4
		}
		var assocCount byte
		if assocCount, err = pyIndex(data, pos); err != nil {
			return
		}
		pos++
		var assocs []IpmaAssoc
		for range assocCount {
			if flags&1 != 0 {
				var v uint16
				if v, err = unpackU16(data, pos); err != nil {
					return
				}
				pos += 2
				assocs = append(assocs, IpmaAssoc{PropIndex: int(v & 0x7fff), Essential: v&0x8000 != 0})
			} else {
				var v byte
				if v, err = pyIndex(data, pos); err != nil {
					return
				}
				pos++
				assocs = append(assocs, IpmaAssoc{PropIndex: int(v & 0x7f), Essential: v&0x80 != 0})
			}
		}
		entries = append(entries, IpmaEntry{ItemID: itemID, Assocs: assocs})
	}
	if pos != cEnd {
		return 0, 0, nil, boxParseErrorf("ipma parse mismatch: consumed %d, expected %d", pos-cStart, cEnd-cStart)
	}
	return version, flags, entries, nil
}

// BuildIpma serializes a full ipma box, including its 8-byte header, from
// structured data (← build_ipma).
func BuildIpma(version uint8, flags uint32, entries []IpmaEntry) ([]byte, error) {
	body := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	body = binary.BigEndian.AppendUint32(body, uint32(len(entries)))
	wideIndex := flags&1 != 0
	for _, e := range entries {
		if version < 1 {
			body = binary.BigEndian.AppendUint16(body, uint16(e.ItemID))
		} else {
			body = binary.BigEndian.AppendUint32(body, e.ItemID)
		}
		if len(e.Assocs) > 255 { // Python: bytes([len(assocs)]) raises ValueError (COMPAT D2)
			return nil, boxParseErrorf("ipma entry for item %d would have %d associations, more than the 255 an entry can hold",
				e.ItemID, len(e.Assocs))
		}
		body = append(body, byte(len(e.Assocs)))
		for _, a := range e.Assocs {
			if wideIndex {
				if a.PropIndex > 0x7fff {
					return nil, boxParseErrorf("property index too large for 15-bit ipma field")
				}
				v := uint16(a.PropIndex)
				if a.Essential {
					v |= 0x8000
				}
				body = binary.BigEndian.AppendUint16(body, v)
			} else {
				if a.PropIndex > 0x7f {
					return nil, boxParseErrorf("property index too large for 7-bit ipma field " +
						"(file would need 16-bit ipma flag - not auto-upgraded)")
				}
				v := byte(a.PropIndex)
				if a.Essential {
					v |= 0x80
				}
				body = append(body, v)
			}
		}
	}
	return makeBox("ipma", body), nil
}

// makeBox mirrors struct.pack('>I4s', len(content) + 8, boxtype) + content.
func makeBox(boxtype string, content []byte) []byte {
	out := make([]byte, 0, 8+len(content))
	out = binary.BigEndian.AppendUint32(out, uint32(len(content)+8))
	out = append(out, boxtype...)
	return append(out, content...)
}

// ParseIpcoChildren returns ipco's child property boxes (← parse_ipco_children).
func ParseIpcoChildren(data []byte, cStart, cEnd int) ([]BoxHeader, error) {
	return listBoxes(data, cStart, cEnd)
}

// ---------------------------------------------------------------------------
// iloc - Item Location box (full parse, with enough info to rebuild
// identically except for patched offset values)
// ---------------------------------------------------------------------------

// Iloc is parse_iloc's dict: everything needed to rebuild the box content
// byte for byte.
type Iloc struct {
	Version                                           uint8
	Flags                                             uint32
	OffsetSize, LengthSize, BaseOffsetSize, IndexSize int // field widths in bytes (0-15)
	Items                                             []IlocItem
}

// IlocItem is one parse_iloc item.
type IlocItem struct {
	ItemID       uint32
	ConstrMethod int
	// RawConstrField is the 12 reserved bits + 4-bit construction_method,
	// kept raw so reserved bits round-trip exactly. nil for iloc v0
	// (Python None), which has no such field.
	RawConstrField *uint16
	DataRefIndex   uint16
	BaseOffset     uint64
	Extents        []IlocExtent
}

// IlocExtent is one [ext_index, ext_offset, ext_length] extent. Index is nil
// when index_size is 0 (Python None).
type IlocExtent struct {
	Index          *uint64
	Offset, Length uint64
}

// ParseIloc parses iloc box content (← parse_iloc).
func ParseIloc(data []byte, cStart, cEnd int) (*Iloc, error) {
	version, err := pyIndex(data, cStart)
	if err != nil {
		return nil, err
	}
	flags, _ := intFromBytes(data, cStart+1, 3)
	pos := cStart + 4
	b, err := pyIndex(data, pos)
	if err != nil {
		return nil, err
	}
	pos++
	b2, err := pyIndex(data, pos)
	if err != nil {
		return nil, err
	}
	pos++
	iloc := &Iloc{
		Version:        version,
		Flags:          uint32(flags),
		OffsetSize:     int(b >> 4),
		LengthSize:     int(b & 0xf),
		BaseOffsetSize: int(b2 >> 4),
	}
	v12 := version == 1 || version == 2
	if v12 {
		iloc.IndexSize = int(b2 & 0xf)
	}

	readU16 := func() (uint16, error) {
		v, err := unpackU16(data, pos)
		pos += 2
		return v, err
	}
	readU32 := func() (uint32, error) {
		v, err := unpackU32(data, pos)
		pos += 4
		return v, err
	}
	readID := func() (uint32, error) { // H for version < 2, else I
		if version < 2 {
			v, err := readU16()
			return uint32(v), err
		}
		return readU32()
	}
	readInt := func(n int) (uint64, error) {
		if n == 0 {
			return 0, nil
		}
		v, err := intFromBytes(data, pos, n)
		pos += n
		return v, err
	}

	itemCount, err := readID()
	if err != nil {
		return nil, err
	}
	// Counts come from the file: grow as bytes are consumed instead of
	// preallocating. Zero-width fields consume no bytes at all, so cap the
	// total number of extents: real extents can't outnumber the file's
	// bytes, but crafted counts could otherwise build billions
	// (docs/COMPAT.md D7).
	totalExtents := 0
	for range itemCount {
		var it IlocItem
		if it.ItemID, err = readID(); err != nil {
			return nil, err
		}
		if v12 {
			raw, err := readU16()
			if err != nil {
				return nil, err
			}
			it.RawConstrField = &raw
			it.ConstrMethod = int(raw & 0xf)
		}
		if it.DataRefIndex, err = readU16(); err != nil {
			return nil, err
		}
		if it.BaseOffset, err = readInt(iloc.BaseOffsetSize); err != nil {
			return nil, err
		}
		extentCount, err := readU16()
		if err != nil {
			return nil, err
		}
		totalExtents += int(extentCount)
		if totalExtents > len(data) {
			return nil, boxParseErrorf("iloc declares more extents (%d so far) than the data has bytes (%d)",
				totalExtents, len(data))
		}
		for range extentCount {
			var ext IlocExtent
			if iloc.IndexSize != 0 {
				idx, err := readInt(iloc.IndexSize)
				if err != nil {
					return nil, err
				}
				ext.Index = &idx
			}
			if ext.Offset, err = readInt(iloc.OffsetSize); err != nil {
				return nil, err
			}
			if ext.Length, err = readInt(iloc.LengthSize); err != nil {
				return nil, err
			}
			it.Extents = append(it.Extents, ext)
		}
		iloc.Items = append(iloc.Items, it)
	}
	if pos != cEnd {
		return nil, boxParseErrorf("iloc parse mismatch: consumed %d, expected %d", pos-cStart, cEnd-cStart)
	}
	return iloc, nil
}

// errIlocFieldOverflow is put_int's error for a value wider than its field.
// val is a decimal string because a patched value may exceed 64 bits.
func errIlocFieldOverflow(val string, n int) *BoxParseError {
	return boxParseErrorf("patched iloc offset %s overflows original field width "+
		"(%d bytes) - file needs a wider iloc field than the original, which "+
		"this script does not (yet) upgrade", val, n)
}

// putInt mirrors build_iloc_content's put_int for a value already known to
// be non-negative and to fit in 64 bits.
func putInt(dst []byte, n int, val uint64) ([]byte, error) {
	if n == 0 {
		if val != 0 {
			return nil, boxParseErrorf("value doesn't fit in zero-width iloc field")
		}
		return dst, nil
	}
	if n < 8 && val > (uint64(1)<<(8*n))-1 {
		return nil, errIlocFieldOverflow(strconv.FormatUint(val, 10), n)
	}
	for i := n - 1; i >= 0; i-- { // big-endian, zero-padded beyond 8 bytes
		if i < 8 {
			dst = append(dst, byte(val>>(8*i)))
		} else {
			dst = append(dst, 0)
		}
	}
	return dst, nil
}

// BuildIlocContent rebuilds iloc box CONTENT (no 8-byte header) with the
// same field widths, flags, and reserved bits as the original - only the
// numeric offset/length values deliberately changed differ
// (← build_iloc_content).
func BuildIlocContent(iloc *Iloc) ([]byte, error) {
	v12 := iloc.Version == 1 || iloc.Version == 2
	body := []byte{iloc.Version, byte(iloc.Flags >> 16), byte(iloc.Flags >> 8), byte(iloc.Flags)}
	idx := 0
	if v12 {
		idx = iloc.IndexSize
	}
	body = append(body, byte(iloc.OffsetSize<<4|iloc.LengthSize), byte(iloc.BaseOffsetSize<<4|idx))
	putID := func(v uint32) {
		if iloc.Version < 2 {
			body = binary.BigEndian.AppendUint16(body, uint16(v))
		} else {
			body = binary.BigEndian.AppendUint32(body, v)
		}
	}
	putID(uint32(len(iloc.Items)))
	var err error
	for _, it := range iloc.Items {
		putID(it.ItemID)
		if v12 {
			// Echo back the original reserved+construction_method field
			// verbatim - we never change construction_method ourselves.
			var raw uint16
			if it.RawConstrField != nil {
				raw = *it.RawConstrField
			}
			body = binary.BigEndian.AppendUint16(body, raw)
		}
		body = binary.BigEndian.AppendUint16(body, it.DataRefIndex)
		if body, err = putInt(body, iloc.BaseOffsetSize, it.BaseOffset); err != nil {
			return nil, err
		}
		body = binary.BigEndian.AppendUint16(body, uint16(len(it.Extents)))
		for _, ext := range it.Extents {
			if iloc.IndexSize != 0 {
				var index uint64
				if ext.Index != nil {
					index = *ext.Index
				}
				if body, err = putInt(body, iloc.IndexSize, index); err != nil {
					return nil, err
				}
			}
			if body, err = putInt(body, iloc.OffsetSize, ext.Offset); err != nil {
				return nil, err
			}
			if body, err = putInt(body, iloc.LengthSize, ext.Length); err != nil {
				return nil, err
			}
		}
	}
	return body, nil
}

// PatchIlocOffsetsAtThreshold shifts every absolute-offset iloc extent that
// points at or past threshold by delta, returning the rebuilt iloc content
// and whether anything changed (← patch_iloc_offsets_at_threshold).
//
// Deliberately patches at the EXTENT level only, and never touches
// base_offset - the standard allows any split between base_offset and
// per-extent offset. Computing each extent's TRUE absolute position as
// base_offset + extent_offset and comparing THAT against the threshold, then
// adjusting only the extent field, is correct regardless of how a given
// encoder chose to apportion the two - including a single item's extents
// straddling the threshold, which a base-offset-oriented patch cannot
// represent.
//
// Python's integers are unbounded, so a patched value can go negative or
// past 64 bits before build_iloc_content's put_int rejects it. Those values
// are checked here, in the same order put_int would meet them: an
// over-wide value gets Python's own overflow message; a negative one, which
// Python crashes on (OverflowError), is a *BoxParseError (COMPAT D4); one
// that only fits a field wider than 8 bytes is refused (COMPAT D5).
func PatchIlocOffsetsAtThreshold(data []byte, ilocHdr *BoxHeader, threshold, delta int) ([]byte, bool, error) {
	if ilocHdr == nil {
		return nil, false, nil
	}
	iloc, err := ParseIloc(data, ilocHdr.ContentStart, ilocHdr.ContentEnd)
	if err != nil {
		return nil, false, err
	}
	anyChanged := false
	for i := range iloc.Items {
		it := &iloc.Items[i]
		if it.ConstrMethod != 0 {
			continue // idat-relative or item-relative: not a file offset
		}
		for j := range it.Extents {
			ext := &it.Extents[j]
			abs, carry := bits.Add64(it.BaseOffset, ext.Offset, 0)
			if carry == 0 && abs < uint64(threshold) {
				continue
			}
			patched, err := shiftIlocOffset(ext.Offset, delta, iloc.OffsetSize)
			if err != nil {
				return nil, false, err
			}
			ext.Offset = patched
			anyChanged = true
		}
	}
	if !anyChanged {
		return nil, false, nil
	}
	content, err := BuildIlocContent(iloc)
	if err != nil {
		return nil, false, err
	}
	return content, true, nil
}

// shiftIlocOffset computes off + delta for an n-byte iloc field, reporting
// the error build_iloc_content's put_int would raise for the result.
func shiftIlocOffset(off uint64, delta, n int) (uint64, error) {
	if n == 0 { // put_int checks width 0 first - even for a negative value
		if delta != 0 { // a zero-width field always reads as 0
			return 0, boxParseErrorf("value doesn't fit in zero-width iloc field")
		}
		return 0, nil
	}
	if delta < 0 {
		d := uint64(-delta)
		if off < d {
			return 0, boxParseErrorf("patched iloc offset %d would be negative "+
				"(shifting %d by %d)", int64(off)-int64(d), off, delta)
		}
		return off - d, nil
	}
	sum, carry := bits.Add64(off, uint64(delta), 0)
	if carry != 0 { // Python's value is 2^64 + sum
		v := new(big.Int).Lsh(big.NewInt(1), 64)
		v.Add(v, new(big.Int).SetUint64(sum))
		if n <= 8 {
			return 0, errIlocFieldOverflow(v.String(), n)
		}
		return 0, boxParseErrorf("patched iloc offset %s doesn't fit in 64 bits", v.String())
	}
	if n < 8 && sum > (uint64(1)<<(8*n))-1 {
		return 0, errIlocFieldOverflow(strconv.FormatUint(sum, 10), n)
	}
	return sum, nil
}

// @@SECTIONS@@

// ---------------------------------------------------------------------------
// Exit code and -V text
// ---------------------------------------------------------------------------

// ErrorExitCode is the CLI exit code for a genuine error - kept out of 0-4
// so it never collides with info's rotation-state exit codes
// (← ERROR_EXIT_CODE).
const ErrorExitCode = 10

// VersionString returns the text printed by -V/--version (← _version_string).
func VersionString() string {
	supported := make([]string, len(SupportedReverseFormatVersions))
	for i, v := range SupportedReverseFormatVersions {
		supported[i] = strconv.Itoa(v)
	}
	return "heic-lossless-rotate " + Version + "\n" +
		"metadata (provenance) format version: " + strconv.Itoa(FormatVersion) + "\n" +
		"metadata format versions supported by reverse: " + strings.Join(supported, ", ")
}
