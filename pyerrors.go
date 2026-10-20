package heicrotate

import "github.com/pjg/go-heic-lossless-rotate/internal/pybytes"

// Python's IndexError / struct.error, which the CLI doesn't catch, so Python
// prints a traceback; this port returns a *BoxParseError instead
// (docs/COMPAT.md D2). The accesses themselves are package pybytes'.

func errIndex(i, size int) *BoxParseError {
	return boxParseErrorf("truncated or malformed data: offset %d is past the end of the data (%d bytes)", i, size)
}

func errUnpack(n, pos, size int) *BoxParseError {
	return boxParseErrorf("truncated or malformed data: need %d bytes at offset %d, but the data is %d bytes", n, pos, size)
}

// pyIndex mirrors data[i] (i >= 0).
func pyIndex(data []byte, i int) (byte, error) {
	b, ok := pybytes.Index(data, i)
	if !ok {
		return 0, errIndex(i, len(data))
	}
	return b, nil
}

// unpackU16 mirrors struct.unpack_from('>H', data, pos)[0].
func unpackU16(data []byte, pos int) (uint16, error) {
	v, ok := pybytes.UnpackU16(data, pos)
	if !ok {
		return 0, errUnpack(2, pos, len(data))
	}
	return v, nil
}

// unpackU32 mirrors struct.unpack_from('>I', data, pos)[0].
func unpackU32(data []byte, pos int) (uint32, error) {
	v, ok := pybytes.UnpackU32(data, pos)
	if !ok {
		return 0, errUnpack(4, pos, len(data))
	}
	return v, nil
}

// unpackU64 mirrors struct.unpack_from('>Q', data, pos)[0].
func unpackU64(data []byte, pos int) (uint64, error) {
	v, ok := pybytes.UnpackU64(data, pos)
	if !ok {
		return 0, errUnpack(8, pos, len(data))
	}
	return v, nil
}
