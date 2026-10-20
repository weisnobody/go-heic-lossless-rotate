package core_test

// Port of upstream tests/test_heic_rotate.py, same classes in the same order
// (each Python TestCase becomes one Go test function with a subtest per
// method). Test groups follow upstream's numbering.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"iter"
	"reflect"
	"slices"
	"testing"

	"github.com/pjg/go-heic-lossless-rotate/internal/synth"
	"github.com/pjg/go-heic-lossless-rotate/internal/tests"
	"github.com/pjg/go-heic-lossless-rotate/pkg/core"
)

// ---------------------------------------------------------------------------
// 1. Unit tests for specific bugs found during development
// ---------------------------------------------------------------------------

// TestIlocPatchingStraddlingExtent: an item whose base_offset is nonzero and
// BEFORE an insertion threshold, but whose extent offset pushes its TRUE
// absolute position PAST that threshold, must still get patched. The
// original buggy implementation gated per-extent patching on
// base_offset == 0, which silently skipped exactly this case.
func TestIlocPatchingStraddlingExtent(t *testing.T) {
	buildIlocBox := func(baseOffset uint32, extents [][2]uint32) []byte {
		body := []byte{1, 0, 0, 0}                         // version=1, flags=0
		body = append(body, 4<<4|4, 4<<4)                  // offset/length_size=4; base_offset_size=4, index_size=0
		body = binary.BigEndian.AppendUint16(body, 1)      // item_count=1
		body = binary.BigEndian.AppendUint16(body, 42)     // item_id
		body = binary.BigEndian.AppendUint16(body, 0x0000) // reserved=0, constr_method=0
		body = binary.BigEndian.AppendUint16(body, 0)      // data_reference_index
		body = binary.BigEndian.AppendUint32(body, baseOffset)
		body = binary.BigEndian.AppendUint16(body, uint16(len(extents)))
		for _, e := range extents {
			body = binary.BigEndian.AppendUint32(body, e[0])
			body = binary.BigEndian.AppendUint32(body, e[1])
		}
		return box("iloc", body)
	}

	t.Run("straddling_extent_gets_patched", func(t *testing.T) {
		threshold, delta := 500, 1000
		// base_offset=100 (before threshold); extent1 -> absolute 150 (before
		// threshold, must NOT move); extent2 -> absolute 550 (after
		// threshold, MUST move by delta).
		ilocBox := buildIlocBox(100, [][2]uint32{{50, 10}, {450, 20}})
		ilocHdr := core.BoxHeader{0, 8, 8, len(ilocBox), [4]byte{'i', 'l', 'o', 'c'}}

		newContent, changed, err := core.PatchIlocOffsetsAtThreshold(ilocBox, &ilocHdr, threshold, delta)
		if err != nil || !changed {
			t.Fatalf("PatchIlocOffsetsAtThreshold = changed %v, err %v", changed, err)
		}
		patchedBox := append(append([]byte{}, ilocBox[:8]...), newContent...)
		parsed, err := core.ParseIloc(patchedBox, 8, len(patchedBox))
		if err != nil {
			t.Fatal(err)
		}
		item := parsed.Items[0]
		if item.BaseOffset != 100 {
			t.Errorf("base_offset = %d: base_offset must never be touched", item.BaseOffset)
		}
		if abs1 := item.BaseOffset + item.Extents[0].Offset; abs1 != 150 {
			t.Errorf("extent before threshold moved to %d, must be unchanged (150)", abs1)
		}
		if abs2 := item.BaseOffset + item.Extents[1].Offset; abs2 != uint64(550+delta) {
			t.Errorf("extent after threshold = %d, must shift by delta (%d)", abs2, 550+delta)
		}
	})

	t.Run("old_buggy_algorithm_actually_fails_this_case", func(t *testing.T) {
		// Documents WHY the fix was needed: replays the old (removed)
		// algorithm inline and confirms it computes the wrong answer for
		// the same input the fixed version gets right above.
		baseOffset := 100
		extents := [][2]int{{50, 10}, {450, 20}}
		threshold, delta := 500, 1000

		baseWasPatched := false
		if baseOffset >= threshold {
			baseOffset += delta
			baseWasPatched = true
		}
		if baseOffset == 0 || !baseWasPatched {
			for i := range extents {
				if baseOffset == 0 && extents[i][0] >= threshold {
					extents[i][0] += delta
				}
			}
		}
		if abs2 := baseOffset + extents[1][0]; abs2 == 550+delta {
			t.Error("this documents the OLD bug: the old algorithm must NOT correctly patch this case")
		}
	})
}

// TestIlocByteFidelity: FullBox 'flags' and construction_method's reserved
// bits must round-trip exactly, not get silently zeroed.
func TestIlocByteFidelity(t *testing.T) {
	t.Run("flags_and_reserved_bits_round_trip", func(t *testing.T) {
		body := []byte{1, 0xAB, 0xCD, 0xEF} // nonstandard flags
		body = append(body, 4<<4|4, 4<<4)   // offset/length_size=4; base_offset_size=4, index_size=0
		body = binary.BigEndian.AppendUint16(body, 1)
		body = binary.BigEndian.AppendUint16(body, 42)
		body = binary.BigEndian.AppendUint16(body, 0x1230) // reserved=0x123 (nonzero), constr_method=0
		body = binary.BigEndian.AppendUint16(body, 0)
		body = binary.BigEndian.AppendUint32(body, 100)
		body = binary.BigEndian.AppendUint16(body, 1)
		body = binary.BigEndian.AppendUint32(body, 50)
		body = binary.BigEndian.AppendUint32(body, 10)

		parsed, err := core.ParseIloc(body, 0, len(body))
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Flags != 0xABCDEF {
			t.Errorf("flags = %#x", parsed.Flags)
		}
		if raw := parsed.Items[0].RawConstrField; raw == nil || *raw != 0x1230 {
			t.Errorf("raw_constr_field = %v", raw)
		}
		rebuilt, err := core.BuildIlocContent(parsed)
		if err != nil || string(rebuilt) != string(body) {
			t.Errorf("flags and reserved bits must round-trip exactly:\n got % x\nwant % x (err %v)", rebuilt, body, err)
		}
	})
}

