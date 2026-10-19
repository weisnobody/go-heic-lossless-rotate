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
	"hash/crc32"
	"io"
	"iter"
	"maps"
	"math"
	"math/big"
	"math/bits"
	"slices"
	"sort"
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
	flags, _ := intFromBytes(data, cStart+1, 3) // 3 bytes always fit
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
	// Values wider than 64 bits (which Python reads as big ints) and
	// runaway zero-width extent counts are Go-only refusals (docs/COMPAT.md
	// D5, D7). Both are deferred until after the mismatch check below, so
	// they only ever fire where Python's own parse would have succeeded -
	// never in place of Python's own error.
	tooWide := false
	readInt := func(n int) uint64 {
		if n == 0 {
			return 0
		}
		v, ok := intFromBytes(data, pos, n)
		tooWide = tooWide || !ok
		pos += n
		return v
	}

	itemCount, err := readID()
	if err != nil {
		return nil, err
	}
	// Counts come from the file, so grow as bytes are consumed instead of
	// preallocating. Once pos runs past cEnd the parse can only end in an
	// error (pos never moves backward, and the final check requires
	// pos == cEnd), so the remaining extents are skipped arithmetically -
	// Python reads them as zeros from past the end of the data - giving the
	// exact "consumed" count without storing them.
	extentWidth := iloc.IndexSize + iloc.OffsetSize + iloc.LengthSize
	zeroWidthExtents := 0
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
		it.BaseOffset = readInt(iloc.BaseOffsetSize)
		extentCount, err := readU16()
		if err != nil {
			return nil, err
		}
		for j := range int(extentCount) {
			if pos > cEnd {
				pos += (int(extentCount) - j) * extentWidth
				break
			}
			if extentWidth == 0 {
				// Zero-byte extents never advance pos, so only the
				// 65535-per-item count bounds how many a crafted file can
				// declare.
				if zeroWidthExtents++; zeroWidthExtents > maxZeroWidthExtents {
					zeroWidthExtents += int(extentCount) - j - 1 // count the rest; store none
					break
				}
			}
			var ext IlocExtent
			if iloc.IndexSize != 0 {
				idx := readInt(iloc.IndexSize)
				ext.Index = &idx
			}
			ext.Offset = readInt(iloc.OffsetSize)
			ext.Length = readInt(iloc.LengthSize)
			it.Extents = append(it.Extents, ext)
		}
		iloc.Items = append(iloc.Items, it)
	}
	if pos != cEnd {
		return nil, boxParseErrorf("iloc parse mismatch: consumed %d, expected %d", pos-cStart, cEnd-cStart)
	}
	if tooWide {
		return nil, boxParseErrorf("iloc has a field value that doesn't fit in 64 bits")
	}
	if zeroWidthExtents > maxZeroWidthExtents {
		return nil, boxParseErrorf("iloc declares %d zero-width extents, more than the %d this port will hold in memory",
			zeroWidthExtents, maxZeroWidthExtents)
	}
	return iloc, nil
}

// maxZeroWidthExtents bounds how many zero-width extents ParseIloc will
// materialize (docs/COMPAT.md D7): about 40 MB. Reaching it takes 17+ items
// each declaring 65535 empty extents - Python would need gigabytes.
const maxZeroWidthExtents = 1 << 20

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

// ---------------------------------------------------------------------------
// iinf - find an item of a given type (used to locate the 'Exif' item, if any)
// ---------------------------------------------------------------------------

// IinfItem is one (item_id, item_type) pair. ItemType is normally 4 bytes,
// but - as in Python, which slices it - may be shorter in a truncated box.
type IinfItem struct {
	ItemID   uint32
	ItemType string
}

// ParseIinfItems returns the (item_id, item_type) of every version 2+ 'infe'
// entry in iinf (← parse_iinf_items).
func ParseIinfItems(data []byte, cStart, cEnd int) ([]IinfItem, error) {
	version, err := pyIndex(data, cStart)
	if err != nil {
		return nil, err
	}
	pos := cStart + 4
	if version == 0 {
		pos += 2
	} else {
		pos += 4
	}
	var items []IinfItem
	for hdr, err := range IterBoxes(data, pos, cEnd) {
		if err != nil {
			return nil, err
		}
		if !hdr.Is("infe") {
			continue
		}
		cs := hdr.ContentStart
		v, err := pyIndex(data, cs)
		if err != nil {
			return nil, err
		}
		if v < 2 {
			continue
		}
		var it IinfItem
		if v == 2 {
			id, err := unpackU16(data, cs+4)
			if err != nil {
				return nil, err
			}
			it = IinfItem{uint32(id), string(pySlice(data, cs+8, cs+12))}
		} else {
			id, err := unpackU32(data, cs+4)
			if err != nil {
				return nil, err
			}
			it = IinfItem{id, string(pySlice(data, cs+10, cs+14))}
		}
		items = append(items, it)
	}
	return items, nil
}

// ---------------------------------------------------------------------------
// Legacy embedded Exif 'Orientation' tag: locate the 2-byte value field
// ---------------------------------------------------------------------------

// IrotToExifOrientation maps HEIF irot (anticlockwise quarter turns needed
// for display) to the legacy TIFF/Exif Orientation code (values that use no
// mirroring: 1/3/6/8):
//
//	irot=0 (0 deg)                          -> Exif 1 (Horizontal / normal)
//	irot=1 (90 deg CCW)                     -> Exif 8 (Rotate 270 CW == 90 CCW)
//	irot=2 (180 deg)                        -> Exif 3 (Rotate 180)
//	irot=3 (270 deg CCW == needs 90 deg CW) -> Exif 6 (Rotate 90 CW)
var IrotToExifOrientation = [4]uint16{1, 8, 3, 6}

// ExifOrientationToIrot is the inverse of IrotToExifOrientation.
var ExifOrientationToIrot = map[uint16]int{1: 0, 8: 1, 3: 2, 6: 3}

// ExifItemLocation is find_exif_item_location's (item_id, absolute_data_start,
// length). AbsStart and Length come straight from iloc and may point past
// the end of the data.
type ExifItemLocation struct {
	ItemID           uint32
	AbsStart, Length uint64
}

// FindExifItemLocation returns where the first 'Exif'-typed item's data
// lives, or ok=false if there isn't one stored at an absolute file offset
// (← find_exif_item_location).
func FindExifItemLocation(data []byte, metaBodyStart, metaCEnd int) (loc ExifItemLocation, ok bool, err error) {
	iinfHdr, iinfOK, err := FindChild(data, metaBodyStart, metaCEnd, "iinf")
	if err != nil {
		return loc, false, err
	}
	ilocHdr, ilocOK, err := FindChild(data, metaBodyStart, metaCEnd, "iloc")
	if err != nil || !iinfOK || !ilocOK {
		return loc, false, err
	}
	items, err := ParseIinfItems(data, iinfHdr.ContentStart, iinfHdr.ContentEnd)
	if err != nil {
		return loc, false, err
	}
	var exifID uint32
	found := false
	for _, it := range items {
		if it.ItemType == "Exif" {
			exifID, found = it.ItemID, true
			break
		}
	}
	if !found {
		return loc, false, nil
	}
	iloc, err := ParseIloc(data, ilocHdr.ContentStart, ilocHdr.ContentEnd)
	if err != nil {
		return loc, false, err
	}
	for _, it := range iloc.Items {
		if it.ItemID != exifID {
			continue
		}
		if it.ConstrMethod != 0 || len(it.Extents) == 0 {
			return loc, false, nil // only handle the simple/common absolute-offset case
		}
		abs, carry := bits.Add64(it.BaseOffset, it.Extents[0].Offset, 0)
		if carry != 0 {
			abs = math.MaxUint64 // Python's sum is >= 2^64: past the end of any data
		}
		return ExifItemLocation{exifID, abs, it.Extents[0].Length}, true, nil
	}
	return loc, false, nil
}

// ExifOrientationField is find_exif_orientation_field's
// (abs_offset_of_2byte_value, byte_order, current_value).
type ExifOrientationField struct {
	AbsOffset int
	ByteOrder binary.ByteOrder // binary.LittleEndian ('II') or binary.BigEndian ('MM')
	Value     uint16
}

