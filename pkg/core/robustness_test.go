package core_test

// Go-only robustness sweep (no Python needed): wherever upstream crashes
// (IndexError, struct.error, KeyError, ...) or hangs, the port must return
// a typed error - never panic, never return another error type, never spin
// (docs/COMPAT.md D2, D4-D7). The differential suite separately checks that
// every typed result matches Python where Python doesn't crash.

import (
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/weisnobody/go-heic-lossless-rotate/internal/synth"
	"github.com/weisnobody/go-heic-lossless-rotate/internal/tests"
	"github.com/weisnobody/go-heic-lossless-rotate/pkg/core"
)

// robustnessFixtures: both layouts x fast/slow path x original/rotated, all
// with Exif (so the Exif sync and restore paths are reachable), plus a v1
// record and a twice-rotated file.
func robustnessFixtures(t *testing.T) map[string][]byte {
	fixtures := map[string][]byte{}
	for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
		for _, irot := range []bool{false, true} {
			base := tests.MustSynth(t, func(o *synth.Options) {
				o.Layout, o.IncludeIrot, o.IncludeExif, o.ExifPosition = layout, irot, true, synth.ExifMiddle
			})
			name := fmt.Sprintf("%s/irot=%v", layout, irot)
			fixtures[name] = base
			fixtures[name+"/rotated"] = tests.MustApply(t, base, 1)
		}
	}
	plain := tests.MustSynth(t, func(o *synth.Options) { o.IncludeExif = true })
	fixtures["v1record"] = tests.MakeV1StyleRecord(t, plain, tests.MustApply(t, plain, 1))
	fixtures["twice"] = tests.MustApply(t, tests.MustApply(t, plain, 1), 3)
	return fixtures
}

// entryPoints runs every exported operation on data, returning each error.
func entryPoints(data []byte) []error {
	var errs []error
	for turn := 0; turn < 4; turn++ {
		_, err := core.ApplyRotation(data, turn, core.RotateOptions{NoExifSync: turn == 2})
		errs = append(errs, err)
	}
	for _, force := range []bool{false, true} {
		_, err := core.ReverseRotation(data, core.ReverseOptions{IgnoreTamper: force})
		errs = append(errs, err, core.VerifyReversible(data, core.ReverseOptions{IgnoreTamper: force}))
	}
	info, err := core.GatherInfo(data)
	if err == nil {
		core.FormatInfo(info, false)
	}
	_, err2 := core.GetCurrentRotation(data)
	_, err3 := core.LocateStructures(data)
	return append(errs, err, err2, err3, core.VerifyStructure(data))
}

// checkNoPanic runs entryPoints on data and fails on a panic, an untyped
// error, or a call that doesn't finish within a generous deadline.
func checkNoPanic(t *testing.T, label string, data []byte) {
	t.Helper()
	type result struct {
		errs  []error
		panic any
	}
	done := make(chan result, 1)
	go func() {
		defer func() {
			if p := recover(); p != nil {
				done <- result{panic: p}
			}
		}()
		done <- result{errs: entryPoints(data)}
	}()
	select {
	case r := <-done:
		if r.panic != nil {
			t.Fatalf("%s: panic: %v", label, r.panic)
		}
		for _, err := range r.errs {
			var be *core.BoxParseError
			var pe *core.ProvenanceError
			if err != nil && !errors.As(err, &be) && !errors.As(err, &pe) {
				t.Fatalf("%s: untyped error %T: %v", label, err, err)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("%s: did not finish within 5s (a hang where Python would loop forever?)", label)
	}
}

func TestNoPanicOnTruncation(t *testing.T) {
	for name, data := range robustnessFixtures(t) {
		t.Run(name, func(t *testing.T) {
			for n := range len(data) {
				checkNoPanic(t, fmt.Sprintf("prefix %d", n), data[:n])
			}
		})
	}
}

func TestNoPanicOnByteCorruption(t *testing.T) {
	masks := []byte{0xFF, 0x01, 0x80}
	if testing.Short() {
		masks = masks[:1]
	}
	for name, data := range robustnessFixtures(t) {
		t.Run(name, func(t *testing.T) {
			bad := make([]byte, len(data))
			for i := range data {
				for _, m := range masks {
					copy(bad, data)
					bad[i] ^= m
					checkNoPanic(t, fmt.Sprintf("byte %d ^ %#x", i, m), bad)
				}
			}
		})
	}
}

func TestNoPanicOnRandomCorruption(t *testing.T) {
	// Deterministic multi-byte corruption, biased toward the header region
	// (where all the parsing happens) and toward boundary values.
	rounds := 400
	if testing.Short() {
		rounds = 50
	}
	interesting := []byte{0x00, 0x01, 0x02, 0x07, 0x08, 0x0F, 0x10, 0x7F, 0x80, 0xFE, 0xFF}
	for name, data := range robustnessFixtures(t) {
		t.Run(name, func(t *testing.T) {
			rng := rand.New(rand.NewPCG(1, uint64(len(data))))
			bad := make([]byte, len(data))
			for round := range rounds {
				copy(bad, data)
				for range 1 + rng.IntN(4) {
					i := rng.IntN(len(data))
					if rng.IntN(2) == 0 {
						bad[i] = interesting[rng.IntN(len(interesting))]
					} else {
						bad[i] = byte(rng.Uint32())
					}
				}
				checkNoPanic(t, fmt.Sprintf("round %d", round), bad)
			}
		})
	}
}

func TestNoPanicOnCraftedEdgeCases(t *testing.T) {
	zeroSize64 := cat(rawHeader(1, "free"), make([]byte, 8))
	cases := map[string][]byte{
		"empty":                    nil,
		"zero-size 64-bit top box": cat(box("ftyp", nil), zeroSize64, make([]byte, 8)),
		"zero-size 64-bit in meta": cat(box("ftyp", nil), box("meta", cat(make([]byte, 4), zeroSize64))),
		"meta with no body":        cat(box("ftyp", nil), box("meta", nil)),
		"size smaller than header": cat(rawHeader(3, "meta"), make([]byte, 16)),
		"extends-to-EOF meta":      cat(box("ftyp", nil), rawHeader(0, "meta"), make([]byte, 32)),
		"64-bit meta header":       cat(box("ftyp", nil), rawHeader(1, "meta"), []byte{0, 0, 0, 0, 0, 0, 0, 24}, make([]byte, 8)),
		"provenance-only uuid box": core.MakeBox("uuid", core.ProvenanceUUID[:]),
		"uuid before meta":         cat(box("ftyp", nil), core.MakeBox("uuid", append(core.ProvenanceUUID[:], 2, 0)), box("meta", make([]byte, 4))),
		"huge 64-bit size":         cat(rawHeader(1, "mdat"), []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}),
		"many tiny boxes":          cat(box("ftyp", nil), box("meta", cat(make([]byte, 4), bytesRepeatBox(10000)))),
	}
	for name, data := range cases {
		checkNoPanic(t, name, data)
	}
}

func bytesRepeatBox(n int) []byte {
	out := make([]byte, 0, 8*n)
	for range n {
		out = append(out, rawHeader(8, "free")...)
	}
	return out
}
