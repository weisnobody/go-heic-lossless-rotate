package core

import "fmt"

// Go-only (COMPAT E2/D8): orientation as a whole - 'irot' and 'imir'
// together - for the mirror feature and for rotating files that have an
// 'imir'. Nothing here exists in core.py.
//
// HEIF decoders apply an item's transformative properties in its 'ipma'
// association order (ISO/IEC 23008-12; libheif image_item.cc). 'irot' turns
// the image counter-clockwise; 'imir' flips it. The eight results are the
// eight Exif Orientation values, so an orientation here is numbered the
// same way, and composing two of them uses libheif's table.

// orient is an orientation numbered like the Exif Orientation tag (1-8).
type orient uint8

// orientConcat[a-1][b-1] is the orientation "a, then b" - libheif's
// orientation_concat table (libheif/api/libheif/heif_encoding.cc).
var orientConcat = [8][8]orient{
	{1, 2, 3, 4, 5, 6, 7, 8},
	{2, 1, 4, 3, 8, 7, 6, 5},
	{3, 4, 1, 2, 7, 8, 5, 6},
	{4, 3, 2, 1, 6, 5, 8, 7},
	{5, 6, 7, 8, 1, 2, 3, 4},
	{6, 5, 8, 7, 4, 3, 2, 1},
	{7, 8, 5, 6, 3, 4, 1, 2},
	{8, 7, 6, 5, 2, 1, 4, 3},
}

const (
	orientNormal     orient = 1
	orientFlipLR     orient = 2 // left-right flip: 'imir' with bit 0 set
	orientFlipTB     orient = 4 // top-bottom flip: 'imir' with bit 0 clear
	orientTranspose  orient = 5
	orientTransverse orient = 7
)

// valid reports whether o is one of the eight orientations.
func (o orient) valid() bool { return o >= 1 && o <= 8 }

// then is the orientation of applying o, then p.
func (o orient) then(p orient) orient { return orientConcat[o-1][p-1] }

// inverse is the orientation that undoes o.
func (o orient) inverse() orient {
	for p := orient(1); p <= 8; p++ {
		if o.then(p) == orientNormal {
			return p
		}
	}
	panic("unreachable: orientConcat is a group")
}

// mirrored reports whether o includes a flip.
func (o orient) mirrored() bool {
	return o == orientFlipLR || o == orientFlipTB || o == orientTranspose || o == orientTransverse
}

// parts splits o into quarter turns counter-clockwise followed, if flip is
// set, by a left-right flip.
func (o orient) parts() (turns int, flip bool) {
	for turns = range 4 {
		if r := irotOrient(turns); r == o {
			return turns, false
		} else if r.then(orientFlipLR) == o {
			return turns, true
		}
	}
	panic("unreachable: every orientation is a rotation, optionally then a flip")
}

// String describes o for the info report: "90 deg", "horizontal flip",
// "90 deg + horizontal flip". Turns are counter-clockwise, as 'irot' and
// `rotate` count them.
func (o orient) String() string {
	if !o.valid() {
		return fmt.Sprintf("invalid orientation %d", uint8(o))
	}
	switch o {
	case orientFlipLR:
		return "horizontal flip"
	case orientFlipTB:
		return "vertical flip"
	}
	turns, flip := o.parts()
	if flip {
		return fmt.Sprintf("%d deg + horizontal flip", turns*90)
	}
	return fmt.Sprintf("%d deg", turns*90)
}

// irotOrient is the orientation of an 'irot' of turns quarter turns.
func irotOrient(turns int) orient { return orient(IrotToExifOrientation[turns&0x3]) }

// imirOrient is the orientation of an 'imir' whose content byte is b. Only
// bit 0 counts: set mirrors about the vertical axis (a left-right flip),
// clear about the horizontal axis (top-bottom) - libheif's reading
// (box.cc Box_imir::parse, pixelimage.cc mirror_inplace).
func imirOrient(b byte) orient {
	if b&1 == 1 {
		return orientFlipLR
	}
	return orientFlipTB
}