// FindExifOrientationField locates the embedded Exif item's Orientation
// tag, or returns ok=false if there's no embedded Exif item or no
// Orientation tag in it (← find_exif_orientation_field).
func FindExifOrientationField(data []byte, metaBodyStart, metaCEnd int) (field ExifOrientationField, ok bool, err error) {
	loc, ok, err := FindExifItemLocation(data, metaBodyStart, metaCEnd)
	if err != nil || !ok {
		return field, false, err
	}
	blob := pySlice64(data, loc.AbsStart, loc.Length)
	if len(blob) < 12 {
		return field, false, nil
	}
	tiffHeaderOffset := binary.BigEndian.Uint32(blob[0:4])
	tiffStartInBlob := 4 + int(tiffHeaderOffset)
	tiff := pySlice(blob, tiffStartInBlob, len(blob))
	if len(tiff) < 8 || (string(tiff[:2]) != "II" && string(tiff[:2]) != "MM") {
		return field, false, nil
	}
	var order binary.ByteOrder = binary.BigEndian
	if string(tiff[:2]) == "II" {
		order = binary.LittleEndian
	}
	ifd0Offset := int(order.Uint32(tiff[4:8]))
	if ifd0Offset+2 > len(tiff) {
		return field, false, nil
	}
	numEntries := int(order.Uint16(tiff[ifd0Offset : ifd0Offset+2]))
	pos := ifd0Offset + 2
	for range numEntries {
		entry := pySlice(tiff, pos, pos+12)
		if len(entry) < 12 {
			break
		}
		if tag := order.Uint16(entry[0:2]); tag == 0x0112 { // Orientation
			absValueOffset := int(loc.AbsStart) + tiffStartInBlob + pos + 8
			return ExifOrientationField{absValueOffset, order, order.Uint16(entry[8:10])}, true, nil
		}
		pos += 12
	}
	return field, false, nil
}

// ---------------------------------------------------------------------------
// Provenance ('uuid') box: read/build/update, and CRC32 helpers
// ---------------------------------------------------------------------------

// Crc32ExcludingRange is the CRC32 of data with the byte range
// [exclStart, exclEnd) removed - used so a provenance box's own CRC field
// isn't hashing itself (← crc32_excluding_range). Computed incrementally
// over the two halves; nothing is copied.
func Crc32ExcludingRange(data []byte, exclStart, exclEnd int) uint32 {
	crc := crc32.ChecksumIEEE(pySlice(data, 0, exclStart))
	return crc32.Update(crc, crc32.IEEETable, pySlice(data, exclEnd, len(data)))
}

// FindProvenanceBox returns the first top-level 'uuid' box carrying
// ProvenanceUUID (← find_provenance_box).
func FindProvenanceBox(data []byte) (hdr BoxHeader, ok bool, err error) {
	for h, err := range IterBoxes(data, 0, len(data)) {
		if err != nil {
			return BoxHeader{}, false, err
		}
		if h.Is("uuid") && h.ContentEnd-h.ContentStart >= 16 &&
			string(data[h.ContentStart:h.ContentStart+16]) == string(ProvenanceUUID[:]) {
			return h, true, nil
		}
	}
	return BoxHeader{}, false, nil
}

// ProvenanceEntries maps a provenance record's 4-byte entry tags (OIRT,
// OIPF, OMSZ, OILC, OEXO, PCRC, CCRC, TVER, and v1's OPRP) to their values.
type ProvenanceEntries map[string][]byte

// parseProvenanceEntries is the TLV walk shared by ParseProvenance and
// ParseProvenanceLenient. It returns the position it stopped at.
func parseProvenanceEntries(data []byte, hdr BoxHeader, strictVersion bool) (uint8, ProvenanceEntries, int, error) {
	pos := hdr.ContentStart + 16 // skip the 16-byte uuid
	version, err := pyIndex(data, pos)
	if err != nil {
		return 0, nil, 0, err
	}
	pos++
	if strictVersion && version > FormatVersion {
		return 0, nil, 0, provenanceErrorf("provenance box format version %d is newer than this "+
			"script understands (max %d) - update heic-lossless-rotate", version, FormatVersion)
	}
	entryCount, err := pyIndex(data, pos)
	if err != nil {
		return 0, nil, 0, err
	}
	pos++
	entries := ProvenanceEntries{}
	for range entryCount {
		tag := string(pySlice(data, pos, pos+4))
		pos += 4
		length, err := unpackU32(data, pos)
		if err != nil {
			return 0, nil, 0, err
		}
		pos += 4
		entries[tag] = pySliceCopy(data, pos, pos+int(length)) // later duplicates win, as in a dict
		pos += int(length)
	}
	return version, entries, pos, nil
}

// ParseProvenance parses the provenance box content into (version, entries)
// (← parse_provenance).
func ParseProvenance(data []byte, hdr BoxHeader) (uint8, ProvenanceEntries, error) {
	version, entries, pos, err := parseProvenanceEntries(data, hdr, true)
	if err != nil {
		return 0, nil, err
	}
	if pos != hdr.ContentEnd {
		return 0, nil, provenanceErrorf("provenance box parse mismatch: consumed %d, "+
			"expected %d (corrupt or truncated?)", pos-hdr.ContentStart, hdr.ContentEnd-hdr.ContentStart)
	}
	return version, entries, nil
}

// BuildProvenanceBox serializes a full 'uuid' provenance box, including its
// 8-byte header, with entries in sorted tag order for reproducibility
// (← build_provenance_box). Python asserts every tag is 4 bytes and that
// there are at most 255 entries; here those are errors.
func BuildProvenanceBox(version uint8, entries ProvenanceEntries) ([]byte, error) {
	if len(entries) > 255 {
		return nil, provenanceErrorf("provenance record has %d entries, more than 255", len(entries))
	}
	tags := slices.Sorted(maps.Keys(entries))
	body := append(append(make([]byte, 0, 64), ProvenanceUUID[:]...), version, byte(len(entries)))
	for _, tag := range tags {
		if len(tag) != 4 {
			return nil, provenanceErrorf("provenance entry tag %s is not 4 bytes", pyfmt.BytesRepr([]byte(tag)))
		}
		body = append(body, tag...)
		body = binary.BigEndian.AppendUint32(body, uint32(len(entries[tag])))
		body = append(body, entries[tag]...)
	}
	return makeBox("uuid", body), nil
}

// Structures is locate_structures' dict: the handful of top-level/meta-level
// structures rotate/reverse/info care about. IlocHdr and ProvenanceHdr are
// nil when absent.
type Structures struct {
	Top           []BoxHeader
	MetaHdr       BoxHeader
	MetaBodyStart int
	PitmHdr       BoxHeader
	PrimaryItemID uint32
	IprpHdr       BoxHeader
	IlocHdr       *BoxHeader
	ProvenanceHdr *BoxHeader
}

// LocateStructures finds meta, pitm (and the primary item id), iprp, iloc,
// and the provenance box (← locate_structures). Shared by rotate, reverse,
// and info so parsing logic lives in exactly one place.
func LocateStructures(data []byte) (*Structures, error) {
	top, err := listBoxes(data, 0, len(data))
	if err != nil {
		return nil, err
	}
	s := &Structures{Top: top}
	found := false
	for _, h := range top {
		if h.Is("meta") {
			s.MetaHdr, found = h, true
			break
		}
	}
	if !found {
		return nil, boxParseErrorf("no top-level 'meta' box found - not a valid HEIF file?")
	}
	s.MetaBodyStart = s.MetaHdr.ContentStart + 4
	metaEnd := s.MetaHdr.ContentEnd

	pitm, ok, err := FindChild(data, s.MetaBodyStart, metaEnd, "pitm")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, boxParseErrorf("no 'pitm' (primary item) box found inside 'meta'")
	}
	s.PitmHdr = pitm
	if s.PrimaryItemID, err = ParsePitm(data, pitm.ContentStart, pitm.ContentEnd); err != nil {
		return nil, err
	}

	iprp, ok, err := FindUniqueChild(data, s.MetaBodyStart, metaEnd, "iprp", "'meta'")
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, boxParseErrorf("no 'iprp' box found inside 'meta'")
	}
	s.IprpHdr = iprp

	iloc, ok, err := FindChild(data, s.MetaBodyStart, metaEnd, "iloc")
	if err != nil {
		return nil, err
	}
	if ok {
		s.IlocHdr = &iloc
	}
	prov, ok, err := FindProvenanceBox(data)
	if err != nil {
		return nil, err
	}
	if ok {
		s.ProvenanceHdr = &prov
	}
	return s, nil
}

// iprpParts finds iprp's ipco and ipma (each refused if duplicated) and
// returns ipco's children and ipma's parse; ok is false if either box is
// missing.
func iprpParts(data []byte, iprpCStart, iprpCEnd int) (ipcoHdr, ipmaHdr BoxHeader, ok bool, err error) {
	ipcoHdr, ipcoOK, err := FindUniqueChild(data, iprpCStart, iprpCEnd, "ipco", "'iprp'")
	if err != nil {
		return
	}
	ipmaHdr, ipmaOK, err := FindUniqueChild(data, iprpCStart, iprpCEnd, "ipma", "'iprp'")
	if err != nil {
		return
	}
	return ipcoHdr, ipmaHdr, ipcoOK && ipmaOK, nil
}

