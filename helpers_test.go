package heicrotate

// Shared test helpers, including ports of upstream's test-module helpers
// (_strip_provenance_tag, _make_v1_style_record, _structural_fingerprint).

import (
	"bytes"
	"errors"
	"testing"

	"github.com/pjg/go-heic-lossless-rotate/internal/synth"
)

func mustSynth(t testing.TB, mutate func(*synth.Options)) []byte {
	t.Helper()
	o := synth.DefaultOptions()
	if mutate != nil {
		mutate(&o)
	}
	data, err := synth.BuildSyntheticHEIC(o)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustApply(t testing.TB, data []byte, turns int) []byte {
	t.Helper()
	out, err := ApplyRotation(data, turns, RotateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func mustReverse(t testing.TB, data []byte) []byte {
	t.Helper()
	out, err := ReverseRotation(data, ReverseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func mustLocate(t testing.TB, data []byte) *Structures {
	t.Helper()
	s, err := LocateStructures(data)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func mustVerifyStructure(t testing.TB, data []byte) {
	t.Helper()
	if err := VerifyStructure(data); err != nil {
		t.Fatal(err)
	}
}

func isProvenanceError(err error) bool {
	var pe *ProvenanceError
	return errors.As(err, &pe)
}

// cyclicPayload mirrors bytes((i % 256) for i in range(n)).
func cyclicPayload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

// firstBox returns the first top-level box of the given type.
func firstBox(t testing.TB, data []byte, boxtype string) BoxHeader {
	t.Helper()
	h, ok, err := FindChild(data, 0, len(data), boxtype)
	if err != nil || !ok {
		t.Fatalf("no top-level %q: %v", boxtype, err)
	}
	return h
}

// ipcoChildren returns the primary iprp's ipco children.
func ipcoChildrenOf(t testing.TB, data []byte) []BoxHeader {
	t.Helper()
	s := mustLocate(t, data)
	ipco, ok, err := FindUniqueChild(data, s.IprpHdr.ContentStart, s.IprpHdr.ContentEnd, "ipco", "'iprp'")
	if err != nil || !ok {
		t.Fatalf("no ipco: %v", err)
	}
	children, err := ParseIpcoChildren(data, ipco.ContentStart, ipco.ContentEnd)
	if err != nil {
		t.Fatal(err)
	}
	return children
}

// stripProvenanceTag returns a self-consistent copy of data with one entry
// tag removed from its provenance record - simulating what a file edited by
// an OLDER version (one that predates that tag, e.g. pre-1.6.0 files
// lacking TVER) looks like on disk: the record is genuinely smaller, and
// iloc is patched to match (← _strip_provenance_tag).
func stripProvenanceTag(t testing.TB, data []byte, tag string) []byte {
	t.Helper()
	out := bytes.Clone(data)
	info := mustLocate(t, out)
	if info.ProvenanceHdr == nil {
		t.Fatal("no provenance record to strip a tag from")
	}
	prov := *info.ProvenanceHdr
	version, entries, err := ParseProvenance(out, prov)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := entries[tag]; !ok {
		t.Fatalf("%q not present in this record", tag)
	}
	delete(entries, tag)
	smaller, err := BuildProvenanceBox(version, entries)
	if err != nil {
		t.Fatal(err)
	}
	delta := len(smaller) - (prov.ContentEnd - prov.Start)
	if delta >= 0 {
		t.Fatal("expected the box to shrink after removing a tag")
	}
	newIloc, changed, err := PatchIlocOffsetsAtThreshold(out, info.IlocHdr, prov.ContentEnd, delta)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		out = splice(out, info.IlocHdr.ContentStart, info.IlocHdr.ContentEnd, newIloc)
	}
	p2 := mustLocate(t, out).ProvenanceHdr
	out = splice(out, p2.Start, p2.ContentEnd, smaller)
	mustVerifyStructure(t, out) // the simulated "legacy" file must itself be well-formed
	return out
}

// makeV1StyleRecord returns an equivalent of rotatedV2 (a single
// ApplyRotation of original, in v2/OIRT format) whose provenance record uses
// the OLD v1/OPRP format instead: same PCRC/CCRC/OMSZ/OILC/OEXO/TVER, but a
// full pristine 'iprp' snapshot in place of OIRT(+OIPF)
// (← _make_v1_style_record). Only valid for the MdatFirst layout, where
// resizing the provenance box never needs iloc patching.
func makeV1StyleRecord(t testing.TB, original, rotatedV2 []byte) []byte {
	t.Helper()
	iprp := mustLocate(t, original).IprpHdr
	pristineIprp := bytes.Clone(original[iprp.Start:iprp.ContentEnd])

	prov := *mustLocate(t, rotatedV2).ProvenanceHdr
	_, entries, err := ParseProvenance(rotatedV2, prov)
	if err != nil {
		t.Fatal(err)
	}
	v1Entries := ProvenanceEntries{}
	for k, v := range entries {
		if k != "OIRT" && k != "OIPF" {
			v1Entries[k] = v
		}
	}
	v1Entries["OPRP"] = pristineIprp
	v1Box, err := BuildProvenanceBox(1, v1Entries)
	if err != nil {
		t.Fatal(err)
	}
	return splice(bytes.Clone(rotatedV2), prov.Start, prov.ContentEnd, v1Box)
}

// fingerprint is _structural_fingerprint's (top_level, prov_fingerprint,
// current_rotation) tuple.
type fingerprint struct {
	top         []BoxHeader
	provVersion int // -1 when there is no provenance record
	provEntries ProvenanceEntries
	rotation    int
}

// structuralFingerprint summarizes data's box layout and provenance record,
// excluding mdat's payload bytes and the two CRC fields (PCRC/CCRC), which
// necessarily depend on mdat's content (← _structural_fingerprint).
func structuralFingerprint(t testing.TB, data []byte) fingerprint {
	t.Helper()
	top, err := listBoxes(data, 0, len(data))
	if err != nil {
		t.Fatal(err)
	}
	fp := fingerprint{top: top, provVersion: -1}
	if prov := mustLocate(t, data).ProvenanceHdr; prov != nil {
		version, entries, err := ParseProvenance(data, *prov)
		if err != nil {
			t.Fatal(err)
		}
		delete(entries, "PCRC")
		delete(entries, "CCRC")
		fp.provVersion, fp.provEntries = int(version), entries
	}
	if fp.rotation, err = GetCurrentRotation(data); err != nil {
		t.Fatal(err)
	}
	return fp
}