// TestDuplicateBoxDetection: the standard permits more than one 'ipma'
// inside an 'iprp'. Rebuilding from just the first match would silently
// discard the second box's associations - so we must refuse, not guess.
func TestDuplicateBoxDetection(t *testing.T) {
	t.Run("duplicate_ipma_is_refused", func(t *testing.T) {
		data := cat(rawHeader(8, "ipma"), rawHeader(8, "ipma"))
		_, _, err := core.FindUniqueChild(data, 0, len(data), "ipma", "test")
		wantBoxParseError(t, err, "")
	})
	t.Run("single_ipma_is_fine", func(t *testing.T) {
		data := rawHeader(8, "ipma")
		if _, ok, err := core.FindUniqueChild(data, 0, len(data), "ipma", "test"); err != nil || !ok {
			t.Errorf("FindUniqueChild = %v, %v", ok, err)
		}
	})
	t.Run("absent_ipma_returns_none", func(t *testing.T) {
		data := rawHeader(8, "ipco")
		if _, ok, err := core.FindUniqueChild(data, 0, len(data), "ipma", "test"); err != nil || ok {
			t.Errorf("FindUniqueChild = %v, %v; want not found", ok, err)
		}
	})
}

// ---------------------------------------------------------------------------
// 2. Synthetic-file integration tests - no real photo needed
// ---------------------------------------------------------------------------