// primaryEntry returns the ipma entry for itemID, if any.
func primaryEntry(entries []IpmaEntry, itemID uint32) (*IpmaEntry, bool) {
	for i := range entries {
		if entries[i].ItemID == itemID {
			return &entries[i], true
		}
	}
	return nil, false
}

// findIrotChild returns the first ipco child that is an 'irot' associated
// with entry, in association order, considering only valid 1-based indexes.
func findIrotChild(entry *IpmaEntry, children []BoxHeader) (BoxHeader, bool) {
	for _, a := range entry.Assocs {
		if a.PropIndex >= 1 && a.PropIndex <= len(children) {
			if child := children[a.PropIndex-1]; child.Is("irot") {
				return child, true
			}
		}
	}
	return BoxHeader{}, false
}

// rotationFromIprpRange returns the primary item's associated 'irot' value
// (0-3), or 0 if it has none, given the byte range of an 'iprp' box's
// CONTENT (← _rotation_from_iprp_range). Works whether data is a whole file
// or a standalone snapshot buffer.
func rotationFromIprpRange(data []byte, iprpCStart, iprpCEnd int, primaryItemID uint32) (int, error) {
	ipcoHdr, ipmaHdr, ok, err := iprpParts(data, iprpCStart, iprpCEnd)
	if err != nil || !ok {
		return 0, err
	}
	children, err := ParseIpcoChildren(data, ipcoHdr.ContentStart, ipcoHdr.ContentEnd)
	if err != nil {
		return 0, err
	}
	_, _, entries, err := ParseIpma(data, ipmaHdr.ContentStart, ipmaHdr.ContentEnd)
	if err != nil {
		return 0, err
	}
	entry, ok := primaryEntry(entries, primaryItemID)
	if !ok {
		return 0, nil
	}
	if child, ok := findIrotChild(entry, children); ok {
		b, err := pyIndex(data, child.ContentStart)
		return int(b & 0x3), err
	}
	return 0, nil
}

// extractIrotState reports the primary item's pristine 'irot' state, given
// an 'iprp' content range (← _extract_irot_state):
//   - hadIrot=true: the primary item already has an associated 'irot';
//     irotByte is that property's raw content byte (unmasked - preserved
//     exactly, not just the 2-bit angle).
//   - hadIrot=false: no such property; ipmaFlags is the (pre-insertion)
//     ipma's flags, needed to restore ipma on reverse if inserting 'irot'
//     later set the 15-bit-index ("wide") flag bit.
//
// Used on a file's first-ever edit and by the v1->v2 provenance-format
// migration (against an old record's full OPRP snapshot).
func extractIrotState(data []byte, iprpCStart, iprpCEnd int, primaryItemID uint32) (hadIrot bool, irotByte byte, ipmaFlags uint32, err error) {
	ipcoHdr, ipmaHdr, ok, err := iprpParts(data, iprpCStart, iprpCEnd)
	if err != nil {
		return
	}
	if !ok {
		return false, 0, 0, boxParseErrorf("'iprp' is missing 'ipco' or 'ipma'")
	}
	children, err := ParseIpcoChildren(data, ipcoHdr.ContentStart, ipcoHdr.ContentEnd)
	if err != nil {
		return
	}
	_, flags, entries, err := ParseIpma(data, ipmaHdr.ContentStart, ipmaHdr.ContentEnd)
	if err != nil {
		return
	}
	if entry, ok := primaryEntry(entries, primaryItemID); ok {
		if child, ok := findIrotChild(entry, children); ok {
			b, err := pyIndex(data, child.ContentStart)
			return true, b, 0, err
		}
	}
	return false, 0, flags, nil
}

// GetCurrentRotation reads the CURRENTLY-applied rotation (0-3 quarter
// turns) from the primary item's live 'irot' property, or 0 if it has none
// (← get_current_rotation). This includes any rotation the file already had
// before heic-lossless-rotate ever touched it.
func GetCurrentRotation(data []byte) (int, error) {
	s, err := LocateStructures(data)
	if err != nil {
		return 0, err
	}
	return rotationFromIprpRange(data, s.IprpHdr.ContentStart, s.IprpHdr.ContentEnd, s.PrimaryItemID)
}

// GetRotationFromIprpSnapshot does the same lookup against a standalone
// saved 'iprp' box (8-byte header + content), as stored in a v1 provenance
// record's OPRP entry (← get_rotation_from_iprp_snapshot).
func GetRotationFromIprpSnapshot(iprpBox []byte, primaryItemID uint32) (int, error) {
	return rotationFromIprpRange(iprpBox, 8, len(iprpBox), primaryItemID)
}

// ParseProvenanceLenient is like ParseProvenance, but tolerates a record
// whose version is newer than this port knows about, returning whatever
// entries it can structurally read plus whether the record was well-formed
// (← parse_provenance_lenient). Used by GatherInfo, which only reports
// what it finds.
func ParseProvenanceLenient(data []byte, hdr BoxHeader) (version uint8, entries ProvenanceEntries, wellFormed bool, err error) {
	version, entries, pos, err := parseProvenanceEntries(data, hdr, false)
	if err != nil {
		return 0, nil, false, err
	}
	return version, entries, pos == hdr.ContentEnd, nil
}

// ---------------------------------------------------------------------------
// Main rotation logic (forward)
// ---------------------------------------------------------------------------

// logger writes Python's verbose print() lines; a nil writer is silent
// (verbose=False).
type logger struct{ w io.Writer }

func (l logger) printf(format string, args ...any) {
	if l.w != nil {
		fmt.Fprintf(l.w, format+"\n", args...)
	}
}

// RotateOptions are ApplyRotation's options. The zero value matches
// Python's defaults except for logging: Python's verbose=True prints to
// stdout, while a nil Log is silent.
type RotateOptions struct {
	NoExifSync bool      // don't sync the legacy embedded Exif Orientation tag (sync_exif=False)
	Log        io.Writer // receives the verbose diagnostic lines; nil = silent
}

// workingCopy mirrors bytearray(data), with spare capacity so splice can
// grow it in place.
func workingCopy(data []byte) []byte {
	out := make([]byte, len(data), len(data)+spliceSlack)
	copy(out, data)
	return out
}

// spliceOp is one (start, end, new_bytes) replacement.
type spliceOp struct {
	start, end int
	repl       []byte
}

