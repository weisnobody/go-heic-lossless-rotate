// Package pybytes reproduces Python's byte-access semantics for the core
// port.
//
// core.py relies on three different behaviors when an access runs past the
// end of the data, and reproducing them exactly is what keeps the port
// byte-identical on every input Python accepts:
//
//   - slicing (data[a:b], memoryview[a:b]) silently clamps, and a > b gives
//     an empty result;
//   - int.from_bytes(data[p:p+n]) therefore silently reads a SHORTER value;
//   - indexing (data[i]) and struct.unpack_from raise IndexError /
//     struct.error - which the CLI doesn't catch, so Python prints a
//     traceback. Here they report ok=false, and package core turns that into
//     a *core.BoxParseError (docs/COMPAT.md D2).
//
// Parsers read across box boundaries into whatever bytes follow, exactly as
// Python does: bounds are always checked against len(data), never against a
// box's own end.
package pybytes

import "encoding/binary"

// Index mirrors data[i] (i >= 0); ok is false where Python raises
// IndexError.
func Index(data []byte, i int) (b byte, ok bool) {
	if i < 0 || i >= len(data) {
		return 0, false
	}
	return data[i], true
}

// clamp mirrors Python's slice-bound normalization for non-negative bounds.
func clamp(data []byte, lo, hi int) (int, int) {
	lo = max(0, min(lo, len(data)))
	hi = max(lo, min(hi, len(data)))
	return lo, hi
}

// Slice mirrors memoryview(data)[lo:hi]: clamped, never fails, and aliases
// data. Use SliceCopy where Python's slice makes an independent copy that
// later mutation must not affect.
func Slice(data []byte, lo, hi int) []byte {
	lo, hi = clamp(data, lo, hi)
	return data[lo:hi:hi]
}

// SliceCopy mirrors bytes(data[lo:hi]): clamped, never fails, and an
// independent copy.
func SliceCopy(data []byte, lo, hi int) []byte {
	return append([]byte{}, Slice(data, lo, hi)...)
}

// IntFromBytes mirrors int.from_bytes(data[pos:pos+n], 'big'): the slice
// clamps, so a read past the end silently yields a shorter value. Python's
// result is arbitrary-precision; ok is false if the value doesn't fit in 64
// bits (callers decide when that matters - see docs/COMPAT.md D5). Widths
// over 8 bytes are fine as long as the leading bytes are zero.
func IntFromBytes(data []byte, pos, n int) (v uint64, ok bool) {
	for _, c := range Slice(data, pos, pos+n) {
		if v>>56 != 0 {
			return 0, false
		}
		v = v<<8 | uint64(c)
	}
	return v, true
}

// UnpackU16 mirrors struct.unpack_from('>H', data, pos)[0]; ok is false
// where Python raises struct.error.
func UnpackU16(data []byte, pos int) (v uint16, ok bool) {
	if pos < 0 || pos+2 > len(data) {
		return 0, false
	}
	return binary.BigEndian.Uint16(data[pos:]), true
}

// UnpackU32 mirrors struct.unpack_from('>I', data, pos)[0]; ok is false
// where Python raises struct.error.
func UnpackU32(data []byte, pos int) (v uint32, ok bool) {
	if pos < 0 || pos+4 > len(data) {
		return 0, false
	}
	return binary.BigEndian.Uint32(data[pos:]), true
}

// UnpackU64 mirrors struct.unpack_from('>Q', data, pos)[0]; ok is false
// where Python raises struct.error.
func UnpackU64(data []byte, pos int) (v uint64, ok bool) {
	if pos < 0 || pos+8 > len(data) {
		return 0, false
	}
	return binary.BigEndian.Uint64(data[pos:]), true
}

// SpliceSlack is the spare capacity given to a working buffer that will be
// resized by Splice, so a handful of small size changes (inserting 'irot', a
// provenance box) move bytes in place instead of reallocating the whole file.
const SpliceSlack = 64 << 10

// Splice mirrors bytearray slice assignment, buf[a:b] = repl: the range is
// clamped, b < a inserts at a, and the buffer's length changes by
// len(repl) - (b - a). Works in place when capacity allows. repl must not
// alias buf.
func Splice(buf []byte, a, b int, repl []byte) []byte {
	a, b = clamp(buf, a, b)
	oldLen := len(buf)
	newLen := oldLen - (b - a) + len(repl)
	if newLen > cap(buf) {
		out := make([]byte, newLen, newLen+SpliceSlack)
		copy(out, buf[:a])
		copy(out[a:], repl)
		copy(out[a+len(repl):], buf[b:])
		return out
	}
	if newLen > oldLen {
		buf = buf[:newLen]
	}
	copy(buf[a+len(repl):], buf[b:oldLen]) // memmove: overlapping copy is safe
	copy(buf[a:], repl)
	return buf[:newLen]
}

// Slice64 mirrors memoryview(data)[start:start+length] for 64-bit values
// taken straight from a file (an iloc extent), which may lie far past the
// end of data or overflow when added.
func Slice64(data []byte, start, length uint64) []byte {
	if start >= uint64(len(data)) {
		return data[len(data):]
	}
	end := uint64(len(data))
	if length < end-start {
		end = start + length
	}
	return data[start:end:end]
}
