// Package tests holds test helpers shared by the module's packages, including
// ports of upstream's test-module helpers (_strip_provenance_tag,
// _make_v1_style_record), and black-box tests that span packages.
package tests

import (
	"bytes"
	"runtime"
	"testing"

	"github.com/pjg/go-heic-lossless-rotate/internal/pybytes"
	"github.com/pjg/go-heic-lossless-rotate/internal/synth"
	"github.com/pjg/go-heic-lossless-rotate/pkg/core"
)

func MustSynth(t testing.TB, mutate func(*synth.Options)) []byte {
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

func MustApply(t testing.TB, data []byte, turns int) []byte {
	t.Helper()
	out, err := core.ApplyRotation(data, turns, core.RotateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func MustReverse(t testing.TB, data []byte) []byte {
	t.Helper()
	out, err := core.ReverseRotation(data, core.ReverseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func MustLocate(t testing.TB, data []byte) *core.Structures {
	t.Helper()
	s, err := core.LocateStructures(data)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func MustVerifyStructure(t testing.TB, data []byte) {
	t.Helper()
	if err := core.VerifyStructure(data); err != nil {
		t.Fatal(err)
	}
}

// CyclicPayload mirrors bytes((i % 256) for i in range(n)).
func CyclicPayload(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(i)
	}
	return b
}

// IpcoChildrenOf returns the primary iprp's ipco children.
func IpcoChildrenOf(t testing.TB, data []byte) []core.BoxHeader {
	t.Helper()
	s := MustLocate(t, data)
	ipco, ok, err := core.FindUniqueChild(data, s.IprpHdr.ContentStart, s.IprpHdr.ContentEnd, "ipco", "'iprp'")
	if err != nil || !ok {
		t.Fatalf("no ipco: %v", err)
	}
	children, err := core.ParseIpcoChildren(data, ipco.ContentStart, ipco.ContentEnd)
	if err != nil {
		t.Fatal(err)
	}
	return children
}

// StripProvenanceTag returns a self-consistent copy of data with one entry
// tag removed from its provenance record - simulating what a file edited by
// an OLDER version (one that predates that tag, e.g. pre-1.6.0 files
// lacking TVER) looks like on disk: the record is genuinely smaller, and
// iloc is patched to match (← _strip_provenance_tag).
func StripProvenanceTag(t testing.TB, data []byte, tag string) []byte {
	t.Helper()
	out := bytes.Clone(data)
	info := MustLocate(t, out)
	if info.ProvenanceHdr == nil {
		t.Fatal("no provenance record to strip a tag from")
	}
	prov := *info.ProvenanceHdr
	version, entries, err := core.ParseProvenance(out, prov)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := entries[tag]; !ok {
		t.Fatalf("%q not present in this record", tag)
	}
	delete(entries, tag)
	smaller, err := core.BuildProvenanceBox(version, entries)
	if err != nil {
		t.Fatal(err)
	}
	delta := len(smaller) - (prov.ContentEnd - prov.Start)
	if delta >= 0 {
		t.Fatal("expected the box to shrink after removing a tag")
	}
	newIloc, changed, err := core.PatchIlocOffsetsAtThreshold(out, info.IlocHdr, prov.ContentEnd, delta)
	if err != nil {
		t.Fatal(err)
	}
	if changed {
		out = pybytes.Splice(out, info.IlocHdr.ContentStart, info.IlocHdr.ContentEnd, newIloc)
	}
	p2 := MustLocate(t, out).ProvenanceHdr
	out = pybytes.Splice(out, p2.Start, p2.ContentEnd, smaller)
	MustVerifyStructure(t, out) // the simulated "legacy" file must itself be well-formed
	return out
}

// MakeV1StyleRecord returns an equivalent of rotatedV2 (a single
// ApplyRotation of original, in v2/OIRT format) whose provenance record uses
// the OLD v1/OPRP format instead: same PCRC/CCRC/OMSZ/OILC/OEXO/TVER, but a
// full pristine 'iprp' snapshot in place of OIRT(+OIPF)
// (← _make_v1_style_record). Only valid for the MdatFirst layout, where
// resizing the provenance box never needs iloc patching.
func MakeV1StyleRecord(t testing.TB, original, rotatedV2 []byte) []byte {
	t.Helper()
	iprp := MustLocate(t, original).IprpHdr
	pristineIprp := bytes.Clone(original[iprp.Start:iprp.ContentEnd])

	prov := *MustLocate(t, rotatedV2).ProvenanceHdr
	_, entries, err := core.ParseProvenance(rotatedV2, prov)
	if err != nil {
		t.Fatal(err)
	}
	v1Entries := core.ProvenanceEntries{}
	for k, v := range entries {
		if k != "OIRT" && k != "OIPF" {
			v1Entries[k] = v
		}
	}
	v1Entries["OPRP"] = pristineIprp
	v1Box, err := core.BuildProvenanceBox(1, v1Entries)
	if err != nil {
		t.Fatal(err)
	}
	return pybytes.Splice(bytes.Clone(rotatedV2), prov.Start, prov.ContentEnd, v1Box)
}

// AllocatedBytes reports the bytes heap-allocated while running f.
func AllocatedBytes(f func()) uint64 {
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.TotalAlloc - before.TotalAlloc
}