// MirrorAxis is a flip direction, valued as the 'imir' content byte that
// produces it.
type MirrorAxis uint8

const (
	// MirrorVertical flips the image upside down (top-bottom), mirroring it
	// about a horizontal axis.
	MirrorVertical MirrorAxis = 0
	// MirrorHorizontal flips the image left-right, mirroring it about a
	// vertical axis.
	MirrorHorizontal MirrorAxis = 1
)

// String is the axis as the CLI names it.
func (a MirrorAxis) String() string {
	if a == MirrorHorizontal {
		return "horizontal"
	}
	return "vertical"
}

// findImirChild returns the first ipco child that is an 'imir' associated
// with entry, in association order, considering only valid 1-based indexes
// (findIrotChild's rule).
func findImirChild(entry *IpmaEntry, children []BoxHeader) (BoxHeader, bool) {
	for _, a := range entry.Assocs {
		if a.PropIndex >= 1 && a.PropIndex <= len(children) {
			if child := children[a.PropIndex-1]; child.Is("imir") {
				return child, true
			}
		}
	}
	return BoxHeader{}, false
}

// composeOrient is the orientation decoders display for an item with these
// associations: every associated 'irot' and 'imir', in association order.
// The 'irot' whose content starts at irotPos reads as irotTurns instead of
// its stored value (irotPos -1 overrides nothing), so callers can try a
// value before writing it.
func composeOrient(data []byte, assocs []IpmaAssoc, children []BoxHeader, irotPos, irotTurns int) (orient, error) {
	o := orientNormal
	for _, a := range assocs {
		if a.PropIndex < 1 || a.PropIndex > len(children) {
			continue
		}
		child := children[a.PropIndex-1]
		switch {
		case child.Is("irot"):
			turns := irotTurns
			if child.ContentStart != irotPos {
				b, err := pyIndex(data, child.ContentStart)
				if err != nil {
					return 0, err
				}
				turns = int(b)
			}
			o = o.then(irotOrient(turns))
		case child.Is("imir"):
			b, err := pyIndex(data, child.ContentStart)
			if err != nil {
				return 0, err
			}
			o = o.then(imirOrient(b))
		}
	}
	return o, nil
}

// solveIrot finds the value for the 'irot' at irotPos that makes the
// associations display as target. ok is false if no value does (target has
// a flip and the others don't, or the other way around).
func solveIrot(data []byte, assocs []IpmaAssoc, children []BoxHeader, irotPos int, target orient) (turns int, ok bool, err error) {
	for turns = range 4 {
		o, err := composeOrient(data, assocs, children, irotPos, turns)
		if err != nil {
			return 0, false, err
		}
		if o == target {
			return turns, true, nil
		}
	}
	return 0, false, nil
}

// orientFromIprpRange is the primary item's displayed orientation, given an
// 'iprp' content range (rotationFromIprpRange, with 'imir' included).
func orientFromIprpRange(data []byte, iprpCStart, iprpCEnd int, primaryItemID uint32) (orient, error) {
	ipcoHdr, ipmaHdr, ok, err := iprpParts(data, iprpCStart, iprpCEnd)
	if err != nil || !ok {
		return orientNormal, err
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
		return orientNormal, nil
	}
	return composeOrient(data, entry.Assocs, children, -1, 0)
}

// primaryHasImir reports whether the primary item has an associated 'imir',
// reading only box types - nothing GetCurrentRotation hasn't already parsed.
func primaryHasImir(data []byte, s *Structures) (bool, error) {
	ipcoHdr, ipmaHdr, ok, err := iprpParts(data, s.IprpHdr.ContentStart, s.IprpHdr.ContentEnd)
	if err != nil || !ok {
		return false, err
	}
	children, err := ParseIpcoChildren(data, ipcoHdr.ContentStart, ipcoHdr.ContentEnd)
	if err != nil {
		return false, err
	}
	_, _, entries, err := ParseIpma(data, ipmaHdr.ContentStart, ipmaHdr.ContentEnd)
	if err != nil {
		return false, err
	}
	entry, ok := primaryEntry(entries, s.PrimaryItemID)
	if !ok {
		return false, nil
	}
	_, ok = findImirChild(entry, children)
	return ok, nil
}

