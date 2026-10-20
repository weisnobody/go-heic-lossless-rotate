// Package stream is the streaming engine (Go-only): rotate, reverse, verify,
// and info over an io.ReaderAt without holding the image data in memory.
//
// Every edit falls inside the meta+provenance span [meta_start, prov_end)
// plus at most one 2-byte Exif patch (upstream's reverse_rotation docstring
// makes this argument; it holds for apply too). So the engine loads only
// that span and runs the SAME orchestration as the []byte engine on it -
// core.ApplyRotationEnv / core.PrepareReversalEnv - through a core.FileEnv that
// reads everything else (the Exif blob, CRCs) on demand. Results are
// Views: the input with a few ranges replaced, never copied wholesale.
//
// Exactness: the core's parsers, like upstream's, read past box ends.
// Within the loaded span that reads the same bytes as the whole file
// would; past its end it wouldn't. The engine therefore runs only on files
// that pass prechecks under which every read stays inside the span or
// ends in an error, and it treats ANY error or failed precheck as "not
// mine": it falls back to reading the whole input and running the []byte
// engine, which gives the authoritative result. Well-formed files stream;
// malformed or unusual ones cost what they always did (~2x the file).
package stream

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"io"
	"math"
	"sort"

	"github.com/pjg/go-heic-lossless-rotate/internal/pybytes"
	"github.com/pjg/go-heic-lossless-rotate/pkg/core"
)

// exifBlobCap bounds how much of an Exif item the streaming engine reads; a
// larger item (real ones are a few KB) falls back to the []byte engine.
const exifBlobCap = 1 << 20

// errFallback means "use the []byte engine for this input".
var errFallback = errors.New("streaming engine: falling back to the []byte engine")

// streamFallbackHook, if set, is told about every fallback (tests count them).
var streamFallbackHook func(op string, cause error)

// ---------------------------------------------------------------------------
// View
// ---------------------------------------------------------------------------

// segment is one run of a View: lit if non-nil, else n bytes of the input
// starting at off.
type segment struct {
	off, n int64
	lit    []byte
}

func (s segment) len() int64 {
	if s.lit != nil {
		return int64(len(s.lit))
	}
	return s.n
}

// View is a rotate/reverse result: the input's bytes with a few ranges
// replaced, read on demand rather than copied. It reads from the input
// ReaderAt it was made from, which must stay unchanged and readable while
// the View is in use.
type View struct {
	r      io.ReaderAt
	segs   []segment
	starts []int64 // starts[i] is segs[i]'s offset in the View
	size   int64
}

func newView(r io.ReaderAt, segs []segment) *View {
	v := &View{r: r}
	for _, s := range segs {
		if s.len() == 0 {
			continue
		}
		v.segs = append(v.segs, s)
		v.starts = append(v.starts, v.size)
		v.size += s.len()
	}
	return v
}

// ViewOfBytes wraps an in-memory result (the []byte engine's).
func ViewOfBytes(b []byte) *View { return newView(nil, []segment{{lit: b}}) }

// Size is the View's length in bytes.
func (v *View) Size() int64 { return v.size }

