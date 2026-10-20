package core_test

// Test helpers used only by package core's tests, including a port of
// upstream's _structural_fingerprint.

import (
	"errors"
	"testing"

	"github.com/pjg/go-heic-lossless-rotate/internal/tests"
	"github.com/pjg/go-heic-lossless-rotate/pkg/core"
)

func isProvenanceError(err error) bool {
	var pe *core.ProvenanceError
	return errors.As(err, &pe)
}

// firstBox returns the first top-level box of the given type.
func firstBox(t testing.TB, data []byte, boxtype string) core.BoxHeader {
	t.Helper()
	h, ok, err := core.FindChild(data, 0, len(data), boxtype)
	if err != nil || !ok {
		t.Fatalf("no top-level %q: %v", boxtype, err)
	}
	return h
}

// fingerprint is _structural_fingerprint's (top_level, prov_fingerprint,
// current_rotation) tuple.
type fingerprint struct {
	top         []core.BoxHeader
	provVersion int // -1 when there is no provenance record
	provEntries core.ProvenanceEntries
	rotation    int
}

// structuralFingerprint summarizes data's box layout and provenance record,
// excluding mdat's payload bytes and the two CRC fields (PCRC/CCRC), which
// necessarily depend on mdat's content (← _structural_fingerprint).
func structuralFingerprint(t testing.TB, data []byte) fingerprint {
	t.Helper()
	top, err := core.ListBoxes(data, 0, len(data))
	if err != nil {
		t.Fatal(err)
	}
	fp := fingerprint{top: top, provVersion: -1}
	if prov := tests.MustLocate(t, data).ProvenanceHdr; prov != nil {
		version, entries, err := core.ParseProvenance(data, *prov)
		if err != nil {
			t.Fatal(err)
		}
		delete(entries, "PCRC")
		delete(entries, "CCRC")
		fp.provVersion, fp.provEntries = int(version), entries
	}
	if fp.rotation, err = core.GetCurrentRotation(data); err != nil {
		t.Fatal(err)
	}
	return fp
}
