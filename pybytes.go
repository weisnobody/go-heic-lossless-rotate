package heicrotate

import "encoding/binary"

// Python byte-access semantics.
//
// core.py relies on three different behaviors when an access runs past the
// end of the data, and reproducing them exactly is what keeps this port
// byte-identical on every input Python accepts:
//
//   - slicing (data[a:b], memoryview[a:b]) silently clamps, and a > b gives
//     an empty result;
//   - int.from_bytes(data[p:p+n]) therefore silently reads a SHORTER value;
//   - indexing (data[i]) and struct.unpack_from raise IndexError /
//     struct.error - which the CLI doesn't catch, so Python prints a
//     traceback. Go returns a *BoxParseError instead (docs/COMPAT.md D2).
//
// Parsers read across box boundaries into whatever bytes follow, exactly as
// Python does: bounds are always checked against len(data), never against a
// box's own end.

func errIndex(i, size int) *BoxParseError {
	return boxParseErrorf("truncated or malformed data: offset %d is past the end of the data (%d bytes)", i, size)
}

func errUnpack(n, pos, size int) *BoxParseError {
	return boxParseErrorf("truncated or malformed data: need %d bytes at offset %d, but the data is %d bytes", n, pos, size)
}

// pyIndex mirrors data[i] (i >= 0): IndexError past the end becomes a
// *BoxParseError.
func pyIndex(data []byte, i int) (byte, error) {
	if i < 0 || i >= len(data) {
		return 0, errIndex(i, len(data))
	}
	return data[i], nil
}

// clamp mirrors Python's slice-bound normalization for non-negative bounds.
func clamp(data []byte, lo, hi int) (int, int) {
	lo = max(0, min(lo, len(data)))
	hi = max(lo, min(hi, len(data)))
	return lo, hi
}

// pySlice mirrors memoryview(data)[lo:hi]: clamped, never fails, and aliases
// data. Use pySliceCopy where Python's slice makes an independent copy that
// later mutation must not affect.
func pySlice(data []byte, lo, hi int) []byte {
	lo, hi = clamp(data, lo, hi)
	return data[lo:hi:hi]
}

// pySliceCopy mirrors bytes(data[lo:hi]): clamped, never fails, and an
// independent copy.
func pySliceCopy(data []byte, lo, hi int) []byte {
	return append([]byte{}, pySlice(data, lo, hi)...)
}

// intFromBytes mirrors int.from_bytes(data[pos:pos+n], 'big'): the slice
// clamps, so a read past the end silently yields a shorter value. Python's
// result is arbitrary-precision; ok is false if the value doesn't fit in 64
// bits (callers decide when that matters - see docs/COMPAT.md D5). Widths
// over 8 bytes are fine as long as the leading bytes are zero.
func intFromBytes(data []byte, pos, n int) (v uint64, ok bool) {
	for _, c := range pySlice(data, pos, pos+n) {
		if v>>56 != 0 {
			return 0, false
		}
		v = v<<8 | uint64(c)
	}
	return v, true
}

// unpackU16 mirrors struct.unpack_from('>H', data, pos)[0].
func unpackU16(data []byte, pos int) (uint16, error) {
	if pos < 0 || pos+2 > len(data) {
		return 0, errUnpack(2, pos, len(data))
	}
	return binary.BigEndian.Uint16(data[pos:]), nil
}

// unpackU32 mirrors struct.unpack_from('>I', data, pos)[0].
func unpackU32(data []byte, pos int) (uint32, error) {
	if pos < 0 || pos+4 > len(data) {
		return 0, errUnpack(4, pos, len(data))
	}
	return binary.BigEndian.Uint32(data[pos:]), nil
}

// unpackU64 mirrors struct.unpack_from('>Q', data, pos)[0].
func unpackU64(data []byte, pos int) (uint64, error) {
	if pos < 0 || pos+8 > len(data) {
		return 0, errUnpack(8, pos, len(data))
	}
	return binary.BigEndian.Uint64(data[pos:]), nil
}

// spliceSlack is the spare capacity given to a working buffer that will be
// resized by splice, so a handful of small size changes (inserting 'irot', a
// provenance box) move bytes in place instead of reallocating the whole file.
const spliceSlack = 64 << 10

// splice mirrors bytearray slice assignment, buf[a:b] = repl: the range is
// clamped, b < a inserts at a, and the buffer's length changes by
// len(repl) - (b - a). Works in place when capacity allows. repl must not
// alias buf.
func splice(buf []byte, a, b int, repl []byte) []byte {
	a, b = clamp(buf, a, b)
	oldLen := len(buf)
	newLen := oldLen - (b - a) + len(repl)
	if newLen > cap(buf) {
		out := make([]byte, newLen, newLen+spliceSlack)
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

// pySlice64 mirrors memoryview(data)[start:start+length] for 64-bit values
// taken straight from a file (an iloc extent), which may lie far past the
// end of data or overflow when added.
func pySlice64(data []byte, start, length uint64) []byte {
	if start >= uint64(len(data)) {
		return data[len(data):]
	}
	end := uint64(len(data))
	if length < end-start {
		end = start + length
	}
	return data[start:end:end]
}