// ApplyRotation rotates the image by deltaTurns quarter-turns
// COUNTER-CLOCKWISE ON TOP OF whatever rotation is already recorded (0 if
// none) - NOT to an absolute angle - and returns the edited file
// (← apply_rotation). "rotate 90" always means "turn it another quarter turn
// from however it looks now", exactly like rotating a physical photo.
// deltaTurns=0 is a genuine no-op for the visual orientation and can be
// used purely to add (or update) tracking metadata.
//
// The input is never modified.
func ApplyRotation(data []byte, deltaTurns int, opts RotateOptions) ([]byte, error) {
	log := logger{opts.Log}
	deltaTurns &= 0x3

	structInfo, err := LocateStructures(data)
	if err != nil {
		return nil, err
	}
	metaStart := structInfo.MetaHdr.Start
	metaBodyStart := structInfo.MetaBodyStart
	metaCEnd := structInfo.MetaHdr.ContentEnd
	primaryItemID := structInfo.PrimaryItemID
	iprpHdr := structInfo.IprpHdr
	ilocHdr := structInfo.IlocHdr

	ipcoHdr, ipmaHdr, ok, err := iprpParts(data, iprpHdr.ContentStart, iprpHdr.ContentEnd)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, boxParseErrorf("'iprp' is missing 'ipco' or 'ipma'")
	}
	ipcoChildren, err := ParseIpcoChildren(data, ipcoHdr.ContentStart, ipcoHdr.ContentEnd)
	if err != nil {
		return nil, err
	}
	ipmaVersion, ipmaFlags, ipmaEntries, err := ParseIpma(data, ipmaHdr.ContentStart, ipmaHdr.ContentEnd)
	if err != nil {
		return nil, err
	}
	primary, ok := primaryEntry(ipmaEntries, primaryItemID)
	if !ok {
		return nil, boxParseErrorf("primary item %d has no 'ipma' entry at all - "+
			"unexpected file structure, refusing to guess", primaryItemID)
	}

	if log.w != nil {
		propTypes := make([]string, len(ipcoChildren))
		for i, h := range ipcoChildren {
			propTypes[i] = pyfmt.DecodeASCIIReplace(h.Type[:])
		}
		assocs := make([]pyfmt.IntBool, len(primary.Assocs))
		for i, a := range primary.Assocs {
			assocs[i] = pyfmt.IntBool{Int: a.PropIndex, Bool: a.Essential}
		}
		log.printf("[info] primary item id = %d", primaryItemID)
		log.printf("[info] ipco properties (1-based) = %s", pyfmt.EnumerateList(propTypes))
		log.printf("[info] primary item associations = %s", pyfmt.IntBoolList(assocs))
	}

	// -- Have we edited this file before? If so, preserve the ORIGINAL
	// -- pristine snapshots captured on the very first edit - never
	// -- recapture, or reverse would only be able to undo the latest edit
	// -- instead of getting back to the true original.
	var existingProvenance ProvenanceEntries // nil <=> Python None
	if structInfo.ProvenanceHdr != nil {
		if _, existingProvenance, err = ParseProvenance(data, *structInfo.ProvenanceHdr); err != nil {
			return nil, err
		}
		log.printf("[info] existing provenance record found - this file has " +
			"been edited by heic-lossless-rotate before; preserving its " +
			"saved original snapshot")
	}

	// Pristine snapshots we may need to save (only on the FIRST ever edit).
	// The pristine 'irot' state itself is captured inside whichever of the
	// FAST/SLOW PATH branches below actually runs.
	pristineMetaSize := pySliceCopy(data, metaStart, metaStart+4)
	var pristineIlocContent []byte
	if ilocHdr != nil {
		pristineIlocContent = pySliceCopy(data, ilocHdr.ContentStart, ilocHdr.ContentEnd)
	}
	exifField, hasExif, err := FindExifOrientationField(data, metaBodyStart, metaCEnd)
	if err != nil {
		return nil, err
	}
	var pristineExifOrientation []byte
	if hasExif {
		pristineExifOrientation = make([]byte, 2) // struct.pack(byte_order + 'H', cur_value)
		exifField.ByteOrder.PutUint16(pristineExifOrientation, exifField.Value)
	}
	var pristineIrotByte byte
	var pristineIpmaFlags uint32

	// ---- FAST PATH: existing irot already associated with primary item ----
	// New value is the CURRENT value plus the requested delta, mod 4 - so
	// the person rotating never needs to know the existing orientation.
	// deltaTurns=0 therefore always yields newTurns == currentTurns.
	var out []byte
	fastPath := false
	var currentTurns, newTurns int
	if child, ok := findIrotChild(primary, ipcoChildren); ok {
		fastPath = true
		irotDataPos := child.ContentStart
		b, err := pyIndex(data, irotDataPos)
		if err != nil {
			return nil, err
		}
		if existingProvenance == nil {
			// First-ever edit, and it happens to be fast-path - the file
			// already had 'irot' before we ever touched it. Save the exact
			// original BYTE (not just the masked 2-bit angle).
			pristineIrotByte = b
		}
		currentTurns = int(b & 0x3)
		newTurns = (currentTurns + deltaTurns) % 4
		out = workingCopy(data)
		if newTurns == currentTurns {
			log.printf("[fast path] existing 'irot' = %d deg; delta %d deg leaves it "+
				"unchanged - box left as-is", currentTurns*90, deltaTurns*90)
		} else {
			out[irotDataPos] = byte(newTurns)
			log.printf("[fast path] existing 'irot' = %d deg, + %d deg delta -> "+
				"%d deg, patched in place at offset %d", currentTurns*90, deltaTurns*90, newTurns*90, irotDataPos)
		}
	}

	// ---- SLOW PATH: no irot property associated with primary item; insert one ----
	if !fastPath {
		if existingProvenance == nil {
			// First-ever edit, no 'irot' existed - the pre-insertion ipma
			// flags are the pristine value we may need to restore on
			// reverse (if inserting 'irot' sets the "wide" index flag bit).
			pristineIpmaFlags = ipmaFlags
		}
		currentTurns = 0 // absence of 'irot' implies 0 deg per spec
		newTurns = (currentTurns + deltaTurns) % 4
		if deltaTurns == 0 {
			log.printf("[angle 0] no 'irot' property exists yet - inserting " +
				"an explicit irot=0 (this matches the orientation " +
				"already implied by its absence, so the actual " +
				"displayed orientation does not change)")
		} else {
			log.printf("[slow path] no 'irot' property associated with the "+
				"primary item (implied 0 deg) - inserting one with "+
				"%d deg after applying the %d deg delta", newTurns*90, deltaTurns*90)
		}

		newIrotBox := makeBox("irot", []byte{byte(newTurns)})
		newIpcoContent := append(pySliceCopy(data, ipcoHdr.ContentStart, ipcoHdr.ContentEnd), newIrotBox...)
		newIpcoBox := makeBox("ipco", newIpcoContent)
		newPropertyIndex := len(ipcoChildren) + 1

		newFlags := ipmaFlags
		if newPropertyIndex > 0x7f { // wide_needed
			newFlags |= 1
		}

		newEntries := make([]IpmaEntry, len(ipmaEntries))
		for i, e := range ipmaEntries {
			assocs := append([]IpmaAssoc(nil), e.Assocs...)
			if e.ItemID == primaryItemID {
				assocs = append(assocs, IpmaAssoc{PropIndex: newPropertyIndex, Essential: true})
			}
			newEntries[i] = IpmaEntry{ItemID: e.ItemID, Assocs: assocs}
		}
		newIpmaBox, err := BuildIpma(ipmaVersion, newFlags, newEntries)
		if err != nil {
			return nil, err
		}

		var newIprpContent []byte
		if ipcoHdr.Start < ipmaHdr.Start { // first_is_ipco
			newIprpContent = append(newIpcoBox, newIpmaBox...)
		} else {
			newIprpContent = append(newIpmaBox, newIpcoBox...)
		}
		newIprpBox := makeBox("iprp", newIprpContent)

		delta := len(newIprpBox) - (iprpHdr.ContentEnd - iprpHdr.Start)

		var ops []spliceOp
		newIlocContent, anyChanged, err := PatchIlocOffsetsAtThreshold(data, ilocHdr, iprpHdr.Start, delta)
		if err != nil {
			return nil, err
		}
		var ilocPatchOp *spliceOp
		if anyChanged {
			ilocPatchOp = &spliceOp{ilocHdr.ContentStart, ilocHdr.ContentEnd, newIlocContent}
			log.printf("[slow path] patched absolute offsets in 'iloc' " +
				"(metadata precedes media data in this file's layout)")
		}

		// We patch meta's size by overwriting its leading 4-byte field in
		// place - this only works if 'meta' uses a standard 32-bit box
		// header. The standard also permits a 64-bit "largesize" header or
		// a size-extends-to-EOF header for ANY box; supporting that would
		// mean growing/shrinking an 8-byte field instead of overwriting a
		// 4-byte one in place, which none of the insertion math here
		// accounts for. So we refuse, before any bytes have been touched.
		metaSizeField, err := unpackU32(data, metaStart)
		if err != nil {
			return nil, err
		}
		if metaSizeField == 0 || metaSizeField == 1 {
			return nil, boxParseErrorf("meta box uses 64-bit/extends-to-EOF size field; " +
				"this script only handles standard 32-bit headers")
		}
		newMetaSize := int64(metaSizeField) + int64(delta)
		if newMetaSize < 0 || newMetaSize > math.MaxUint32 { // Python: struct.error (COMPAT D2)
			return nil, boxParseErrorf("new meta box size %d doesn't fit in its 32-bit size field", newMetaSize)
		}
		ops = append(ops,
			spliceOp{metaStart, metaStart + 4, binary.BigEndian.AppendUint32(nil, uint32(newMetaSize))},
			spliceOp{iprpHdr.Start, iprpHdr.ContentEnd, newIprpBox})
		if ilocPatchOp != nil {
			ops = append(ops, *ilocPatchOp)
		}
		sort.SliceStable(ops, func(i, j int) bool { return ops[i].start > ops[j].start })

		out = workingCopy(data)
		for _, op := range ops {
			out = splice(out, op.start, op.end, op.repl)
		}

		log.printf("[slow path] inserted new 'irot' property (index "+
			"%d), grew container by %d bytes", newPropertyIndex, delta)
	}

	// ---- Sync the legacy embedded Exif Orientation tag, if present ----
	// Only when the effective orientation actually changed - if the delta
	// left it at the same value it already had (delta 0, most commonly),
	// nothing that represents displayed orientation should be touched.
	if !opts.NoExifSync && newTurns != currentTurns {
		// re-locate fresh against the (possibly resized) output buffer
		infoNow, err := LocateStructures(out)
		if err != nil {
			return nil, err
		}
		fieldNow, ok, err := FindExifOrientationField(out, infoNow.MetaBodyStart, infoNow.MetaHdr.ContentEnd)
		if err != nil {
			return nil, err
		}
		if ok {
			newExifOrientation := IrotToExifOrientation[newTurns]
			if fieldNow.AbsOffset+2 > len(out) { // Python: struct.error (COMPAT D2)
				return nil, errUnpack(2, fieldNow.AbsOffset, len(out))
			}
			fieldNow.ByteOrder.PutUint16(out[fieldNow.AbsOffset:], newExifOrientation)
			log.printf("[exif sync] embedded legacy Exif Orientation tag "+
				"updated to %d (offset %d)", newExifOrientation, fieldNow.AbsOffset)
		} else {
			log.printf("[exif sync] no embedded legacy Exif Orientation tag found " +
				"- nothing to sync")
		}
	}

	// ---- Update / create the provenance box ----
	entries := ProvenanceEntries{}
	if existingProvenance != nil {
		// keep the true-original snapshots untouched
		for _, tag := range []string{"OILC", "OMSZ", "OEXO", "PCRC"} {
			if v, ok := existingProvenance[tag]; ok {
				entries[tag] = v
			}
		}
		if oirt, ok := existingProvenance["OIRT"]; ok {
			// Already v2 format - carry the leaner entries forward as-is.
			entries["OIRT"] = oirt
			if oipf, ok := existingProvenance["OIPF"]; ok {
				entries["OIPF"] = oipf
			}
		} else if oldOprp, ok := existingProvenance["OPRP"]; ok {
			// v1 record: it saved the WHOLE pristine 'iprp' box. Migrate it
			// forward to v2's leaner OIRT(+OIPF) representation now, by
			// asking the same "did 'irot' already exist?" question of that
			// OLD snapshot instead of the live file. The full OPRP snapshot
			// is then dropped.
			hadIrot, irotByte, origIpmaFlags, err := extractIrotState(oldOprp, 8, len(oldOprp), primaryItemID)
			if err != nil {
				return nil, err
			}
			if hadIrot {
				entries["OIRT"] = []byte{irotByte}
			} else {
				entries["OIRT"] = []byte{}
				entries["OIPF"] = flagsBytes(origIpmaFlags)
			}
			log.printf("[provenance] migrating v1 record's full 'iprp' " +
				"snapshot to the leaner v2 format (irot-only)")
		} else {
			return nil, provenanceErrorf("existing provenance record has neither 'OIRT' nor " +
				"'OPRP' - don't know how to determine its original " +
				"irot state")
		}
	} else {
		if fastPath {
			entries["OIRT"] = []byte{pristineIrotByte}
		} else {
			entries["OIRT"] = []byte{}
			entries["OIPF"] = flagsBytes(pristineIpmaFlags)
		}
		entries["OMSZ"] = pristineMetaSize
		if pristineIlocContent != nil {
			entries["OILC"] = pristineIlocContent
		}
		if pristineExifOrientation != nil {
			entries["OEXO"] = pristineExifOrientation
		}
		// PCRC: crc32 of the truly original file (no provenance box existed yet)
		entries["PCRC"] = binary.BigEndian.AppendUint32(nil, crc32.ChecksumIEEE(data))
	}

	// TVER: the version performing THIS update - a "who last updated this
	// file" marker, freshly written on every edit rather than carried
	// forward. ReverseRotation never writes it.
	tver, err := versionTuple()
	if err != nil {
		return nil, err
	}
	entries["TVER"] = tver[:]

	// Insert, resize, or same-size-update the provenance box - one
	// operation: splice a possibly-different-size blob in, patching iloc
	// for the size delta if there is one.
	//   - oldProvSize == 0: first-time insertion.
	//   - oldProvSize == newProvSize: every edit after the first (only
	//     CCRC's and TVER's fixed-width VALUES change).
	//   - otherwise: a one-time migration of a record written by an older
	//     version (e.g. one predating TVER, or a v1 record).
	infoNow, err := LocateStructures(out)
	if err != nil {
		return nil, err
	}
	var oldProvStart, oldProvEnd int
	if infoNow.ProvenanceHdr != nil {
		oldProvStart, oldProvEnd = infoNow.ProvenanceHdr.Start, infoNow.ProvenanceHdr.ContentEnd
	} else {
		oldProvStart, oldProvEnd = infoNow.MetaHdr.ContentEnd, infoNow.MetaHdr.ContentEnd
	}
	oldProvSize := oldProvEnd - oldProvStart

	// CCRC is a fixed 4-byte field regardless of its value, so a
	// placeholder-CCRC box gives the real box's size before the real CCRC
	// (which depends on the fully-patched buffer) is known.
	entries["CCRC"] = []byte{0, 0, 0, 0}
	placeholderProvBox, err := BuildProvenanceBox(FormatVersion, entries)
	if err != nil {
		return nil, err
	}
	newProvSize := len(placeholderProvBox)
	provDelta := newProvSize - oldProvSize

	if provDelta != 0 {
		// This box is a TOP-LEVEL SIBLING of 'meta' (right after it), so
		// changing its size never touches meta's own size field - but if
		// 'meta' sits BEFORE 'mdat', every iloc absolute offset pointing
		// into mdat needs to shift by the size delta. This is a SEPARATE
		// delta from the iprp-insertion patch earlier (if any).
		ilocHdrNow := infoNow.IlocHdr
		newIlocContent, anyChanged, err := PatchIlocOffsetsAtThreshold(out, ilocHdrNow, oldProvEnd, provDelta)
		if err != nil {
			return nil, err
		}
		if anyChanged {
			out = splice(out, ilocHdrNow.ContentStart, ilocHdrNow.ContentEnd, newIlocContent)
			log.printf("[provenance] patched absolute offsets in 'iloc' " +
				"for the provenance box's own insertion/resize too " +
				"(metadata precedes media data in this file's " +
				"layout)")
		}
		if oldProvSize != 0 {
			log.printf("[provenance] existing record predates an entry tag "+
				"this tool version writes - migrating it in place "+
				"(%d -> %d bytes)", oldProvSize, newProvSize)
		}
	}

	// Now compute the REAL CCRC over the file as it will be immediately
	// BEFORE the provenance box is (re)written - with iloc already patched
	// and the provenance box itself excluded.
	ccrc := Crc32ExcludingRange(out, oldProvStart, oldProvEnd)
	entries["CCRC"] = binary.BigEndian.AppendUint32(nil, ccrc)
	newProvBox, err := BuildProvenanceBox(FormatVersion, entries)
	if err != nil {
		return nil, err
	}
	if len(newProvBox) != newProvSize {
		return nil, boxParseErrorf("internal error: provenance box size changed between " +
			"placeholder and real CCRC - CCRC should always be a " +
			"fixed 4 bytes")
	}
	out = splice(out, oldProvStart, oldProvEnd, newProvBox)

	action := "created"
	if len(existingProvenance) > 0 { // Python: `if existing_provenance` (dict truthiness)
		action = "updated"
	}
	log.printf("[provenance] %s provenance record (format v%d, %d bytes)", action, FormatVersion, len(newProvBox))

	return out, nil
}