// ReadAt implements io.ReaderAt.
func (v *View) ReadAt(p []byte, off int64) (int, error) {
	if off < 0 {
		return 0, errors.New("stream.View.ReadAt: negative offset")
	}
	n := 0
	i := sort.Search(len(v.starts), func(i int) bool { return v.starts[i] > off }) - 1
	for ; i >= 0 && i < len(v.segs) && n < len(p); i++ {
		s, segOff := v.segs[i], off+int64(n)-v.starts[i]
		want := min(int64(len(p)-n), s.len()-segOff)
		if s.lit != nil {
			n += copy(p[n:], s.lit[segOff:segOff+want])
			continue
		}
		m, err := v.r.ReadAt(p[n:n+int(want)], s.off+segOff)
		n += m
		if err != nil && !(errors.Is(err, io.EOF) && int64(m) == want) {
			return n, err
		}
	}
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

// WriteTo writes the View's bytes to w (io.WriterTo).
func (v *View) WriteTo(w io.Writer) (int64, error) {
	var total int64
	var buf []byte
	for _, s := range v.segs {
		if s.lit != nil {
			n, err := w.Write(s.lit)
			total += int64(n)
			if err != nil {
				return total, err
			}
			continue
		}
		if buf == nil {
			buf = make([]byte, 64<<10)
		}
		n, err := io.CopyBuffer(w, io.NewSectionReader(v.r, s.off, s.n), buf)
		total += n
		if err != nil {
			return total, err
		}
		if n != s.n {
			return total, io.ErrUnexpectedEOF
		}
	}
	return total, nil
}

// Bytes reads the whole View into memory.
func (v *View) Bytes() ([]byte, error) {
	if len(v.segs) == 1 && v.segs[0].lit != nil {
		return v.segs[0].lit, nil
	}
	out := make([]byte, v.size)
	if _, err := v.ReadAt(out, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return out, nil
}

// crcRange updates crc with r's bytes [off, off+n).
func crcRange(crc uint32, r io.ReaderAt, off, n int64, buf []byte) (uint32, error) {
	for n > 0 {
		chunk := buf[:min(n, int64(len(buf)))]
		m, err := r.ReadAt(chunk, off)
		crc = crc32.Update(crc, crc32.IEEETable, chunk[:m])
		off, n = off+int64(m), n-int64(m)
		if err != nil && n > 0 {
			if errors.Is(err, io.EOF) {
				err = io.ErrUnexpectedEOF
			}
			return crc, err
		}
	}
	return crc, nil
}

// crcExcluding is Crc32ExcludingRange over v: crc32 of v[:a] then v[b:],
// with Python slice semantics (clamped; a > b overlaps).
func (v *View) crcExcluding(a, b int64) (uint32, error) {
	a, b = max(0, min(a, v.size)), max(0, min(b, v.size))
	buf := make([]byte, 64<<10)
	crc, err := crcRange(0, v, 0, a, buf)
	if err != nil {
		return 0, err
	}
	return crcRange(crc, v, b, v.size-b, buf)
}

// ---------------------------------------------------------------------------
// Prechecks and the span
// ---------------------------------------------------------------------------

// streamInput is an input that passed the prechecks: span holds the file's
// bytes [metaStart, spanEnd) - the first and only 'meta' box plus, if
// present, the provenance box immediately after it.
type streamInput struct {
	r                  io.ReaderAt
	size               int
	metaStart, spanEnd int
	span               []byte
}

// loadStreamInput reads the span and checks that streaming will give
// exactly the []byte engine's result: every check failure is errFallback.
//
// Under these checks every read the core makes either stays inside the span
// (where it sees the same bytes as the whole file) or ends in an error:
//   - the top level tiles the file exactly, with one 'meta' box (32-bit
//     header) and the first provenance box, if any, right after it;
//   - the span's own structure passes VerifyStructure;
//   - the boxes the core reads at fixed offsets (pitm, iinf/infe, irot, the
//     provenance box) are large enough for those reads;
//   - no absolute-offset iloc extent points into the span.
//
// The remaining reads past a box's end are the length-driven parsers
// (ipma, iloc, provenance entries), which then fail their own consumed-
// length checks - an error, so a fallback.
func loadStreamInput(r io.ReaderAt, size int64) (*streamInput, error) {
	if size < 0 || size > math.MaxInt-pybytes.SpliceSlack {
		return nil, errFallback
	}
	in := &streamInput{r: r, size: int(size)}

	var hdr [16]byte
	meta, prov := -1, -1
	metaEnd, provEnd, metas := 0, 0, 0
	for pos := 0; pos < in.size; {
		n := min(16, in.size-pos)
		if n < 8 {
			return nil, errFallback // trailing bytes that aren't a box
		}
		if _, err := r.ReadAt(hdr[:n], int64(pos)); err != nil {
			return nil, err
		}
		size64, headerLen := uint64(binary.BigEndian.Uint32(hdr[:4])), 8
		switch size64 {
		case 1:
			if n < 16 {
				return nil, errFallback
			}
			size64, headerLen = binary.BigEndian.Uint64(hdr[8:16]), 16
		case 0:
			size64 = uint64(in.size - pos)
		}
		if size64 < uint64(headerLen) || size64 > uint64(in.size-pos) {
			return nil, errFallback
		}
		end := pos + int(size64)
		switch string(hdr[4:8]) {
		case "meta":
			if metas++; metas == 1 {
				if binary.BigEndian.Uint32(hdr[:4]) <= 1 {
					return nil, errFallback // 64-bit or to-EOF meta header
				}
				meta, metaEnd = pos, end
			}
		case "uuid":
			if prov < 0 && end-(pos+headerLen) >= 16 {
				var id [16]byte
				if _, err := r.ReadAt(id[:], int64(pos+headerLen)); err != nil {
					return nil, err
				}
				if id == core.ProvenanceUUID {
					prov, provEnd = pos, end
				}
			}
		}
		pos = end
	}
	if meta < 0 || metas > 1 || (prov >= 0 && prov != metaEnd) {
		return nil, errFallback
	}
	in.metaStart, in.spanEnd = meta, metaEnd
	if prov >= 0 {
		in.spanEnd = provEnd
	}
	in.span = make([]byte, in.spanEnd-in.metaStart, in.spanEnd-in.metaStart+pybytes.SpliceSlack)
	if _, err := r.ReadAt(in.span, int64(in.metaStart)); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if err := core.VerifyStructure(in.span); err != nil {
		return nil, errFallback
	}
	if !fixedReadsFit(in.span) {
		return nil, errFallback
	}
	if err := checkExtentsOutside(in); err != nil {
		return nil, err
	}
	return in, nil
}

// fixedReadsFit checks that the span's boxes read at fixed offsets are big
// enough for those reads (see loadStreamInput).
func fixedReadsFit(span []byte) bool {
	top, err := core.ListBoxes(span, 0, len(span))
	if err != nil {
		return false
	}
	need := func(h core.BoxHeader, n int) bool { return h.ContentEnd-h.ContentStart >= n }
	for _, t := range top {
		if t.Is("uuid") && !need(t, 18) { // uuid + version + entry count
			return false
		}
		if !t.Is("meta") {
			continue
		}
		children, err := core.ListBoxes(span, t.ContentStart+4, t.ContentEnd)
		if err != nil {
			return false
		}
		for _, c := range children {
			switch {
			case c.Is("pitm"):
				if !need(c, 1) || !need(c, map[bool]int{true: 6, false: 8}[span[c.ContentStart] == 0]) {
					return false
				}
			case c.Is("iinf"):
				if !need(c, 1) {
					return false
				}
				skip := 6
				if span[c.ContentStart] != 0 {
					skip = 8
				}
				infes, err := core.ListBoxes(span, c.ContentStart+skip, c.ContentEnd)
				if err != nil {
					return false
				}
				for _, e := range infes {
					if !e.Is("infe") {
						continue
					}
					if !need(e, 1) {
						return false
					}
					if v := span[e.ContentStart]; (v == 2 && !need(e, 12)) || (v > 2 && !need(e, 14)) {
						return false
					}
				}
			case c.Is("iprp"):
				parts, err := core.ListBoxes(span, c.ContentStart, c.ContentEnd)
				if err != nil {
					return false
				}
				for _, part := range parts {
					if !part.Is("ipco") {
						continue
					}
					props, err := core.ListBoxes(span, part.ContentStart, part.ContentEnd)
					if err != nil {
						return false
					}
					for _, prop := range props {
						if prop.Is("irot") && !need(prop, 1) {
							return false
						}
					}
				}
			}
		}
	}
	return true
}

// checkExtentsOutside requires every absolute-offset iloc extent to lie
// outside the span, so reads of item data (the Exif blob) never land in
// bytes the edit rewrites.
func checkExtentsOutside(in *streamInput) error {
	s, err := core.LocateStructures(in.span)
	if err != nil {
		return errFallback
	}
	if s.IlocHdr == nil {
		return nil
	}
	iloc, err := core.ParseIloc(in.span, s.IlocHdr.ContentStart, s.IlocHdr.ContentEnd)
	if err != nil {
		return errFallback
	}
	lo, hi := uint64(in.metaStart), uint64(in.spanEnd)
	for _, it := range iloc.Items {
		if it.ConstrMethod != 0 {
			continue
		}
		for _, ext := range it.Extents {
			start := it.BaseOffset + ext.Offset
			if start < it.BaseOffset { // overflow: beyond any file
				continue
			}
			end := start + max(ext.Length, 1)
			if end < start {
				end = math.MaxUint64
			}
			if start < hi && end > lo {
				return errFallback
			}
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// The streaming FileEnv
// ---------------------------------------------------------------------------

// patch replaces input bytes [off, off+len(b)) in the output.
type patch struct {
	off int
	b   []byte
}

// streamEnv is the core.FileEnv for an edit of the span: the edited buffer is
// the span (or the output span derived from it), and the rest of the file -
// before metaStart and from spanEnd - is read from in.r. Output-side Exif
// patches are kept in input offsets, so they stay put as the span resizes.
type streamEnv struct {
	in      *streamInput
	patches []patch
}

func (e *streamEnv) Base() int { return e.in.metaStart }

// file returns the segments of the file buf belongs to: the input's bytes
// around it (with output patches applied when output is true), and buf in
// place of the original span.
func (e *streamEnv) file(buf []byte, output bool) []segment {
	var segs []segment
	addRange := func(lo, hi int) {
		for _, p := range e.patchesIn(lo, hi, output) {
			segs = append(segs, segment{off: int64(lo), n: int64(p.off - lo)}, segment{lit: p.b})
			lo = p.off + len(p.b)
		}
		segs = append(segs, segment{off: int64(lo), n: int64(hi - lo)})
	}
	addRange(0, e.in.metaStart)
	segs = append(segs, segment{lit: buf})
	addRange(e.in.spanEnd, e.in.size)
	return segs
}

func (e *streamEnv) patchesIn(lo, hi int, output bool) []patch {
	if !output {
		return nil
	}
	var ps []patch
	for _, p := range e.patches {
		if p.off >= lo && p.off+len(p.b) <= hi {
			ps = append(ps, p)
		}
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].off < ps[j].off })
	return ps
}

func (e *streamEnv) InputCRC(in []byte) (uint32, error) {
	v := newView(e.in.r, e.file(in, false))
	return v.crcExcluding(v.size, v.size)
}

func (e *streamEnv) InputCRCExcluding(in []byte, a, b int) (uint32, error) {
	return newView(e.in.r, e.file(in, false)).crcExcluding(int64(e.Base()+a), int64(e.Base()+b))
}

func (e *streamEnv) OutputCRCExcluding(out []byte, a, b int) (uint32, error) {
	return newView(e.in.r, e.file(out, true)).crcExcluding(int64(e.Base()+a), int64(e.Base()+b))
}

// toInput maps a file offset in the file buf belongs to (the input, or an
// output whose span is buf) to an input offset; ok is false inside the span.
func (e *streamEnv) toInput(buf []byte, off int) (int, bool) {
	switch {
	case off < e.in.metaStart:
		return off, true
	case off >= e.in.metaStart+len(buf):
		return off - (len(buf) - len(e.in.span)), true
	}
	return 0, false
}

// rangeToInput maps the n-byte range at file offset off (in the file buf
// belongs to) to input offsets [lo, hi); ok is false if any byte of it is
// inside the span. Zero-length ranges map their start.
func (e *streamEnv) rangeToInput(buf []byte, off, n int) (lo, hi int, ok bool) {
	lo, ok = e.toInput(buf, off)
	if !ok || n == 0 {
		return lo, lo, ok
	}
	last, ok := e.toInput(buf, off+n-1)
	if !ok || last-lo != n-1 {
		return 0, 0, false
	}
	return lo, last + 1, true
}

func (e *streamEnv) ExifField(buf []byte, metaBodyStart, metaCEnd int) (core.ExifOrientationField, bool, error) {
	loc, ok, err := core.FindExifItemLocation(buf, metaBodyStart, metaCEnd)
	if err != nil || !ok {
		return core.ExifOrientationField{}, false, err
	}
	fileSize := e.in.size + len(buf) - len(e.in.span)
	if loc.AbsStart >= uint64(fileSize) {
		field, ok := core.ExifFieldInBlob(nil, loc.AbsStart) // pySlice64 past the end: empty
		return field, ok, nil
	}
	start := int(loc.AbsStart)
	n := int(min(loc.Length, uint64(fileSize-start)))
	if n > exifBlobCap {
		return core.ExifOrientationField{}, false, errFallback
	}
	inStart, _, ok := e.rangeToInput(buf, start, n)
	if !ok {
		return core.ExifOrientationField{}, false, errFallback // the blob would touch the span
	}
	blob := make([]byte, n)
	if _, err := newView(e.in.r, []segment{{off: int64(inStart), n: int64(n)}}).ReadAt(blob, 0); err != nil {
		return core.ExifOrientationField{}, false, err
	}
	field, ok := core.ExifFieldInBlob(blob, loc.AbsStart)
	return field, ok, nil
}

func (e *streamEnv) WriteExif(out []byte, field core.ExifOrientationField, value uint16) error {
	off, end, ok := e.rangeToInput(out, field.AbsOffset, 2)
	if !ok || end > e.in.size {
		return errFallback
	}
	b := make([]byte, 2)
	field.ByteOrder.PutUint16(b, value)
	e.patches = append(e.patches, patch{off, b})
	return nil
}

// ---------------------------------------------------------------------------
// Public streaming API
// ---------------------------------------------------------------------------

// readAll reads the whole input, for the []byte engine fallback.
func readAll(r io.ReaderAt, size int64) ([]byte, error) {
	if size < 0 || size > math.MaxInt {
		return nil, errors.New("stream: input too large for this platform")
	}
	data := make([]byte, size)
	if _, err := r.ReadAt(data, 0); err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return data, nil
}

func noteFallback(op string, cause error) {
	if streamFallbackHook != nil {
		streamFallbackHook(op, cause)
	}
}

// logBuffer collects the streaming attempt's log, released only if it
// succeeds - a fallback re-runs and logs from scratch.
func logBuffer(w io.Writer) *bytes.Buffer {
	if w == nil {
		return nil
	}
	return new(bytes.Buffer)
}

func asWriter(b *bytes.Buffer) io.Writer {
	if b == nil {
		return nil
	}
	return b
}

func flushLog(w io.Writer, b *bytes.Buffer) {
	if w != nil && b != nil {
		w.Write(b.Bytes())
	}
}

// ApplyRotationReaderAt is ApplyRotation over an io.ReaderAt of the given
// size: the same result, as a View that reads unchanged bytes from r on
// demand. For well-formed files only the 'meta' and provenance boxes are
// held in memory; anything unusual falls back to reading all of r into
// memory and running ApplyRotation. r must stay unchanged and readable
// while the View is used.
func ApplyRotationReaderAt(r io.ReaderAt, size int64, deltaTurns int, opts core.RotateOptions) (*View, error) {
	buf := logBuffer(opts.Log)
	in, err := loadStreamInput(r, size)
	if err == nil {
		env := &streamEnv{in: in}
		var out []byte
		out, err = core.ApplyRotationEnv(env, in.span, deltaTurns, core.RotateOptions{NoExifSync: opts.NoExifSync, Log: asWriter(buf)})
		if err == nil {
			flushLog(opts.Log, buf)
			return newView(r, env.file(out, true)), nil
		}
	}
	noteFallback("rotate", err)
	data, err := readAll(r, size)
	if err != nil {
		return nil, err
	}
	out, err := core.ApplyRotation(data, deltaTurns, opts)
	if err != nil {
		return nil, err
	}
	return ViewOfBytes(out), nil
}

// reverseStream runs prepareReversalEnv on the span; a non-nil error other
// than errNoProvenance means "fall back".
func reverseStream(r io.ReaderAt, size int64, opts core.ReverseOptions, log io.Writer) (*core.Reversal, *streamInput, error) {
	in, err := loadStreamInput(r, size)
	if err != nil {
		return nil, nil, err
	}
	rev, err := core.PrepareReversalEnv(&streamEnv{in: in}, in.span, opts.IgnoreTamper, log)
	return rev, in, err
}

// reversalView is reconstructedPlan as a View over the input.
func reversalView(r io.ReaderAt, size int, rev *core.Reversal) *View {
	var segs []segment
	for _, c := range core.ReconstructedPlan(rev) {
		if c.Lit != nil {
			segs = append(segs, segment{lit: c.Lit})
			continue
		}
		lo, hi := max(0, min(c.Lo, size)), max(0, min(c.Hi, size))
		hi = max(lo, hi)
		segs = append(segs, segment{off: int64(lo), n: int64(hi - lo)})
	}
	return newView(r, segs)
}

// ReverseRotationReaderAt is ReverseRotation over an io.ReaderAt, with the
// same result as a View (see ApplyRotationReaderAt).
func ReverseRotationReaderAt(r io.ReaderAt, size int64, opts core.ReverseOptions) (*View, error) {
	buf := logBuffer(opts.Log)
	rev, in, err := reverseStream(r, size, opts, asWriter(buf))
	if err == nil {
		v := reversalView(r, in.size, rev)
		var crc uint32
		if crc, err = v.crcExcluding(v.size, v.size); err == nil {
			if err = core.CheckReconstructedCrc(rev, crc, asWriter(buf)); err == nil {
				flushLog(opts.Log, buf)
				return v, nil
			}
		}
	}
	if errors.Is(err, core.ErrNoProvenance) { // exact without the []byte engine
		flushLog(opts.Log, buf)
		return nil, err
	}
	noteFallback("reverse", err)
	data, err := readAll(r, size)
	if err != nil {
		return nil, err
	}
	out, err := core.ReverseRotation(data, opts)
	if err != nil {
		return nil, err
	}
	return ViewOfBytes(out), nil
}

// VerifyReversibleReaderAt is VerifyReversible over an io.ReaderAt.
func VerifyReversibleReaderAt(r io.ReaderAt, size int64, opts core.ReverseOptions) error {
	buf := logBuffer(opts.Log)
	rev, in, err := reverseStream(r, size, opts, asWriter(buf))
	if err == nil {
		v := reversalView(r, in.size, rev)
		var crc uint32
		if crc, err = v.crcExcluding(v.size, v.size); err == nil {
			if err = core.CheckReconstructedCrc(rev, crc, asWriter(buf)); err == nil {
				flushLog(opts.Log, buf)
				return nil
			}
		}
	}
	if errors.Is(err, core.ErrNoProvenance) {
		flushLog(opts.Log, buf)
		return err
	}
	noteFallback("verify", err)
	data, err := readAll(r, size)
	if err != nil {
		return err
	}
	return core.VerifyReversible(data, opts)
}

// GatherInfoReaderAt is GatherInfo over an io.ReaderAt; for well-formed
// files it reads only the 'meta' and provenance boxes.
func GatherInfoReaderAt(r io.ReaderAt, size int64) (*core.Info, error) {
	in, err := loadStreamInput(r, size)
	if err == nil {
		err = errFallback
		// GatherInfo's lenient provenance parse reads entries without a
		// consumed-length error; stream only if the record is well-formed.
		s, lerr := core.LocateStructures(in.span)
		wellFormed := lerr == nil
		if wellFormed && s.ProvenanceHdr != nil {
			_, _, wellFormed, lerr = core.ParseProvenanceLenient(in.span, *s.ProvenanceHdr)
			wellFormed = wellFormed && lerr == nil
		}
		if wellFormed {
			var info *core.Info
			if info, err = core.GatherInfo(in.span); err == nil {
				return info, nil
			}
		}
	}
	noteFallback("info", err)
	data, err := readAll(r, size)
	if err != nil {
		return nil, err
	}
	return core.GatherInfo(data)
}

// VerifyStructureView is core.VerifyStructure over a View: the prechecks include
// VerifyStructure of the span and exact tiling of the top level, which
// together imply it for the whole file; otherwise check the bytes.
func VerifyStructureView(v *View) error {
	if len(v.segs) != 1 || v.segs[0].lit == nil {
		if _, err := loadStreamInput(v, v.size); err == nil {
			return nil
		}
		noteFallback("verify_structure", errFallback)
	}
	data, err := v.Bytes()
	if err != nil {
		return err
	}
	return core.VerifyStructure(data)
}
