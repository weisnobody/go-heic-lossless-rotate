package core

import (
	"errors"
	"testing"
)

func TestPyAccessErrors(t *testing.T) {
	data := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09}
	if b, err := pyIndex(data, 8); err != nil || b != 0x09 {
		t.Errorf("pyIndex(8) = %d, %v", b, err)
	}
	for name, err := range map[string]error{
		"index": second(pyIndex(data, 9)),
		"u16":   second(unpackU16(data, 8)),
		"u32":   second(unpackU32(data, 6)),
		"u64":   second(unpackU64(data, 2)),
	} {
		var be *BoxParseError
		if !errors.As(err, &be) {
			t.Errorf("%s past end: err = %v, want *BoxParseError", name, err)
		}
	}
}

func second[T any](_ T, err error) error { return err }