// TestSyntheticRoundTrips exercises the full ApplyRotation/ReverseRotation
// pipeline against minimal synthetic files covering both box-layout orders -
// the exact distinction (mdat-before-meta vs meta-before-mdat) that exposed
// the real provenance-insertion bug upstream found.
func TestSyntheticRoundTrips(t *testing.T) {
	assertFullRoundTrip := func(t *testing.T, original []byte, deltaDegrees int, includeExif bool) {
		t.Helper()
		deltaTurns := (deltaDegrees / 90) % 4
		rotated, err := core.ApplyRotation(original, deltaTurns, core.RotateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		tests.MustVerifyStructure(t, rotated)

		// mdat payload (the "pixel data") must be untouched except for the
		// single intentional Exif-orientation byte, if an Exif item exists.
		om, rm := firstBox(t, original, "mdat"), firstBox(t, rotated, "mdat")
		origBytes, rotBytes := original[om.Start:om.ContentEnd], rotated[rm.Start:rm.ContentEnd]
		if len(origBytes) != len(rotBytes) {
			t.Fatal("mdat payload length must never change")
		}
		var diffs []int
		for i := range origBytes {
			if origBytes[i] != rotBytes[i] {
				diffs = append(diffs, i)
			}
		}
		maxAllowed := 0
		if includeExif && deltaTurns != 0 {
			maxAllowed = 1
		}
		if len(diffs) > maxAllowed {
			t.Errorf("unexpected byte differences in mdat payload: %v", diffs)
		}

		restored := tests.MustReverse(t, rotated)
		tests.MustVerifyStructure(t, restored)
		if !bytes.Equal(restored, original) {
			t.Error("reverse must reconstruct the exact original bytes")
		}
	}

	t.Run("all_layout_and_path_combinations", func(t *testing.T) {
		for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
			for _, includeIrot := range []bool{false, true} {
				for _, includeExif := range []bool{false, true} {
					t.Run(fmt.Sprintf("%s/irot=%v/exif=%v", layout, includeIrot, includeExif), func(t *testing.T) {
						original := tests.MustSynth(t, func(o *synth.Options) {
							o.Layout, o.IncludeIrot, o.IncludeExif = layout, includeIrot, includeExif
						})
						assertFullRoundTrip(t, original, 90, includeExif)
					})
				}
			}
		}
	})

	t.Run("multi_edit_chain_composes_additively", func(t *testing.T) {
		// rotate 90 then rotate 90 again must land at 180, not stay at 90 or reset.
		original := tests.MustSynth(t, func(o *synth.Options) { o.Layout = synth.MetaFirst })
		step2 := tests.MustApply(t, tests.MustApply(t, original, 1), 1)
		info, err := core.GatherInfo(step2)
		if err != nil || !info.HasProvenance || info.DeltaQuarterTurns == nil || *info.DeltaQuarterTurns != 2 {
			t.Fatalf("two +90 edits must compose to a net +180: %+v, %v", info, err)
		}
		if !bytes.Equal(tests.MustReverse(t, step2), original) {
			t.Error("reverse must undo BOTH edits back to the true original")
		}
	})

	t.Run("exif_orientation_sync_direction_matches_documented_mapping", func(t *testing.T) {
		// Ground truth is independently documented real-world exiftool
		// behavior (irot -> Exif Orientation), matching the HEIF 'irot'
		// spec's counter-clockwise angle definition.
		groundTruth := map[int]uint16{0: 1, 1: 8, 2: 3, 3: 6}
		readExifOrientation := func(data []byte) (uint16, bool) {
			info := tests.MustLocate(t, data)
			field, ok, err := core.FindExifOrientationField(data, info.MetaBodyStart, info.MetaHdr.ContentEnd)
			if err != nil {
				t.Fatal(err)
			}
			return field.Value, ok
		}
		for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
			for targetIrot := 0; targetIrot < 4; targetIrot++ {
				original := tests.MustSynth(t, func(o *synth.Options) {
					o.Layout, o.IncludeExif, o.ExifOrientation = layout, true, 1
				})
				if v, ok := readExifOrientation(original); !ok || v != 1 {
					t.Fatal("fixture sanity check: starting Exif orientation must actually be readable")
				}
				rotated := tests.MustApply(t, original, targetIrot)
				if r, err := core.GetCurrentRotation(rotated); err != nil || r != targetIrot {
					t.Errorf("GetCurrentRotation = %d, %v; want %d", r, err, targetIrot)
				}
				if v, ok := readExifOrientation(rotated); !ok || v != groundTruth[targetIrot] {
					t.Errorf("%s: irot=%d must sync Exif Orientation to %d (documented exiftool behavior), got %d",
						layout, targetIrot, groundTruth[targetIrot], v)
				}
			}
		}
	})

	t.Run("rotate_zero_preserves_existing_rotation", func(t *testing.T) {
		original := tests.MustSynth(t, func(o *synth.Options) { o.IncludeIrot, o.IrotValue = true, 2 })
		if r, err := core.GetCurrentRotation(tests.MustApply(t, original, 0)); err != nil || r != 2 {
			t.Errorf("rotate 0 must not alter an existing rotation: got %d, %v", r, err)
		}
	})

	t.Run("tamper_of_saved_irot_byte_recovers_with_force_fast_path", func(t *testing.T) {
		// FAST PATH: the ORIGINAL 'irot' byte (OIRT) is unconditionally
		// written back on reverse, so tampering with that live byte still
		// recovers cleanly with --ignore-tamper-check.
		original := tests.MustSynth(t, func(o *synth.Options) { o.IncludeIrot, o.IrotValue = true, 1 })
		rotated := tests.MustApply(t, original, 1) // fast path, 90->180
		for _, c := range tests.IpcoChildrenOf(t, rotated) {
			if c.Is("irot") {
				rotated[c.ContentStart] = 0xFF // garbage - not even a valid 2-bit angle
				break
			}
		}
		if _, err := core.ReverseRotation(rotated, core.ReverseOptions{}); !isProvenanceError(err) {
			t.Errorf("without force: err = %v, want *ProvenanceError", err)
		}
		restored, err := core.ReverseRotation(rotated, core.ReverseOptions{IgnoreTamper: true})
		if err != nil || !bytes.Equal(restored, original) {
			t.Errorf("with force: restored original = %v, err %v", bytes.Equal(restored, original), err)
		}
	})

	t.Run("tamper_of_discarded_irot_box_recovers_with_force_slow_path", func(t *testing.T) {
		// SLOW PATH: the inserted 'irot' box is discarded wholesale on
		// reverse, so tampering with its own bytes still recovers.
		original := tests.MustSynth(t, nil)
		rotated := tests.MustApply(t, original, 1) // slow path
		children := tests.IpcoChildrenOf(t, rotated)
		last := children[len(children)-1]
		if !last.Is("irot") {
			t.Fatal("fixture sanity check: last ipco child must be the inserted irot")
		}
		rotated[last.ContentStart] = 0xFF
		if _, err := core.ReverseRotation(rotated, core.ReverseOptions{}); !isProvenanceError(err) {
			t.Errorf("without force: err = %v, want *ProvenanceError", err)
		}
		restored, err := core.ReverseRotation(rotated, core.ReverseOptions{IgnoreTamper: true})
		if err != nil || !bytes.Equal(restored, original) {
			t.Errorf("with force: restored original = %v, err %v", bytes.Equal(restored, original), err)
		}
	})

	t.Run("tamper_elsewhere_in_iprp_still_refused_even_with_force", func(t *testing.T) {
		// v2's lean record doesn't blindly overwrite iprp on reverse, so
		// tampering with anything else in it (here, inside 'ispe') must be
		// caught by the final CRC self-check even with --ignore-tamper-check.
		for _, includeIrot := range []bool{true, false} {
			original := tests.MustSynth(t, func(o *synth.Options) { o.IncludeIrot = includeIrot })
			rotated := tests.MustApply(t, original, 1)
			for _, c := range tests.IpcoChildrenOf(t, rotated) {
				if c.Is("ispe") {
					rotated[c.ContentStart] ^= 0xFF
					break
				}
			}
			for _, force := range []bool{false, true} {
				if _, err := core.ReverseRotation(rotated, core.ReverseOptions{IgnoreTamper: force}); !isProvenanceError(err) {
					t.Errorf("fast_path=%v force=%v: err = %v, want *ProvenanceError", includeIrot, force, err)
				}
			}
		}
	})

	t.Run("tamper_outside_restored_region_still_refused_even_with_force", func(t *testing.T) {
		rotated := tests.MustApply(t, tests.MustSynth(t, nil), 1)
		rotated[10] ^= 0xFF // inside 'ftyp' - never touched by reverse at all
		for _, force := range []bool{false, true} {
			if _, err := core.ReverseRotation(rotated, core.ReverseOptions{IgnoreTamper: force}); !isProvenanceError(err) {
				t.Errorf("force=%v: err = %v, want *ProvenanceError", force, err)
			}
		}
	})
}