// flagsBytes mirrors struct.pack('>I', flags)[1:]: the low 3 bytes.
func flagsBytes(flags uint32) []byte {
	return []byte{byte(flags >> 16), byte(flags >> 8), byte(flags)}
}

// ---------------------------------------------------------------------------
// Reverse logic
// ---------------------------------------------------------------------------

// ReverseOptions are ReverseRotation's and VerifyReversible's options. As
// with RotateOptions, a nil Log is silent.
type ReverseOptions struct {
	// IgnoreTamper proceeds even if the file appears to have been modified
	// by something else since this tool's last edit (CCRC mismatch). The
	// final reconstruction CRC check is never skipped.
	IgnoreTamper bool
	Log          io.Writer // receives the verbose diagnostic lines; nil = silent
}

// requireEntry mirrors entries[tag]: a missing entry is a Python KeyError,
// here a *ProvenanceError (COMPAT D2).
func requireEntry(entries ProvenanceEntries, tag string) ([]byte, error) {
	v, ok := entries[tag]
	if !ok {
		return nil, provenanceErrorf("provenance record has no '%s' entry", tag)
	}
	return v, nil
}

// entryU32 mirrors struct.unpack('>I', entries[tag])[0]: a missing or
// wrong-length entry is a Python KeyError/struct.error (COMPAT D2).
func entryU32(entries ProvenanceEntries, tag string) (uint32, error) {
	v, err := requireEntry(entries, tag)
	if err != nil {
		return 0, err
	}
	if len(v) != 4 {
		return 0, provenanceErrorf("provenance entry '%s' is %d bytes, expected 4", tag, len(v))
	}
	return binary.BigEndian.Uint32(v), nil
}

