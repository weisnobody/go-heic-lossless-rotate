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
	"fmt"
	"strconv"
	"strings"
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