// TestVerifyReversibleMatchesReverseRotation cross-checks VerifyReversible
// against ReverseRotation directly: rotate's post-edit self-check relies on
// VerifyReversible exclusively, so it must never silently diverge.
func TestVerifyReversibleMatchesReverseRotation(t *testing.T) {
	// assertAgree runs both and asserts an identical verdict; returns
	// ReverseRotation's bytes (nil if both failed).
	assertAgree := func(t *testing.T, rotated []byte, ignoreTamper bool) []byte {
		t.Helper()
		opts := core.ReverseOptions{IgnoreTamper: ignoreTamper}
		restored, reverseErr := core.ReverseRotation(rotated, opts)
		verifyErr := core.VerifyReversible(rotated, opts)
		if (reverseErr == nil) != (verifyErr == nil) {
			t.Fatalf("ReverseRotation and VerifyReversible disagreed on whether this file is reversible: "+
				"ReverseRotation err=%v, VerifyReversible err=%v", reverseErr, verifyErr)
		}
		return restored
	}
	midStreamExif := func(layout synth.Layout) []byte {
		return tests.MustSynth(t, func(o *synth.Options) {
			o.Layout, o.IncludeExif, o.ExifPosition, o.ExifOrientation = layout, true, synth.ExifMiddle, 1
			o.MdatPayload = bytes.Repeat(tests.CyclicPayload(256), 4096)
		})
	}

	t.Run("agrees_across_layout_path_and_exif_combinations", func(t *testing.T) {
		for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
			for _, includeIrot := range []bool{false, true} {
				for _, includeExif := range []bool{false, true} {
					original := tests.MustSynth(t, func(o *synth.Options) {
						o.Layout, o.IncludeIrot, o.IncludeExif = layout, includeIrot, includeExif
					})
					if !bytes.Equal(assertAgree(t, tests.MustApply(t, original, 1), false), original) {
						t.Errorf("%s irot=%v exif=%v: restored != original", layout, includeIrot, includeExif)
					}
				}
			}
		}
	})

	t.Run("agrees_with_mid_stream_exif", func(t *testing.T) {
		for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
			original := midStreamExif(layout)
			for targetIrot := 0; targetIrot < 4; targetIrot++ {
				if !bytes.Equal(assertAgree(t, tests.MustApply(t, original, targetIrot), false), original) {
					t.Errorf("%s irot=%d: restored != original", layout, targetIrot)
				}
			}
		}
	})

	t.Run("agrees_with_multi_edit_chain", func(t *testing.T) {
		original := tests.MustSynth(t, func(o *synth.Options) { o.Layout = synth.MetaFirst })
		if !bytes.Equal(assertAgree(t, tests.MustApply(t, tests.MustApply(t, original, 1), 1), false), original) {
			t.Error("restored != original")
		}
	})

	t.Run("agrees_with_large_payload", func(t *testing.T) {
		payload := tests.CyclicPayload(2 << 20)
		for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
			for _, includeExif := range []bool{false, true} {
				original := tests.MustSynth(t, func(o *synth.Options) {
					o.Layout, o.MdatPayload, o.IncludeExif = layout, payload, includeExif
				})
				if !bytes.Equal(assertAgree(t, tests.MustApply(t, original, 1), false), original) {
					t.Errorf("%s exif=%v: restored != original", layout, includeExif)
				}
			}
		}
	})

	t.Run("agrees_with_extra_unreferenced_mdat", func(t *testing.T) {
		for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
			original := tests.MustSynth(t, func(o *synth.Options) { o.Layout, o.ExtraMdat = layout, true })
			if !bytes.Equal(assertAgree(t, tests.MustApply(t, original, 1), false), original) {
				t.Errorf("%s: restored != original", layout)
			}
		}
	})

	t.Run("agrees_with_old_v1_format_record", func(t *testing.T) {
		for _, includeIrot := range []bool{true, false} {
			original := tests.MustSynth(t, func(o *synth.Options) { o.IncludeIrot = includeIrot })
			v1Style := tests.MakeV1StyleRecord(t, original, tests.MustApply(t, original, 1))
			if !bytes.Equal(assertAgree(t, v1Style, false), original) {
				t.Errorf("fast_path=%v: restored != original", includeIrot)
			}
		}
	})

	t.Run("agrees_on_tamper_of_saved_irot_byte_fast_path", func(t *testing.T) {
		original := tests.MustSynth(t, func(o *synth.Options) { o.IncludeIrot, o.IrotValue = true, 1 })
		rotated := tests.MustApply(t, original, 1)
		for _, c := range tests.IpcoChildrenOf(t, rotated) {
			if c.Is("irot") {
				rotated[c.ContentStart] = 0xFF
				break
			}
		}
		assertAgree(t, rotated, false)
		if !bytes.Equal(assertAgree(t, rotated, true), original) {
			t.Error("restored != original")
		}
	})

	t.Run("agrees_on_tamper_elsewhere_in_iprp", func(t *testing.T) {
		for _, includeIrot := range []bool{true, false} {
			rotated := tests.MustApply(t, tests.MustSynth(t, func(o *synth.Options) { o.IncludeIrot = includeIrot }), 1)
			for _, c := range tests.IpcoChildrenOf(t, rotated) {
				if c.Is("ispe") {
					rotated[c.ContentStart] ^= 0xFF
					break
				}
			}
			assertAgree(t, rotated, false)
			assertAgree(t, rotated, true)
		}
	})

	t.Run("agrees_on_tamper_outside_restored_region", func(t *testing.T) {
		rotated := tests.MustApply(t, tests.MustSynth(t, nil), 1)
		rotated[10] ^= 0xFF // inside 'ftyp' - never touched by either function
		assertAgree(t, rotated, false)
		assertAgree(t, rotated, true)
	})

	t.Run("agrees_on_tampered_mid_stream_exif_byte", func(t *testing.T) {
		// The one kind of tamper that lands inside the region
		// VerifyReversible reads directly off the input rather than off a
		// freshly-restored buffer.
		rotated := tests.MustApply(t, midStreamExif(synth.MetaFirst), 1)
		info := tests.MustLocate(t, rotated)
		field, ok, err := core.FindExifOrientationField(rotated, info.MetaBodyStart, info.MetaHdr.ContentEnd)
		if err != nil || !ok {
			t.Fatalf("no Exif field: %v", err)
		}
		rotated[field.AbsOffset] ^= 0xFF // corrupt the live Exif Orientation byte
		assertAgree(t, rotated, false)
		assertAgree(t, rotated, true)
	})

	t.Run("avoids_full_file_size_allocation", func(t *testing.T) {
		// VerifyReversible's entire reason to exist is avoiding a second
		// full-file-size buffer; measure actual allocation rather than
		// trusting the source still looks right (upstream uses tracemalloc).
		if tests.RaceEnabled {
			t.Skip("allocation counts are inflated under -race")
		}
		payload := tests.CyclicPayload(4 << 20)
		rotated := tests.MustApply(t, tests.MustSynth(t, func(o *synth.Options) {
			o.Layout, o.MdatPayload = synth.MetaFirst, payload
		}), 1)
		if allocated := tests.AllocatedBytes(func() {
			if err := core.VerifyReversible(rotated, core.ReverseOptions{}); err != nil {
				t.Fatal(err)
			}
		}); allocated >= uint64(len(payload)/4) {
			t.Errorf("VerifyReversible allocated %d bytes for a %d-byte payload - expected a small "+
				"fraction of the file size, not scaling with it", allocated, len(payload))
		}
	})
}