// restoreMetaProvenanceSpan restores 'meta' to its pristine pre-rotation
// state (iprp/ipma, meta's own size field, iloc content) and removes the
// provenance box, given span = data[meta_start:prov_end] - a standalone
// buffer whose two top-level boxes are the file's 'meta' immediately
// followed by the provenance box (← _restore_meta_provenance_span).
// Returns the restored span.
//
// This is ReverseRotation's restoration logic, factored out so it has
// exactly one implementation shared by ReverseRotation and
// VerifyReversible. Box parsing is header-driven, not position-driven, so
// operating on this standalone slice needs no special-casing.
func restoreMetaProvenanceSpan(span []byte, entries ProvenanceEntries, log logger) ([]byte, error) {
	info, err := LocateStructures(span)
	if err != nil {
		return nil, err
	}
	iprpHdr := info.IprpHdr
	iprpStart, iprpCEnd := iprpHdr.Start, iprpHdr.ContentEnd
	primaryItemID := info.PrimaryItemID

	// Restore iprp. Two formats, branched on which entry is present:
	//   v1 (OPRP): the pristine 'iprp' box was saved in full - a single splice.
	//   v2 (OIRT): only the 'irot' property itself was saved. FAST PATH
	//     (OIRT length 1) just needs that one byte put back. SLOW PATH
	//     (OIRT length 0) removes the inserted property - always the LAST
	//     ipco child, with its association always the LAST entry in the
	//     primary item's ipma list - so no index bookkeeping was needed.
	if pristineIprp, ok := entries["OPRP"]; ok {
		span = splice(span, iprpStart, iprpCEnd, pristineIprp)
		log.printf("[reverse] restored pristine 'iprp' from full v1 "+
			"snapshot (%d bytes)", len(pristineIprp))
	} else if oirt, ok := entries["OIRT"]; ok {
		ipcoHdr, ipmaHdr, ok, err := iprpParts(span, iprpHdr.ContentStart, iprpHdr.ContentEnd)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, provenanceErrorf("'iprp' is missing 'ipco' or 'ipma' - " +
				"cannot reverse")
		}
		ipcoChildren, err := ParseIpcoChildren(span, ipcoHdr.ContentStart, ipcoHdr.ContentEnd)
		if err != nil {
			return nil, err
		}
		ipmaVersion, _, ipmaEntries, err := ParseIpma(span, ipmaHdr.ContentStart, ipmaHdr.ContentEnd)
		if err != nil {
			return nil, err
		}
		primary, hasPrimary := primaryEntry(ipmaEntries, primaryItemID)

		switch len(oirt) {
		case 1:
			// FAST PATH: 'irot' already existed - put its original byte back.
			if !hasPrimary {
				return nil, provenanceErrorf("OIRT indicates the primary item should have an " +
					"'irot' property, but it now has no 'ipma' entry " +
					"at all - cannot reverse")
			}
			child, ok := findIrotChild(primary, ipcoChildren)
			if !ok {
				return nil, provenanceErrorf("OIRT indicates an 'irot' property should exist on " +
					"the primary item, but none was found - cannot " +
					"reverse")
			}
			irotPos := child.ContentStart
			if irotPos >= len(span) { // Python: IndexError (COMPAT D2)
				return nil, errIndex(irotPos, len(span))
			}
			span[irotPos] = oirt[0]
			log.printf("[reverse] restored original 'irot' byte "+
				"(%d) at offset %d", oirt[0], irotPos)
		case 0:
			// SLOW PATH: no 'irot' existed originally - strip out exactly
			// the one heic-lossless-rotate inserted.
			if len(ipcoChildren) == 0 || !ipcoChildren[len(ipcoChildren)-1].Is("irot") {
				return nil, provenanceErrorf("OIRT indicates heic-lossless-rotate appended an 'irot' " +
					"property as the last 'ipco' child, but that's not " +
					"what's there now - cannot safely reverse (file may " +
					"have been modified by something else since)")
			}
			lastChild := ipcoChildren[len(ipcoChildren)-1]
			newPropertyIndex := len(ipcoChildren) // 1-based index of lastChild
			if !hasPrimary || len(primary.Assocs) == 0 ||
				primary.Assocs[len(primary.Assocs)-1].PropIndex != newPropertyIndex {
				return nil, provenanceErrorf("OIRT indicates the primary item's last 'ipma' " +
					"association should point at the inserted 'irot' " +
					"property, but it doesn't - cannot safely reverse")
			}

			newEntries := make([]IpmaEntry, len(ipmaEntries))
			for i, e := range ipmaEntries {
				assocs := append([]IpmaAssoc(nil), e.Assocs...)
				if e.ItemID == primaryItemID {
					if len(assocs) == 0 { // Python: pop from empty list (COMPAT D2)
						return nil, boxParseErrorf("ipma has an entry for primary item %d with no associations to remove", e.ItemID)
					}
					assocs = assocs[:len(assocs)-1] // drop the association we appended
				}
				newEntries[i] = IpmaEntry{ItemID: e.ItemID, Assocs: assocs}
			}
			oipf, err := requireEntry(entries, "OIPF")
			if err != nil {
				return nil, err
			}
			if len(oipf) != 3 { // Python: struct.error (COMPAT D2)
				return nil, provenanceErrorf("provenance entry 'OIPF' is %d bytes, expected 3", len(oipf))
			}
			originalFlags := uint32(oipf[0])<<16 | uint32(oipf[1])<<8 | uint32(oipf[2])
			newIpmaBox, err := BuildIpma(ipmaVersion, originalFlags, newEntries)
			if err != nil {
				return nil, err
			}

			newIpcoBox := makeBox("ipco", pySliceCopy(span, ipcoHdr.ContentStart, lastChild.Start))
			var newIprpContent []byte
			if ipcoHdr.Start < ipmaHdr.Start { // first_is_ipco
				newIprpContent = append(newIpcoBox, newIpmaBox...)
			} else {
				newIprpContent = append(newIpmaBox, newIpcoBox...)
			}
			newIprpBox := makeBox("iprp", newIprpContent)
			span = splice(span, iprpStart, iprpCEnd, newIprpBox)
			log.printf("[reverse] removed inserted 'irot' property and "+
				"its 'ipma' association, restored original 'ipma' "+
				"flags (%d byte 'iprp' now, was %d)", len(newIprpBox), iprpCEnd-iprpStart)
		default:
			return nil, provenanceErrorf("corrupt provenance record: OIRT has unexpected length "+
				"%d (expected 0 or 1)", len(oirt))
		}
	} else {
		return nil, provenanceErrorf("provenance record has neither 'OPRP' nor 'OIRT' - don't " +
			"know how to restore 'iprp'")
	}

	// Restore meta's size field. This overwrites a plain 4-byte field in
	// place; any file reaching this point was edited by ApplyRotation,
	// which refuses 64-bit/extends-to-EOF 'meta' headers.
	omsz, err := requireEntry(entries, "OMSZ")
	if err != nil {
		return nil, err
	}
	metaStartLocal := info.MetaHdr.Start
	span = splice(span, metaStartLocal, metaStartLocal+4, omsz)

	// Remove the provenance box itself NOW, before restoring iloc - which
	// needs 'meta''s own byte range in its FINAL (post-shrink) state.
	info3, err := LocateStructures(span)
	if err != nil {
		return nil, err
	}
	if info3.ProvenanceHdr == nil { // Python: unpacking None (TypeError, COMPAT D2)
		return nil, provenanceErrorf("provenance box no longer found after restoring 'meta' - cannot reverse")
	}
	span = splice(span, info3.ProvenanceHdr.Start, info3.ProvenanceHdr.ContentEnd, nil)

	// Restore iloc content, if we have a saved snapshot.
	if oilc, ok := entries["OILC"]; ok {
		info4, err := LocateStructures(span)
		if err != nil {
			return nil, err
		}
		if info4.IlocHdr != nil {
			span = splice(span, info4.IlocHdr.ContentStart, info4.IlocHdr.ContentEnd, oilc)
		}
	}
	return span, nil
}

// exifPatch is the (absolute offset, pristine bytes) Exif restoration.
type exifPatch struct {
	offset int
	bytes  []byte
}

// reversal is everything prepareReversal works out before the reconstructed
// bytes are assembled.
type reversal struct {
	entries   ProvenanceEntries
	metaStart int
	provEnd   int
	span      []byte
	exif      *exifPatch
}

