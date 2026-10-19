package heicrotate

// Port of upstream tests/test_heic_rotate.py, same classes in the same order
// (each Python TestCase becomes one Go test function with a subtest per
// method). Test groups follow upstream's numbering.

import (
	"encoding/binary"
	"testing"
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
		ilocHdr := BoxHeader{0, 8, 8, len(ilocBox), [4]byte{'i', 'l', 'o', 'c'}}

		newContent, changed, err := PatchIlocOffsetsAtThreshold(ilocBox, &ilocHdr, threshold, delta)
		if err != nil || !changed {
			t.Fatalf("PatchIlocOffsetsAtThreshold = changed %v, err %v", changed, err)
		}
		patchedBox := append(append([]byte{}, ilocBox[:8]...), newContent...)
		parsed, err := ParseIloc(patchedBox, 8, len(patchedBox))
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

		parsed, err := ParseIloc(body, 0, len(body))
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Flags != 0xABCDEF {
			t.Errorf("flags = %#x", parsed.Flags)
		}
		if raw := parsed.Items[0].RawConstrField; raw == nil || *raw != 0x1230 {
			t.Errorf("raw_constr_field = %v", raw)
		}
		rebuilt, err := BuildIlocContent(parsed)
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
		_, _, err := FindUniqueChild(data, 0, len(data), "ipma", "test")
		wantBoxParseError(t, err, "")
	})
	t.Run("single_ipma_is_fine", func(t *testing.T) {
		data := rawHeader(8, "ipma")
		if _, ok, err := FindUniqueChild(data, 0, len(data), "ipma", "test"); err != nil || !ok {
			t.Errorf("FindUniqueChild = %v, %v", ok, err)
		}
	})
	t.Run("absent_ipma_returns_none", func(t *testing.T) {
		data := rawHeader(8, "ipco")
		if _, ok, err := FindUniqueChild(data, 0, len(data), "ipma", "test"); err != nil || ok {
			t.Errorf("FindUniqueChild = %v, %v; want not found", ok, err)
		}
	})
}