// TestSharedReconstructionCode goes a step further than agreement: both
// functions must run the literal same code to reconstruct bytes.
func TestSharedReconstructionCode(t *testing.T) {
	rotated := tests.MustApply(t, tests.MustSynth(t, func(o *synth.Options) {
		o.Layout, o.IncludeExif, o.ExifPosition, o.ExifOrientation = synth.MetaFirst, true, synth.ExifMiddle, 1
		o.MdatPayload = bytes.Repeat(tests.CyclicPayload(256), 4096)
	}), 1)

	// spy wraps reconstructedChunks, recording each call's chunk sequence.
	spy := func(t *testing.T) *[][][]byte {
		var captured [][][]byte
		real := *core.ReconstructedChunks
		t.Cleanup(func() { *core.ReconstructedChunks = real })
		*core.ReconstructedChunks = func(data []byte, r *core.Reversal) iter.Seq[[]byte] {
			var chunks [][]byte
			for c := range real(data, r) {
				chunks = append(chunks, bytes.Clone(c))
			}
			captured = append(captured, chunks)
			return slices.Values(chunks)
		}
		return &captured
	}

	t.Run("both_functions_call_the_shared_helpers", func(t *testing.T) {
		// Upstream inspects bytecode; here, each function must go through
		// the reconstructedChunks seam at least once.
		captured := spy(t)
		if _, err := core.ReverseRotation(rotated, core.ReverseOptions{}); err != nil {
			t.Fatal(err)
		}
		afterReverse := len(*captured)
		if err := core.VerifyReversible(rotated, core.ReverseOptions{}); err != nil {
			t.Fatal(err)
		}
		if afterReverse == 0 || len(*captured) == afterReverse {
			t.Errorf("ReverseRotation made %d seam calls, VerifyReversible %d; both must use reconstructedChunks",
				afterReverse, len(*captured)-afterReverse)
		}
	})

	t.Run("both_consume_the_identical_chunk_sequence", func(t *testing.T) {
		captured := spy(t)
		if _, err := core.ReverseRotation(rotated, core.ReverseOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := core.VerifyReversible(rotated, core.ReverseOptions{}); err != nil {
			t.Fatal(err)
		}
		// ReverseRotation walks the chunks twice (size, then copy).
		if len(*captured) != 3 {
			t.Fatalf("captured %d chunk sequences, want 3", len(*captured))
		}
		if len((*captured)[0]) <= 2 {
			t.Error("fixture sanity check: the mid-stream Exif item should make this a multi-chunk sequence")
		}
		for i := 1; i < 3; i++ {
			if !reflect.DeepEqual((*captured)[0], (*captured)[i]) {
				t.Errorf("chunk sequence %d differs - both functions must be handed the exact same chunks, in order", i)
			}
		}
	})
}

// TestApplyRotationCopyBudget replaces upstream's
// TestReturnTypeAvoidsExtraCopy (Go has no bytes/bytearray split): output
// must not alias input, and each function makes one file-sized copy.
func TestApplyRotationCopyBudget(t *testing.T) {
	payload := tests.CyclicPayload(4 << 20)
	original := tests.MustSynth(t, func(o *synth.Options) {
		// meta_first + slow path: iloc patch AND provenance insertion, the
		// most resizing an edit does.
		o.Layout, o.MdatPayload, o.IncludeExif = synth.MetaFirst, payload, true
	})

	t.Run("apply_rotation_output_does_not_alias_input", func(t *testing.T) {
		before := bytes.Clone(original)
		rotated := tests.MustApply(t, original, 1)
		rotated[0] ^= 0xFF
		if !bytes.Equal(original, before) {
			t.Error("ApplyRotation's output aliases (or it modified) its input")
		}
	})
	if tests.RaceEnabled {
		t.Skip("allocation counts are inflated under -race")
	}
	limit := uint64(len(original)) * 3 / 2
	t.Run("apply_rotation_copies_once", func(t *testing.T) {
		if got := tests.AllocatedBytes(func() { tests.MustApply(t, original, 1) }); got > limit {
			t.Errorf("ApplyRotation allocated %d bytes for a %d-byte file (limit %d)", got, len(original), limit)
		}
	})
	t.Run("reverse_rotation_copies_once", func(t *testing.T) {
		rotated := tests.MustApply(t, original, 1)
		if got := tests.AllocatedBytes(func() { tests.MustReverse(t, rotated) }); got > limit {
			t.Errorf("ReverseRotation allocated %d bytes for a %d-byte file (limit %d)", got, len(rotated), limit)
		}
	})
}

// TestProvenanceVersionAndSelfCheck covers the TVER (tool version) entry and
// the one-time migration that grows an older on-disk record to fit a newly
// added tag. (Upstream's CLI self-check method is ported with the CLI.)
func TestProvenanceVersionAndSelfCheck(t *testing.T) {
	t.Run("tver_entry_matches_running_version", func(t *testing.T) {
		info, err := core.GatherInfo(tests.MustApply(t, tests.MustSynth(t, nil), 1))
		want, _ := core.VersionTuple()
		if err != nil || info.ToolVersion == nil || *info.ToolVersion != want {
			t.Errorf("ToolVersion = %v, %v; want %v", info.ToolVersion, err, want)
		}
	})

	t.Run("second_edit_keeps_provenance_box_same_size", func(t *testing.T) {
		original := tests.MustSynth(t, func(o *synth.Options) { o.Layout = synth.MetaFirst })
		step1 := tests.MustApply(t, original, 1)
		step2 := tests.MustApply(t, step1, 1)
		size := func(d []byte) int { h := tests.MustLocate(t, d).ProvenanceHdr; return h.ContentEnd - h.Start }
		if size(step1) != size(step2) {
			t.Errorf("same tool version, second edit: provenance box size changed (%d -> %d)", size(step1), size(step2))
		}
	})

	t.Run("rotate_migrates_legacy_record_missing_tver", func(t *testing.T) {
		// A record written before TVER existed must be grown to include it
		// on the next edit - including via `rotate 0`, which must be usable
		// purely to bring old files' metadata up to date.
		for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
			for _, deltaTurns := range []int{0, 1} {
				original := tests.MustSynth(t, func(o *synth.Options) { o.Layout = layout })
				legacy := tests.StripProvenanceTag(t, tests.MustApply(t, original, 1), "TVER")
				before, err := core.GatherInfo(legacy)
				if err != nil || before.ToolVersion != nil {
					t.Fatalf("fixture sanity check: simulated legacy record must not have TVER (%v)", err)
				}
				migrated := tests.MustApply(t, legacy, deltaTurns)
				tests.MustVerifyStructure(t, migrated)
				after, err := core.GatherInfo(migrated)
				want, _ := core.VersionTuple()
				if err != nil || after.ToolVersion == nil || *after.ToolVersion != want {
					t.Errorf("%s/%d: migrating must add TVER for the running version: %v, %v", layout, deltaTurns, after, err)
				}
				if *after.DeltaQuarterTurns != (*before.DeltaQuarterTurns+deltaTurns)%4 {
					t.Errorf("%s/%d: migration changed the cumulative rotation", layout, deltaTurns)
				}
				if !bytes.Equal(tests.MustReverse(t, migrated), original) {
					t.Errorf("%s/%d: must still reverse all the way back to the true original", layout, deltaTurns)
				}
			}
		}
	})
}