// prepareReversal does all of the setup shared by ReverseRotation and
// VerifyReversible: locate the provenance record, check its format version,
// run the tamper check, restore the meta+provenance span, and determine the
// embedded-Exif patch (← _prepare_reversal). Having this in exactly one
// place means the two can't drift apart on what counts as a
// valid/tampered/malformed record.
func prepareReversal(data []byte, ignoreTamper bool, log logger) (*reversal, error) {
	info, err := LocateStructures(data)
	if err != nil {
		return nil, err
	}
	provHdr := info.ProvenanceHdr
	if provHdr == nil {
		return nil, provenanceErrorf("no heic-lossless-rotate provenance record found in this file - " +
			"either it was never edited by this tool, or the record was " +
			"stripped by something else. Cannot reverse.")
	}

	version, entries, err := ParseProvenance(data, *provHdr)
	if err != nil {
		return nil, err
	}
	if !slices.Contains(SupportedReverseFormatVersions, int(version)) {
		supported := make([]string, len(SupportedReverseFormatVersions))
		for i, v := range SupportedReverseFormatVersions {
			supported[i] = strconv.Itoa(v)
		}
		return nil, provenanceErrorf("provenance record is format version %d; this script "+
			"only knows how to reverse version(s) %s. Use a "+
			"matching version of heic-lossless-rotate.", version, strings.Join(supported, ", "))
	}

	provStart, provEnd := provHdr.Start, provHdr.ContentEnd
	currentCcrc := Crc32ExcludingRange(data, provStart, provEnd)
	storedCcrc, err := entryU32(entries, "CCRC")
	if err != nil {
		return nil, err
	}
	if currentCcrc != storedCcrc {
		msg := fmt.Sprintf("file appears to have been modified by something else since "+
			"the last heic-lossless-rotate edit (CRC32 mismatch: file is "+
			"%s, provenance expects %s). "+
			"Reversing now could silently clobber those other changes.",
			pyfmt.Hex08(currentCcrc), pyfmt.Hex08(storedCcrc))
		if !ignoreTamper {
			return nil, &ProvenanceError{Msg: msg + " Re-run with --ignore-tamper-check to override."}
		}
		log.printf("[warning] %s Proceeding anyway (--ignore-tamper-check).", msg)
	}

	// 'mdat' is excluded from this span in both supported layouts, since
	// the provenance box is always inserted as 'meta''s immediate next
	// sibling; it is never touched by the restoration itself, only
	// (optionally) by the Exif-Orientation patch below.
	metaStart := info.MetaHdr.Start
	span, err := restoreMetaProvenanceSpan(pySliceCopy(data, metaStart, provEnd), entries, log)
	if err != nil {
		return nil, err
	}

	// The Exif patch, if any, lands inside 'mdat' - read its CURRENT
	// position and value directly off data. Reversing never moves mdat's
	// own bytes, only removes/shrinks the meta+provenance span.
	r := &reversal{entries: entries, metaStart: metaStart, provEnd: provEnd, span: span}
	if oexo, ok := entries["OEXO"]; ok {
		field, ok, err := FindExifOrientationField(data, info.MetaBodyStart, info.MetaHdr.ContentEnd)
		if err != nil {
			return nil, err
		}
		if ok {
			if metaStart <= field.AbsOffset && field.AbsOffset < provEnd {
				// Should be structurally impossible for any file this tool
				// itself produced - fail loudly rather than guess.
				return nil, provenanceErrorf("internal error: embedded Exif Orientation field " +
					"unexpectedly falls inside the meta/provenance span " +
					"- refusing to guess at the reconstructed bytes")
			}
			r.exif = &exifPatch{field.AbsOffset, oexo}
			log.printf("[reverse] restoring pristine legacy Exif Orientation tag")
		} else {
			log.printf("[reverse] warning: had a saved Exif Orientation snapshot " +
				"but couldn't relocate the field to restore it - the " +
				"final CRC check below will catch any resulting mismatch")
		}
	}
	return r, nil
}

// iterReconstructedChunks yields the reconstructed file's bytes, in file
// order, as a handful of chunks: sub-slices of data for everything passed
// through unchanged, real bytes only for the restored meta+provenance span
// and (if applicable) the Exif patch (← _iter_reconstructed_chunks). This is
// the SINGLE piece of code that decides what bytes the reconstructed file
// consists of - ReverseRotation concatenates the chunks, VerifyReversible
// only feeds them into a running CRC32.
func iterReconstructedChunks(data []byte, r *reversal) iter.Seq[[]byte] {
	return func(yield func([]byte) bool) {
		if r.exif != nil && r.exif.offset < r.metaStart {
			if !yield(pySlice(data, 0, r.exif.offset)) || !yield(r.exif.bytes) ||
				!yield(pySlice(data, r.exif.offset+2, r.metaStart)) {
				return
			}
		} else if !yield(pySlice(data, 0, r.metaStart)) {
			return
		}

		if !yield(r.span) {
			return
		}

		if r.exif != nil && r.exif.offset >= r.provEnd {
			if yield(pySlice(data, r.provEnd, r.exif.offset)) && yield(r.exif.bytes) {
				yield(pySlice(data, r.exif.offset+2, len(data)))
			}
		} else {
			yield(pySlice(data, r.provEnd, len(data)))
		}
	}
}

// reconstructedChunks is the indirection both ReverseRotation and
// VerifyReversible call through, so tests can confirm they consume the
// identical chunk sequence (upstream monkeypatches the same function).
var reconstructedChunks = iterReconstructedChunks

// checkReconstructedCrc compares the reconstructed file's CRC32 with the
// pristine-original CRC32 recorded on the very first edit.
func checkReconstructedCrc(r *reversal, finalCrc uint32, log logger) error {
	expected, err := entryU32(r.entries, "PCRC")
	if err != nil {
		return err
	}
	if finalCrc != expected {
		return provenanceErrorf("reconstruction mismatch: rebuilt file CRC32 %s "+
			"does not match recorded original CRC32 %s. "+
			"Do not trust this output - this indicates a bug or an "+
			"inconsistent provenance record.", pyfmt.Hex08(finalCrc), pyfmt.Hex08(expected))
	}
	log.printf("[ok] reconstructed file CRC32 matches the recorded original "+
		"(%s) - byte-identical to the true original", pyfmt.Hex08(finalCrc))
	return nil
}

// ReverseRotation undoes heic-lossless-rotate's own edit(s), reconstructing
// the exact original file, verified by CRC32 against the PCRC recorded on
// the file's first-ever edit (← reverse_rotation).
//
// Every edit ApplyRotation makes falls either inside the contiguous span
// [meta_start, prov_end) - 'meta' itself plus the provenance box that
// immediately follows it - or, for at most one 2-byte patch, inside 'mdat'
// (the embedded legacy Exif Orientation tag). Nothing outside those two
// regions is ever touched. The input is never modified.
func ReverseRotation(data []byte, opts ReverseOptions) ([]byte, error) {
	log := logger{opts.Log}
	r, err := prepareReversal(data, opts.IgnoreTamper, log)
	if err != nil {
		return nil, err
	}
	size := 0
	for chunk := range reconstructedChunks(data, r) {
		size += len(chunk)
	}
	out := make([]byte, 0, size)
	var crc uint32
	for chunk := range reconstructedChunks(data, r) {
		out = append(out, chunk...)
		crc = crc32.Update(crc, crc32.IEEETable, chunk)
	}
	if err := checkReconstructedCrc(r, crc, log); err != nil {
		return nil, err
	}
	return out, nil
}

// VerifyReversible is equivalent, in pass/fail and the error returned, to
// calling ReverseRotation and discarding the result - but it computes the
// final CRC32 comparison without ever materializing a second full-file
// buffer (← verify_reversible). Used by rotate's post-edit self-check.
//
// It isn't a second implementation that happens to agree with
// ReverseRotation: both call prepareReversal and iterReconstructedChunks;
// the only difference is what happens to each chunk.
func VerifyReversible(data []byte, opts ReverseOptions) error {
	log := logger{opts.Log}
	r, err := prepareReversal(data, opts.IgnoreTamper, log)
	if err != nil {
		return err
	}
	var crc uint32
	for chunk := range reconstructedChunks(data, r) {
		crc = crc32.Update(crc, crc32.IEEETable, chunk)
	}
	return checkReconstructedCrc(r, crc, log)
}

// ---------------------------------------------------------------------------
// Info: cheap, read-only summary - no reconstruction, no heavy verification.
// ---------------------------------------------------------------------------

