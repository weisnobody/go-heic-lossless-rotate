package core_test

import (
	"reflect"
	"testing"

	"github.com/pjg/go-heic-lossless-rotate/internal/synth"
	"github.com/pjg/go-heic-lossless-rotate/internal/tests"
	"github.com/pjg/go-heic-lossless-rotate/pkg/core"
)

// Expected values in this file were captured from upstream core.py.

func intp(v int) *int       { return &v }
func u32p(v uint32) *uint32 { return &v }

func TestGatherAndFormatInfo(t *testing.T) {
	fresh := tests.MustSynth(t, func(o *synth.Options) { o.IncludeIrot, o.IrotValue = true, 3 })
	r2src := tests.MustSynth(t, func(o *synth.Options) { o.IncludeExif, o.Layout = true, synth.MetaFirst })
	tests := []struct {
		name     string
		data     []byte
		info     core.Info
		text     string
		exitCode int
	}{
		{"fresh with native irot", fresh, core.Info{CurrentQuarterTurns: 3},
			"Not rotated with heic-lossless-rotate (no provenance record found).\n" +
				"Note: the file currently has its own 'irot' value of 270 deg from elsewhere (not something this tool did).", 0},
		{"plain", tests.MustSynth(t, nil), core.Info{},
			"Not rotated with heic-lossless-rotate (no provenance record found).", 0},
		{"rotated once", tests.MustApply(t, fresh, 1), core.Info{
			CurrentQuarterTurns: 0, HasProvenance: true, OriginalQuarterTurns: intp(3), DeltaQuarterTurns: intp(1),
			Version: 2, VersionSupported: true, ToolVersion: &[3]uint8{1, 7, 1}, PCRC: u32p(2862766951), HasIlocSnapshot: true,
		}, "Rotated with heic-lossless-rotate v1.7.1, provenance format v2.\n" +
			"  heic-lossless-rotate has added: 90 deg\n" +
			"  file's rotation: 270 deg originally -> 0 deg now\n" +
			"  original file CRC32:      0xaaa25b67\n" +
			"  also recorded: iloc snapshot", 1},
		{"rotated 180 with exif", tests.MustApply(t, r2src, 2), core.Info{
			CurrentQuarterTurns: 2, HasProvenance: true, OriginalQuarterTurns: intp(0), DeltaQuarterTurns: intp(2),
			Version: 2, VersionSupported: true, ToolVersion: &[3]uint8{1, 7, 1}, PCRC: u32p(3777746169),
			HasIlocSnapshot: true, HasExifSnapshot: true,
		}, "Rotated with heic-lossless-rotate v1.7.1, provenance format v2.\n" +
			"  heic-lossless-rotate has added: 180 deg\n" +
			"  file's rotation: 0 deg originally -> 180 deg now\n" +
			"  original file CRC32:      0xe12bd4f9\n" +
			"  also recorded: iloc snapshot, exif-orientation snapshot", 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := core.GatherInfo(tt.data)
			if err != nil || !reflect.DeepEqual(*got, tt.info) {
				t.Fatalf("GatherInfo = %+v, %v\nwant %+v", got, err, tt.info)
			}
			text, code := core.FormatInfo(got, false)
			if text != tt.text || code != tt.exitCode {
				t.Errorf("FormatInfo =\n%q, %d\nwant\n%q, %d", text, code, tt.text, tt.exitCode)
			}
			if text, code := core.FormatInfo(got, true); text != "" || code != tt.exitCode {
				t.Errorf("FormatInfo(quiet) = %q, %d", text, code)
			}
		})
	}
}

func TestFormatInfoNewerFormat(t *testing.T) {
	text, code := core.FormatInfo(&core.Info{CurrentQuarterTurns: 1, HasProvenance: true, Version: 3}, false)
	want := "Rotated with heic-lossless-rotate, provenance format v3 (NEWER than this script's v2 - shown info may be incomplete).\n" +
		"  currently at: 90 deg (original rotation snapshot unavailable - can't compute what this tool added specifically)"
	if text != want || code != 4 {
		t.Errorf("FormatInfo =\n%q, %d\nwant\n%q, 4", text, code, want)
	}
}

func TestVerifyStructure(t *testing.T) {
	ok := cat(box("ftyp", nil), box("meta", cat(make([]byte, 4), box("iprp", box("ipco", box("irot", []byte{0}))))))
	if err := core.VerifyStructure(ok); err != nil {
		t.Errorf("valid file: %v", err)
	}
	tests := []struct {
		data []byte
		msg  string
	}{
		{cat(box("ftyp", nil), box("meta", cat(make([]byte, 4), rawHeader(20, "irot"))), make([]byte, 16)),
			"meta: child 'b'irot'' at 20 overflows container end (40 > 28)"},
		{cat(box("ftyp", nil), box("meta", make([]byte, 2))), "meta: children end at 20, expected 18"},
		{cat(box("ftyp", nil), box("meta", make([]byte, 9))), "meta: truncated box at 20"},
		{cat(box("ftyp", nil), box("meta", cat(make([]byte, 4), box("iprp", rawHeader(99, "ipco"))))),
			"box b'ipco' at 28 claims size 99 beyond EOF"},
	}
	for _, tt := range tests {
		wantBoxParseError(t, core.VerifyStructure(tt.data), tt.msg)
	}
	// COMPAT D6: Python's check_container loops forever on this.
	zero := cat(box("ftyp", nil), rawHeader(1, "free"), make([]byte, 8))
	wantBoxParseError(t, core.VerifyStructure(zero), "")
}
