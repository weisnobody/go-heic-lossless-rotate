package core

// Go-only (COMPAT E2): flipping the image by editing its 'imir' property,
// the mirror-image counterpart of rotating it through 'irot'. Nothing here
// exists in core.py; see orient.go for the orientation math.
//
// A flip has no "off" value: every 'imir' flips the image. So un-flipping
// removes the property, which this tool only does for an 'imir' it
// inserted itself - always the last 'ipco' child, with the primary item's
// last association pointing at it, like the 'irot' the SLOW PATH inserts.
// To keep it last, the first flip of a file with no 'irot' inserts an
// explicit irot=0 just before it; later rotations then patch that 'irot' in
// place and never insert anything after the 'imir'. The tool never changes
// an 'imir' byte in place, so reverse only ever has to remove one.

import (
	"encoding/binary"

	"github.com/weisnobody/go-heic-lossless-rotate/internal/pybytes"
)

// ApplyMirror flips the image as it is currently displayed - left-right for
// MirrorHorizontal, upside down for MirrorVertical - and returns the edited
// file, with the same Exif sync and provenance tracking as ApplyRotation.
// Flipping twice the same way returns the original orientation. A file
// whose own 'imir' (one this tool didn't insert) would have to be removed is
// refused.
//
// The input is never modified.
func ApplyMirror(data []byte, axis MirrorAxis, opts RotateOptions) ([]byte, error) {
	return ApplyMirrorEnv(memEnv{}, data, axis, opts)
}

// ApplyMirrorEnv is ApplyMirror's body over a FileEnv, for package stream.
func ApplyMirrorEnv(env FileEnv, data []byte, axis MirrorAxis, opts RotateOptions) ([]byte, error) {
	if axis != MirrorHorizontal && axis != MirrorVertical {
		return nil, boxParseErrorf("invalid mirror axis %d", axis)
	}
	return applyEdit(env, data, editOp{mirror: true, axis: axis}, opts)
}

// iprpState is the parsed 'iprp' applyEdit works from.
type iprpState struct {
	ipcoHdr, ipmaHdr BoxHeader
	children         []BoxHeader
	ipmaVersion      uint8
	ipmaFlags        uint32
	entries          []IpmaEntry
	primary          *IpmaEntry
}

// mirrorResult is what mirrorEdit did.
type mirrorResult struct {
	out          []byte
	hadIrot      bool // the primary item had an 'irot' before this edit
	irotByte     byte // that 'irot' property's content byte
	insertedImir bool
	newOrient    orient
}

// mirrorEdit makes the flip: it inserts an 'imir' if the primary item has
// none, or removes the one this tool inserted (adjusting 'irot' to keep the
// displayed rotation) if it has one.
func mirrorEdit(env FileEnv, data []byte, s *Structures, p iprpState, axis MirrorAxis,
	current orient, existingProvenance ProvenanceEntries, log logger) (mirrorResult, error) {
	var res mirrorResult
	res.newOrient = current.then(imirOrient(byte(axis)))
	log.printf("[mirror] flipping %s: orientation %s -> %s", axis, current, res.newOrient)

	irotChild, hasIrot := findIrotChild(p.primary, p.children)
	if hasIrot {
		b, err := pyIndex(data, irotChild.ContentStart)
		if err != nil {
			return res, err
		}
		res.hadIrot, res.irotByte = true, b
	}

	imirChild, hasImir := findImirChild(p.primary, p.children)
	if !hasImir {
		var boxes [][]byte
		if !hasIrot {
			log.printf("[mirror] no 'irot' property associated with the primary item either - " +
				"inserting an explicit irot=0 ahead of the 'imir', so the flip stays the last transform")
			boxes = append(boxes, makeBox("irot", []byte{0}))
		}
		boxes = append(boxes, makeBox("imir", []byte{byte(axis)}))
		newIprpBox, index, err := iprpWithAppended(data, p.ipcoHdr, p.ipmaHdr, p.children,
			p.ipmaVersion, p.ipmaFlags, p.entries, s.PrimaryItemID, boxes...)
		if err != nil {
			return res, err
		}
		out, delta, err := spliceIprp(env, data, s, newIprpBox, "mirror", log)
		if err != nil {
			return res, err
		}
		if hasIrot {
			log.printf("[mirror] inserted new 'imir' property (index %d), grew container by %d bytes", index, delta)
		} else {
			log.printf("[mirror] inserted new 'irot' and 'imir' properties (indexes %d and %d), "+
				"grew container by %d bytes", index, index+1, delta)
		}
		res.out, res.insertedImir = out, true
		return res, nil
	}

	// Un-flipping: the 'imir' has to go.
	if _, ok := existingProvenance["OIMR"]; !ok {
		return res, boxParseErrorf("the primary item already has its own 'imir' (mirroring) " +
			"property; un-mirroring it would mean removing a property heic-lossless-rotate " +
			"didn't add, which isn't supported")
	}
	n := len(p.children)
	assocs := p.primary.Assocs
	if p.children[n-1].Start != imirChild.Start || assocs[len(assocs)-1].PropIndex != n {
		return res, provenanceErrorf("OIMR indicates heic-lossless-rotate inserted the primary item's " +
			"'imir' as the last 'ipco' child and association, but that's not what's there now - " +
			"cannot safely un-mirror (file may have been modified by something else since)")
	}
	if !hasIrot {
		return res, provenanceErrorf("the primary item has no 'irot' property to keep its " +
			"rotation in once the 'imir' heic-lossless-rotate inserted is removed - cannot " +
			"safely un-mirror (file may have been modified by something else since)")
	}
	irotPos := irotChild.ContentStart
	newTurns, ok, err := solveIrot(data, assocs[:len(assocs)-1], p.children, irotPos, res.newOrient)
	if err != nil {
		return res, err
	}
	if !ok {
		return res, boxParseErrorf("the primary item has another 'imir' property besides the one " +
			"heic-lossless-rotate inserted - cannot un-mirror it")
	}

	buf := workingCopy(data)
	if newTurns != int(res.irotByte&0x3) {
		buf[irotPos] = byte(newTurns)
		log.printf("[mirror] existing 'irot' = %d deg -> %d deg, patched in place at offset %d",
			int(res.irotByte&0x3)*90, newTurns*90, env.Base()+irotPos)
	}
	newIprpBox, err := iprpWithoutLast(buf, p.ipcoHdr, p.ipmaHdr, p.ipmaVersion, p.entries,
		s.PrimaryItemID, p.children[n-1], func() (uint32, error) { return p.ipmaFlags, nil })
	if err != nil {
		return res, err
	}
	out, delta, err := spliceIprp(env, buf, s, newIprpBox, "mirror", log)
	if err != nil {
		return res, err
	}
	log.printf("[mirror] removed the 'imir' property heic-lossless-rotate inserted earlier "+
		"(index %d), shrank container by %d bytes", n, -delta)
	res.out = out
	return res, nil
}