// Info is gather_info's dict. Pointer fields are nil where Python leaves the
// key out.
type Info struct {
	// CurrentQuarterTurns is the live 'irot' value (0-3) on the primary item
	// right now - including any rotation the file had before this tool
	// touched it.
	CurrentQuarterTurns int
	HasProvenance       bool

	// The rest is only set when HasProvenance is true.

	// OriginalQuarterTurns is the rotation when heic-lossless-rotate FIRST
	// touched the file; nil if the record has neither OPRP nor OIRT.
	OriginalQuarterTurns *int
	// DeltaQuarterTurns is how much heic-lossless-rotate has added on top
	// of the original, cumulatively across every edit - the answer to "what
	// did this tool do to it". Same nil rule as OriginalQuarterTurns.
	DeltaQuarterTurns *int
	Version           uint8 // provenance format version
	VersionSupported  bool  // false if newer than FormatVersion or malformed
	// ToolVersion is the (major, minor, patch) that most recently UPDATED
	// the file; nil on records written before TVER existed.
	ToolVersion *[3]uint8
	// PCRC is the CRC32 of the TRUE ORIGINAL file - independently
	// verifiable by hashing your own original file. (CCRC is deliberately
	// not surfaced: it is computed with the provenance box excluded, so it
	// isn't checkable with a plain whole-file CRC32 tool.)
	PCRC                             *uint32
	HasIlocSnapshot, HasExifSnapshot bool
}

// GatherInfo is a read-only summary of heic-lossless-rotate's provenance
// state and the file's live rotation (← gather_info). It does none of the
// heavy work rotate/reverse do - just parses what's already in the file.
func GatherInfo(data []byte) (*Info, error) {
	current, err := GetCurrentRotation(data)
	if err != nil {
		return nil, err
	}
	result := &Info{CurrentQuarterTurns: current}

	info, err := LocateStructures(data)
	if err != nil {
		return nil, err
	}
	if info.ProvenanceHdr == nil {
		return result, nil
	}

	version, entries, wellFormed, err := ParseProvenanceLenient(data, *info.ProvenanceHdr)
	if err != nil {
		return nil, err
	}
	result.HasProvenance = true
	result.Version = version
	result.VersionSupported = version <= FormatVersion && wellFormed
	_, result.HasIlocSnapshot = entries["OILC"]
	_, result.HasExifSnapshot = entries["OEXO"]

	setOriginal := func(original int) {
		delta := ((current-original)%4 + 4) % 4 // Python's % is never negative
		result.OriginalQuarterTurns, result.DeltaQuarterTurns = &original, &delta
	}
	if oprp, ok := entries["OPRP"]; ok {
		original, err := GetRotationFromIprpSnapshot(oprp, info.PrimaryItemID)
		if err != nil {
			return nil, err
		}
		setOriginal(original)
	} else if oirt, ok := entries["OIRT"]; ok {
		// length 1: 'irot' already existed originally - its masked value is
		// the original rotation. length 0: none existed - 0 deg per spec.
		original := 0
		if len(oirt) == 1 {
			original = int(oirt[0] & 0x3)
		}
		setOriginal(original)
	}

	if _, ok := entries["PCRC"]; ok {
		pcrc, err := entryU32(entries, "PCRC")
		if err != nil {
			return nil, err
		}
		result.PCRC = &pcrc
	}
	if tver, ok := entries["TVER"]; ok && len(tver) == 3 {
		result.ToolVersion = &[3]uint8{tver[0], tver[1], tver[2]}
	}
	return result, nil
}

// FormatInfo renders GatherInfo's result as text and returns the info exit
// code (← format_info). text is "" when quiet (Python returns None).
//
// Exit code 0 is reserved EXCLUSIVELY for "no provenance record" (this tool
// never touched the file). Once a record exists it is always non-zero:
// 1/2/3 for a cumulative rotation ADDED BY THIS TOOL of 90/180/270 deg, or
// 4 if the tool's net contribution is 0 deg (e.g. rotate 0 was used just to
// add tracking, or opposing rotations cancelled out).
func FormatInfo(result *Info, quiet bool) (text string, exitCode int) {
	currentDegrees := result.CurrentQuarterTurns * 90

	if !result.HasProvenance {
		text = "Not rotated with heic-lossless-rotate (no provenance record found)."
		if result.CurrentQuarterTurns != 0 {
			text += fmt.Sprintf("\nNote: the file currently has its own 'irot' value "+
				"of %d deg from elsewhere (not something this tool did).", currentDegrees)
		}
		if quiet {
			return "", 0
		}
		return text, 0
	}

	// What heic-lossless-rotate itself did is the delta between the
	// rotation it found on first touching the file and the rotation live
	// now - NOT the absolute current value, which may also include rotation
	// the file already had.
	toolVersion := ""
	if v := result.ToolVersion; v != nil {
		toolVersion = fmt.Sprintf(" v%d.%d.%d", v[0], v[1], v[2])
	}
	first := fmt.Sprintf("Rotated with heic-lossless-rotate%s, provenance format v%d", toolVersion, result.Version)
	if !result.VersionSupported {
		first += fmt.Sprintf(" (NEWER than this script's v%d - shown info "+
			"may be incomplete)", FormatVersion)
	}
	lines := []string{first + "."}
	if result.DeltaQuarterTurns != nil {
		lines = append(lines,
			fmt.Sprintf("  heic-lossless-rotate has added: %d deg", *result.DeltaQuarterTurns*90),
			fmt.Sprintf("  file's rotation: %d deg originally -> %d deg now", *result.OriginalQuarterTurns*90, currentDegrees))
	} else {
		lines = append(lines, fmt.Sprintf("  currently at: %d deg (original "+
			"rotation snapshot unavailable - can't compute what "+
			"this tool added specifically)", currentDegrees))
	}
	if result.PCRC != nil {
		lines = append(lines, "  original file CRC32:      "+pyfmt.Hex08(*result.PCRC))
	}
	var extras []string
	if result.HasIlocSnapshot {
		extras = append(extras, "iloc snapshot")
	}
	if result.HasExifSnapshot {
		extras = append(extras, "exif-orientation snapshot")
	}
	if len(extras) > 0 {
		lines = append(lines, "  also recorded: "+strings.Join(extras, ", "))
	}

	exitCode = 4
	if d := result.DeltaQuarterTurns; d != nil && *d != 0 {
		exitCode = *d
	}
	if quiet {
		return "", exitCode
	}
	return strings.Join(lines, "\n"), exitCode
}

// ---------------------------------------------------------------------------
// Self-verification: re-walk the box tree of the OUTPUT file and assert every
// container's children sizes sum exactly to its declared content size.
// ---------------------------------------------------------------------------

// VerifyStructure checks that every container this tool edits (top level,
// meta, iprp, ipco) holds children that exactly fill it (← verify_structure).
func VerifyStructure(data []byte) error {
	checkContainer := func(cStart, cEnd int, label string) error {
		pos := cStart
		for pos < cEnd {
			hdr, ok, err := ReadBoxHeader(data, pos)
			if err != nil {
				return err
			}
			if !ok {
				return boxParseErrorf("%s: truncated box at %d", label, pos)
			}
			if hdr.ContentEnd > cEnd {
				return boxParseErrorf("%s: child '%s' at %d overflows "+
					"container end (%d > %d)", label, pyfmt.BytesRepr(hdr.Type[:]), hdr.Start, hdr.ContentEnd, cEnd)
			}
			if hdr.ContentEnd <= pos { // Python loops forever here (COMPAT D6)
				return errZeroSizeBox(hdr)
			}
			pos = hdr.ContentEnd
		}
		if pos != cEnd {
			return boxParseErrorf("%s: children end at %d, expected %d", label, pos, cEnd)
		}
		return nil
	}

	top, err := listBoxes(data, 0, len(data))
	if err != nil {
		return err
	}
	if err := checkContainer(0, len(data), "top-level"); err != nil {
		return err
	}
	for _, hdr := range top {
		if !hdr.Is("meta") {
			continue
		}
		metaBody := hdr.ContentStart + 4
		if err := checkContainer(metaBody, hdr.ContentEnd, "meta"); err != nil {
			return err
		}
		for child, err := range IterBoxes(data, metaBody, hdr.ContentEnd) {
			if err != nil {
				return err
			}
			if !child.Is("iprp") {
				continue
			}
			if err := checkContainer(child.ContentStart, child.ContentEnd, "iprp"); err != nil {
				return err
			}
			for gc, err := range IterBoxes(data, child.ContentStart, child.ContentEnd) {
				if err != nil {
					return err
				}
				if gc.Is("ipco") {
					if err := checkContainer(gc.ContentStart, gc.ContentEnd, "ipco"); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

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