// TestProvenanceLeanFormat covers the v2 format (only the 'irot' property's
// pristine state is stored, not a whole 'iprp' snapshot), that reverse
// still handles v1 records, and that rotate migrates v1 to v2.
func TestProvenanceLeanFormat(t *testing.T) {
	t.Run("new_records_use_v2_leaner_format", func(t *testing.T) {
		for _, includeIrot := range []bool{true, false} {
			original := tests.MustSynth(t, func(o *synth.Options) { o.IncludeIrot = includeIrot })
			rotated := tests.MustApply(t, original, 1)
			version, entries, err := core.ParseProvenance(rotated, *tests.MustLocate(t, rotated).ProvenanceHdr)
			if err != nil || version != 2 {
				t.Fatalf("version = %d, %v", version, err)
			}
			oirt, hasOirt := entries["OIRT"]
			_, hasOprp := entries["OPRP"]
			_, hasOipf := entries["OIPF"]
			wantLen := 0
			if includeIrot {
				wantLen = 1
			}
			if !hasOirt || hasOprp || len(oirt) != wantLen || hasOipf == includeIrot {
				t.Errorf("fast_path=%v: entries = %v", includeIrot, entries)
			}
			if !bytes.Equal(tests.MustReverse(t, rotated), original) {
				t.Errorf("fast_path=%v: restored != original", includeIrot)
			}
		}
	})

	t.Run("v2_record_smaller_than_v1_equivalent", func(t *testing.T) {
		original := tests.MustSynth(t, nil)
		rotated := tests.MustApply(t, original, 1)
		v1 := tests.MakeV1StyleRecord(t, original, rotated)
		size := func(d []byte) int { h := tests.MustLocate(t, d).ProvenanceHdr; return h.ContentEnd - h.Start }
		if size(rotated) >= size(v1) {
			t.Errorf("v2 record (%d bytes) must be smaller than v1 (%d bytes)", size(rotated), size(v1))
		}
	})

	t.Run("reverse_supports_old_v1_records", func(t *testing.T) {
		for _, includeIrot := range []bool{true, false} {
			original := tests.MustSynth(t, func(o *synth.Options) { o.IncludeIrot = includeIrot })
			v1 := tests.MakeV1StyleRecord(t, original, tests.MustApply(t, original, 1))
			if version, _, err := core.ParseProvenance(v1, *tests.MustLocate(t, v1).ProvenanceHdr); err != nil || version != 1 {
				t.Fatalf("fixture sanity check: version = %d, %v", version, err)
			}
			if !bytes.Equal(tests.MustReverse(t, v1), original) {
				t.Errorf("fast_path=%v: restored != original", includeIrot)
			}
			if info, err := core.GatherInfo(v1); err != nil || *info.DeltaQuarterTurns != 1 {
				t.Errorf("fast_path=%v: GatherInfo = %+v, %v", includeIrot, info, err)
			}
		}
	})

	t.Run("apply_rotation_migrates_v1_record_to_v2_on_next_edit", func(t *testing.T) {
		for _, includeIrot := range []bool{true, false} {
			original := tests.MustSynth(t, func(o *synth.Options) { o.IncludeIrot = includeIrot })
			v1 := tests.MakeV1StyleRecord(t, original, tests.MustApply(t, original, 1))
			twice := tests.MustApply(t, v1, 1)
			tests.MustVerifyStructure(t, twice)
			version, entries, err := core.ParseProvenance(twice, *tests.MustLocate(t, twice).ProvenanceHdr)
			_, hasOirt := entries["OIRT"]
			_, hasOprp := entries["OPRP"]
			if err != nil || version != 2 || !hasOirt || hasOprp {
				t.Errorf("fast_path=%v: version %d entries %v err %v", includeIrot, version, entries, err)
			}
			if info, err := core.GatherInfo(twice); err != nil || *info.DeltaQuarterTurns != 2 {
				t.Errorf("fast_path=%v: GatherInfo = %+v, %v", includeIrot, info, err)
			}
			if !bytes.Equal(tests.MustReverse(t, twice), original) {
				t.Errorf("fast_path=%v: must still reverse back to the TRUE original after a v1->v2 migration", includeIrot)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// 5. mdat edge cases and scale (core-only; CLI output-path tests are ported
//    with the CLI)
// ---------------------------------------------------------------------------

// TestMdatContentIndependence: rotate/reverse must never branch on mdat's
// actual payload bytes - only on iloc's stored numbers and mdat's declared
// size.
func TestMdatContentIndependence(t *testing.T) {
	t.Run("structural_fingerprint_independent_of_mdat_payload_bytes", func(t *testing.T) {
		payloadA := make([]byte, 4096)
		payloadB := make([]byte, 4096)
		for i := range payloadB {
			payloadB[i] = byte((i*37 + 11) % 256)
		}
		for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
			for _, includeIrot := range []bool{false, true} {
				build := func(p []byte) []byte {
					return tests.MustSynth(t, func(o *synth.Options) { o.Layout, o.IncludeIrot, o.MdatPayload = layout, includeIrot, p })
				}
				originalA, originalB := build(payloadA), build(payloadB)
				rotatedA, rotatedB := tests.MustApply(t, originalA, 1), tests.MustApply(t, originalB, 1)
				if len(rotatedA) != len(rotatedB) {
					t.Fatal("same-length payloads must produce same-length output")
				}
				if !reflect.DeepEqual(structuralFingerprint(t, rotatedA), structuralFingerprint(t, rotatedB)) {
					t.Errorf("%s irot=%v: box layout/provenance (aside from CRCs) must not depend on mdat's bytes", layout, includeIrot)
				}
				tests.MustVerifyStructure(t, rotatedA)
				tests.MustVerifyStructure(t, rotatedB)
				if !bytes.Equal(tests.MustReverse(t, rotatedA), originalA) || !bytes.Equal(tests.MustReverse(t, rotatedB), originalB) {
					t.Errorf("%s irot=%v: round trip failed", layout, includeIrot)
				}
			}
		}
	})

	t.Run("mutating_mdat_payload_after_the_fact_does_not_change_edit_decisions", func(t *testing.T) {
		original := tests.MustSynth(t, nil)
		tampered := bytes.Clone(original)
		mdat := firstBox(t, tampered, "mdat")
		for i := mdat.ContentStart; i < mdat.ContentEnd; i++ {
			tampered[i] ^= 0xFF
		}
		rotatedOrig, rotatedTampered := tests.MustApply(t, original, 1), tests.MustApply(t, tampered, 1)
		if !reflect.DeepEqual(structuralFingerprint(t, rotatedOrig), structuralFingerprint(t, rotatedTampered)) {
			t.Error("fingerprints differ")
		}
		if !bytes.Equal(tests.MustReverse(t, rotatedOrig), original) || !bytes.Equal(tests.MustReverse(t, rotatedTampered), tampered) {
			t.Error("round trip failed")
		}
	})
}

// TestLargeMdatFixtures adds SIZE as a covered dimension (a few MiB).
func TestLargeMdatFixtures(t *testing.T) {
	const largeSize = 2 << 20

	t.Run("round_trip_with_large_payload", func(t *testing.T) {
		payload := tests.CyclicPayload(largeSize)
		for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
			for _, includeExif := range []bool{false, true} {
				original := tests.MustSynth(t, func(o *synth.Options) {
					o.Layout, o.MdatPayload, o.IncludeExif = layout, payload, includeExif
				})
				rotated := tests.MustApply(t, original, 1)
				tests.MustVerifyStructure(t, rotated)
				om, rm := firstBox(t, original, "mdat"), firstBox(t, rotated, "mdat")
				if om.ContentEnd-om.ContentStart != rm.ContentEnd-rm.ContentStart {
					t.Fatal("mdat length must never change")
				}
				diffs := 0
				origBytes, rotBytes := original[om.Start:om.ContentEnd], rotated[rm.Start:rm.ContentEnd]
				for i := range origBytes {
					if origBytes[i] != rotBytes[i] {
						diffs++
					}
				}
				maxAllowed := 0
				if includeExif {
					maxAllowed = 1
				}
				if diffs > maxAllowed {
					t.Errorf("%s exif=%v: %d payload bytes changed", layout, includeExif, diffs)
				}
				if !bytes.Equal(tests.MustReverse(t, rotated), original) {
					t.Errorf("%s exif=%v: reverse must reconstruct the exact original even at multi-MB size", layout, includeExif)
				}
			}
		}
	})

	t.Run("multi_edit_chain_at_large_size", func(t *testing.T) {
		payload := make([]byte, largeSize)
		for i := range payload {
			payload[i] = byte(i * 7)
		}
		original := tests.MustSynth(t, func(o *synth.Options) { o.Layout, o.MdatPayload = synth.MetaFirst, payload })
		step2 := tests.MustApply(t, tests.MustApply(t, original, 1), 1)
		if info, err := core.GatherInfo(step2); err != nil || *info.DeltaQuarterTurns != 2 {
			t.Errorf("GatherInfo = %+v, %v", info, err)
		}
		if !bytes.Equal(tests.MustReverse(t, step2), original) {
			t.Error("restored != original")
		}
	})
}

// TestMdatMidStreamExifPatch: the Exif Orientation tag can sit at an offset
// strictly inside mdat, with real payload bytes both before and after it.
func TestMdatMidStreamExifPatch(t *testing.T) {
	build := func(layout synth.Layout) []byte {
		return tests.MustSynth(t, func(o *synth.Options) {
			o.Layout, o.IncludeExif, o.ExifPosition, o.ExifOrientation = layout, true, synth.ExifMiddle, 1
			o.MdatPayload = bytes.Repeat(tests.CyclicPayload(256), 4096) // 1 MiB
		})
	}

	t.Run("exif_offset_lands_strictly_inside_mdat", func(t *testing.T) {
		for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
			original := build(layout)
			info := tests.MustLocate(t, original)
			loc, ok, err := core.FindExifItemLocation(original, info.MetaBodyStart, info.MetaHdr.ContentEnd)
			if err != nil || !ok {
				t.Fatalf("no Exif location: %v", err)
			}
			mdat := firstBox(t, original, "mdat")
			offsetInMdat := int(loc.AbsStart) - mdat.ContentStart
			if offsetInMdat <= 0 || offsetInMdat+int(loc.Length) >= mdat.ContentEnd-mdat.ContentStart {
				t.Errorf("%s: Exif blob not strictly inside mdat (offset %d)", layout, offsetInMdat)
			}
		}
	})

	t.Run("round_trip_and_sync_with_mid_stream_exif", func(t *testing.T) {
		for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
			original := build(layout)
			for targetIrot := 0; targetIrot < 4; targetIrot++ {
				rotated := tests.MustApply(t, original, targetIrot)
				tests.MustVerifyStructure(t, rotated)
				if r, err := core.GetCurrentRotation(rotated); err != nil || r != targetIrot {
					t.Errorf("GetCurrentRotation = %d, %v", r, err)
				}
				info := tests.MustLocate(t, rotated)
				field, ok, err := core.FindExifOrientationField(rotated, info.MetaBodyStart, info.MetaHdr.ContentEnd)
				if err != nil || !ok || field.Value != core.IrotToExifOrientation[targetIrot] {
					t.Errorf("%s/%d: mid-stream Exif field = %+v, %v, %v", layout, targetIrot, field, ok, err)
				}
				if !bytes.Equal(tests.MustReverse(t, rotated), original) {
					t.Errorf("%s/%d: restored != original", layout, targetIrot)
				}
			}
		}
	})
}

// TestMultipleTopLevelMdatBoxes: an extra, unreferenced top-level mdat (legal,
// if rare) must have zero effect and never be touched.
func TestMultipleTopLevelMdatBoxes(t *testing.T) {
	mdatBoxes := func(data []byte) []core.BoxHeader {
		var out []core.BoxHeader
		top, _ := core.ListBoxes(data, 0, len(data))
		for _, h := range top {
			if h.Is("mdat") {
				out = append(out, h)
			}
		}
		return out
	}

	t.Run("round_trip_unaffected_by_extra_unreferenced_mdat", func(t *testing.T) {
		for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
			original := tests.MustSynth(t, func(o *synth.Options) { o.Layout, o.ExtraMdat = layout, true })
			if n := len(mdatBoxes(original)); n != 2 {
				t.Fatalf("fixture sanity check: %d mdat boxes, want 2", n)
			}
			rotated := tests.MustApply(t, original, 1)
			tests.MustVerifyStructure(t, rotated)
			if !bytes.Equal(tests.MustReverse(t, rotated), original) {
				t.Errorf("%s: an extra, unreferenced mdat must not affect round-trip correctness", layout)
			}
		}
	})

	t.Run("extra_mdat_content_is_never_touched", func(t *testing.T) {
		original := tests.MustSynth(t, func(o *synth.Options) { o.ExtraMdat = true })
		rotated := tests.MustApply(t, original, 1)
		om, rm := mdatBoxes(original), mdatBoxes(rotated)
		if len(om) != 2 || len(rm) != 2 {
			t.Fatalf("mdat counts %d/%d, want 2/2", len(om), len(rm))
		}
		if !bytes.Equal(original[om[1].Start:om[1].ContentEnd], rotated[rm[1].Start:rm[1].ContentEnd]) {
			t.Error("the unreferenced mdat must be completely unchanged")
		}
	})
}
