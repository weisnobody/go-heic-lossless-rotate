package heicrotate

import (
	"crypto/sha1"
	"errors"
	"fmt"
	"testing"

	"github.com/pjg/go-heic-lossless-rotate/pkg/version"
)

func TestProvenanceUUIDDerivation(t *testing.T) {
	// uuid.uuid5(uuid.NAMESPACE_URL, 'urn:heic_rotate.py:provenance:v1')
	namespaceURL := []byte{
		0x6b, 0xa7, 0xb8, 0x11, 0x9d, 0xad, 0x11, 0xd1,
		0x80, 0xb4, 0x00, 0xc0, 0x4f, 0xd4, 0x30, 0xc8,
	}
	h := sha1.Sum(append(namespaceURL, "urn:heic_rotate.py:provenance:v1"...))
	var want [16]byte
	copy(want[:], h[:16])
	want[6] = want[6]&0x0f | 0x50 // version 5
	want[8] = want[8]&0x3f | 0x80 // RFC 4122 variant
	if ProvenanceUUID != want {
		t.Fatalf("ProvenanceUUID = %x, want %x", ProvenanceUUID, want)
	}
}

func TestVersionTuple(t *testing.T) {
	got, err := versionTuple()
	if err != nil {
		t.Fatal(err)
	}
	if s := fmt.Sprintf("%d.%d.%d", got[0], got[1], got[2]); s != version.Version {
		t.Errorf("versionTuple() = %v, want %s", got, version.Version)
	}
}

func TestVersionString(t *testing.T) {
	want := "heic-lossless-rotate 1.7.1\n" +
		"metadata (provenance) format version: 2\n" +
		"metadata format versions supported by reverse: 1, 2"
	if got := VersionString(); got != want {
		t.Errorf("VersionString() =\n%s\nwant\n%s", got, want)
	}
}

func TestErrorTypes(t *testing.T) {
	cause := errors.New("cause")
	var err error = &ProvenanceError{Msg: "outer", Err: cause}
	if err.Error() != "outer" || !errors.Is(err, cause) {
		t.Errorf("ProvenanceError: Error()=%q, Is(cause)=%v", err.Error(), errors.Is(err, cause))
	}
	var pe *ProvenanceError
	if !errors.As(err, &pe) {
		t.Error("errors.As(*ProvenanceError) failed")
	}
	if pe := provenanceErrorf("missing %s", "CCRC"); pe.Msg != "missing CCRC" || pe.Err != nil {
		t.Errorf("provenanceErrorf: %#v", pe)
	}
	err = boxParseErrorf("bad %s", "box")
	var be *BoxParseError
	if !errors.As(err, &be) || be.Msg != "bad box" || errors.Unwrap(err) != nil {
		t.Errorf("BoxParseError: %#v", err)
	}
}