// restoreImir is reverse's 'imir' step (in restoreMetaProvenanceSpan, before
// the 'irot' one): remove the 'imir' this tool inserted, if it is still
// there. If 'irot' wasn't inserted too (OIRT is its original byte), this is
// the last insertion to undo, so the pristine 'ipma' flags (OIPF) go back
// now - an un-flip leaves a wide index flag the insertion set; otherwise
// the 'irot' step restores them once its own property is gone.
//
// Unlike that step, this one re-locates boxes afterwards, so it keeps the
// 'meta' size field in step with the splice (OMSZ overwrites it later).
func restoreImir(span []byte, info *Structures, entries ProvenanceEntries, log logger) ([]byte, error) {
	iprpHdr := info.IprpHdr
	ipcoHdr, ipmaHdr, ok, err := iprpParts(span, iprpHdr.ContentStart, iprpHdr.ContentEnd)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, provenanceErrorf("'iprp' is missing 'ipco' or 'ipma' - cannot reverse")
	}
	children, err := ParseIpcoChildren(span, ipcoHdr.ContentStart, ipcoHdr.ContentEnd)
	if err != nil {
		return nil, err
	}
	ipmaVersion, ipmaFlags, ipmaEntries, err := ParseIpma(span, ipmaHdr.ContentStart, ipmaHdr.ContentEnd)
	if err != nil {
		return nil, err
	}
	flags := ipmaFlags
	if oirt, ok := entries["OIRT"]; ok && len(oirt) == 1 {
		if flags, err = entryOipf(entries); err != nil {
			return nil, err
		}
	}
	primary, hasPrimary := primaryEntry(ipmaEntries, info.PrimaryItemID)
	n := len(children)
	inserted := hasPrimary && n > 0 && children[n-1].Is("imir") &&
		len(primary.Assocs) > 0 && primary.Assocs[len(primary.Assocs)-1].PropIndex == n

	var newIprpBox []byte
	switch {
	case inserted:
		newIprpBox, err = iprpWithoutLast(span, ipcoHdr, ipmaHdr, ipmaVersion, ipmaEntries,
			info.PrimaryItemID, children[n-1], func() (uint32, error) { return flags, nil })
		if err != nil {
			return nil, err
		}
		log.printf("[reverse] removed inserted 'imir' property and its 'ipma' association")
	case flags != ipmaFlags:
		newIpmaBox, err := BuildIpma(ipmaVersion, flags, ipmaEntries)
		if err != nil {
			return nil, err
		}
		newIprpBox = joinIprp(ipcoHdr, ipmaHdr, pybytes.SliceCopy(span, ipcoHdr.Start, ipcoHdr.ContentEnd), newIpmaBox)
	default:
		return span, nil // un-flipped already; nothing of the 'imir' left
	}
	delta := len(newIprpBox) - (iprpHdr.ContentEnd - iprpHdr.Start)
	metaStart := info.MetaHdr.Start
	metaSize, err := unpackU32(span, metaStart)
	if err != nil {
		return nil, err
	}
	if metaSize <= 1 || int64(metaSize)+int64(delta) < 0 {
		return nil, provenanceErrorf("'meta' size field can't be adjusted for removing the 'imir' - cannot reverse")
	}
	binary.BigEndian.PutUint32(span[metaStart:], uint32(int64(metaSize)+int64(delta)))
	return pybytes.Splice(span, iprpHdr.Start, iprpHdr.ContentEnd, newIprpBox), nil
}