// gatherOrientation fills GatherInfo's orientation fields when an 'imir' is
// involved. entries is nil without a provenance record.
//
// The original orientation is the live one with this tool's edits undone:
// with OIMR, the 'imir' is this tool's (it never touches a file's own), so
// it is dropped; with OIRT, the 'irot' is either set back to its saved byte
// or, if the tool inserted it, dropped. A v1 record (OPRP) has the whole
// original 'iprp' to read instead.
func gatherOrientation(data []byte, s *Structures, entries ProvenanceEntries, result *Info) error {
	hasImir, err := primaryHasImir(data, s)
	if err != nil {
		return err
	}
	_, toolImir := entries["OIMR"]
	if !hasImir && !toolImir {
		return nil
	}
	current, err := orientFromIprpRange(data, s.IprpHdr.ContentStart, s.IprpHdr.ContentEnd, s.PrimaryItemID)
	if err != nil {
		return err
	}
	cur := int(current)
	result.CurrentOrientation = &cur

	var original orient
	if oprp, ok := entries["OPRP"]; ok {
		if original, err = orientFromIprpRange(oprp, 8, len(oprp), s.PrimaryItemID); err != nil {
			return err
		}
	} else if oirt, ok := entries["OIRT"]; ok {
		ipcoHdr, ipmaHdr, _, err := iprpParts(data, s.IprpHdr.ContentStart, s.IprpHdr.ContentEnd)
		if err != nil {
			return err
		}
		children, err := ParseIpcoChildren(data, ipcoHdr.ContentStart, ipcoHdr.ContentEnd)
		if err != nil {
			return err
		}
		_, _, ipmaEntries, err := ParseIpma(data, ipmaHdr.ContentStart, ipmaHdr.ContentEnd)
		if err != nil {
			return err
		}
		original = orientNormal
		if entry, ok := primaryEntry(ipmaEntries, s.PrimaryItemID); ok {
			var assocs []IpmaAssoc
			for _, a := range entry.Assocs {
				if a.PropIndex >= 1 && a.PropIndex <= len(children) {
					child := children[a.PropIndex-1]
					if (toolImir && child.Is("imir")) || (len(oirt) != 1 && child.Is("irot")) {
						continue
					}
				}
				assocs = append(assocs, a)
			}
			irotPos, irotTurns := -1, 0
			if irot, ok := findIrotChild(entry, children); ok && len(oirt) == 1 {
				irotPos, irotTurns = irot.ContentStart, int(oirt[0])
			}
			if original, err = composeOrient(data, assocs, children, irotPos, irotTurns); err != nil {
				return err
			}
		}
	} else {
		return nil
	}
	delta := int(original.inverse().then(current))
	orig := int(original)
	result.OriginalOrientation, result.DeltaOrientation = &orig, &delta
	return nil
}

// orientationLines are FormatInfo's lines describing the orientation when
// an 'imir' is involved, in place of its quarter-turn lines.
func orientationLines(result *Info) []string {
	current := orient(*result.CurrentOrientation)
	if result.DeltaOrientation == nil {
		return []string{fmt.Sprintf("  currently at: %s (original orientation snapshot "+
			"unavailable - can't compute what this tool added specifically)", current)}
	}
	return []string{
		fmt.Sprintf("  heic-lossless-rotate has added: %s", orient(*result.DeltaOrientation)),
		fmt.Sprintf("  file's orientation: %s originally -> %s now", orient(*result.OriginalOrientation), current),
	}
}

// orientExitCode is info's exit code for what this tool added: 5 if it
// includes a flip, else 1/2/3 for its quarter turns, or 4 for none.
func orientExitCode(delta orient) int {
	if !delta.valid() {
		return 4
	}
	if delta.mirrored() {
		return 5
	}
	if turns, _ := delta.parts(); turns != 0 {
		return turns
	}
	return 4
}
