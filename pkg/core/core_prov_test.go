package core_test

import (
	"encoding/binary"
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/weisnobody/go-heic-lossless-rotate/internal/synth"
	"github.com/weisnobody/go-heic-lossless-rotate/internal/tests"
	"github.com/weisnobody/go-heic-lossless-rotate/pkg/core"
)

// Expected values in this file were captured from upstream core.py.

const sampleProvHex = "0000003975756964e850777c341650a49fa86493cfc13f310203434352430000000400000007" +
	"4f495254000000005456455200000003010701"

func sampleProvEntries() core.ProvenanceEntries {
	return core.ProvenanceEntries{"TVER": {1, 7, 1}, "CCRC": {0, 0, 0, 7}, "OIRT": {}}
}

func uuidHdr(b []byte) core.BoxHeader {
	return core.BoxHeader{0, 8, 8, len(b), [4]byte{'u', 'u', 'i', 'd'}}
}

func TestBuildProvenanceBox(t *testing.T) {
	got, err := core.BuildProvenanceBox(2, sampleProvEntries())
	if err != nil || hex.EncodeToString(got) != sampleProvHex {
		t.Fatalf("BuildProvenanceBox = %x, %v\nwant %s", got, err, sampleProvHex)
	}
	if _, err := core.BuildProvenanceBox(2, core.ProvenanceEntries{"TOOLONG": nil}); err == nil {
		t.Error("5+ byte tag: want error (Python AssertionError)")
	}
}

func TestParseProvenance(t *testing.T) {
	box := mustHex(t, sampleProvHex)
	version, entries, err := core.ParseProvenance(box, uuidHdr(box))
	if err != nil || version != 2 || !reflect.DeepEqual(entries, sampleProvEntries()) {
		t.Errorf("ParseProvenance = %d, %v, %v", version, entries, err)
	}

	newer := append([]byte{}, box...)
	newer[24] = 3
	_, _, err = core.ParseProvenance(newer, uuidHdr(newer))
	wantProvenanceError(t, err, "provenance box format version 3 is newer than this script understands (max 2) - update heic-lossless-rotate")
	version, entries, wellFormed, err := core.ParseProvenanceLenient(newer, uuidHdr(newer))
	if err != nil || version != 3 || !wellFormed || !reflect.DeepEqual(entries, sampleProvEntries()) {
		t.Errorf("ParseProvenanceLenient(newer) = %d, %v, %v, %v", version, entries, wellFormed, err)
	}

	short := append([]byte{}, box...)
	short[25] = 2 // claims 2 entries of 3
	_, _, err = core.ParseProvenance(short, uuidHdr(short))
	wantProvenanceError(t, err, "provenance box parse mismatch: consumed 38, expected 49 (corrupt or truncated?)")
	_, entries, wellFormed, err = core.ParseProvenanceLenient(short, uuidHdr(short))
	if err != nil || wellFormed || len(entries) != 2 {
		t.Errorf("ParseProvenanceLenient(short) = %v, %v, %v", entries, wellFormed, err)
	}

	// Duplicate tags: the later one wins, as with a Python dict.
	dup := append([]byte{}, box[:24]...)
	dup = append(dup, 2, 2)
	dup = append(append(dup, "TVER"...), 0, 0, 0, 1, 'A')
	dup = append(append(dup, "TVER"...), 0, 0, 0, 1, 'B')
	binary.BigEndian.PutUint32(dup, uint32(len(dup)))
	_, entries, err = core.ParseProvenance(dup, uuidHdr(dup))
	if err != nil || !reflect.DeepEqual(entries, core.ProvenanceEntries{"TVER": []byte("B")}) {
		t.Errorf("duplicate tags = %v, %v", entries, err)
	}
}

func wantProvenanceError(t *testing.T, err error, msg string) {
	t.Helper()
	pe, ok := err.(*core.ProvenanceError)
	if !ok {
		t.Fatalf("err = %v (%T), want *ProvenanceError", err, err)
	}
	if msg != "" && pe.Msg != msg {
		t.Errorf("message:\n  got  %s\n  want %s", pe.Msg, msg)
	}
}

func TestCrc32ExcludingRange(t *testing.T) {
	data := []byte("abcdefgh")
	for _, tt := range []struct {
		s, e int
		want uint32
	}{{2, 5, 3603969520}, {5, 2, 212554987}, {3, 3, 2934909520}} {
		if got := core.Crc32ExcludingRange(data, tt.s, tt.e); got != tt.want {
			t.Errorf("Crc32ExcludingRange(%d, %d) = %d, want %d", tt.s, tt.e, got, tt.want)
		}
	}
}

func TestGetCurrentRotation(t *testing.T) {
	for _, irot := range []int{-1, 0, 1, 2, 3} {
		data := tests.MustSynth(t, func(o *synth.Options) {
			o.Layout, o.IncludeIrot, o.IrotValue = synth.MetaFirst, irot >= 0, max(irot, 0)
		})
		if got, err := core.GetCurrentRotation(data); err != nil || got != max(irot, 0) {
			t.Errorf("irot %d: GetCurrentRotation = %d, %v", irot, got, err)
		}
	}
}

func TestLocateStructuresErrors(t *testing.T) {
	data := tests.MustSynth(t, nil)
	replace := func(old, new string) []byte {
		out := append([]byte{}, data...)
		for i := 0; i+4 <= len(out); i++ {
			if string(out[i:i+4]) == old {
				copy(out[i:], new)
			}
		}
		return out
	}
	tests := []struct {
		data []byte
		msg  string
	}{
		{nil, "no top-level 'meta' box found - not a valid HEIF file?"},
		{replace("pitm", "pitX"), "no 'pitm' (primary item) box found inside 'meta'"},
		{replace("iprp", "iprX"), "no 'iprp' box found inside 'meta'"},
	}
	for _, tt := range tests {
		_, err := core.LocateStructures(tt.data)
		wantBoxParseError(t, err, tt.msg)
	}
	s, err := core.LocateStructures(data)
	if err != nil || s.PrimaryItemID != 1 || s.IlocHdr == nil || s.ProvenanceHdr != nil || !s.IprpHdr.Is("iprp") {
		t.Errorf("LocateStructures = %+v, %v", s, err)
	}
}

func TestExtractIrotState(t *testing.T) {
	for _, withIrot := range []bool{false, true} {
		data := tests.MustSynth(t, func(o *synth.Options) { o.IncludeIrot, o.IrotValue = withIrot, 3 })
		s, err := core.LocateStructures(data)
		if err != nil {
			t.Fatal(err)
		}
		had, b, flags, err := core.ExtractIrotState(data, s.IprpHdr.ContentStart, s.IprpHdr.ContentEnd, s.PrimaryItemID)
		if err != nil || had != withIrot || (withIrot && b != 3) || flags != 0 {
			t.Errorf("irot=%v: extractIrotState = %v, %d, %d, %v", withIrot, had, b, flags, err)
		}
	}
	_, _, _, err := core.ExtractIrotState(box("free", nil), 0, 8, 1)
	wantBoxParseError(t, err, "'iprp' is missing 'ipco' or 'ipma'")
}
