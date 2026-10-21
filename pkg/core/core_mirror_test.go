package core_test

// Go-only (COMPAT E2/D8): mirroring, and rotating files with an 'imir'.
// Every edit is checked against tests.DisplayedGrid - a decoder's view of
// the file, worked out independently of core's orientation table - and
// must reverse to the exact original.

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/weisnobody/go-heic-lossless-rotate/internal/craft"
	"github.com/weisnobody/go-heic-lossless-rotate/internal/synth"
	"github.com/weisnobody/go-heic-lossless-rotate/internal/tests"
	"github.com/weisnobody/go-heic-lossless-rotate/pkg/core"
)

// edit is one step of a sequence: "H"/"V" flip, "R0".."R3" rotate.
type edit string

func (e edit) apply(data []byte) ([]byte, error) {
	switch e {
	case "H":
		return core.ApplyMirror(data, core.MirrorHorizontal, core.RotateOptions{})
	case "V":
		return core.ApplyMirror(data, core.MirrorVertical, core.RotateOptions{})
	}
	return core.ApplyRotation(data, int(e[1]-'0'), core.RotateOptions{})
}

// onScreen is what e does to the displayed image.
func (e edit) onScreen(g tests.Grid) tests.Grid {
	switch e {
	case "H":
		return g.FlipLR()
	case "V":
		return g.FlipTB()
	}
	return g.Rot(int(e[1] - '0'))
}

// sequences returns every sequence of up to n edits drawn from alphabet.
func sequences(alphabet []edit, n int) [][]edit {
	out := [][]edit{nil}
	frontier := [][]edit{nil}
	for range n {
		var next [][]edit
		for _, s := range frontier {
			for _, e := range alphabet {
				next = append(next, append(append([]edit{}, s...), e))
			}
		}
		out = append(out, next...)
		frontier = next
	}
	return out[1:]
}

// checkEdits runs seq on original, checking after every step that the
// displayed image changed as the edit says, the structure is consistent,
// the Exif Orientation (if any) was synced to the display if that changed
// and left alone if not, and the result reverses to original exactly.
func checkEdits(t *testing.T, original []byte, seq []edit) []byte {
	t.Helper()
	data := original
	want := tests.DisplayedGrid(t, original)
	_, hasExif := tests.ExifOrientation(t, original)
	for i, e := range seq {
		out, err := e.apply(data)
		if err != nil {
			t.Fatalf("step %d (%s): %v", i, e, err)
		}
		before, _ := tests.ExifOrientation(t, data)
		data = out
		prev := want
		want = e.onScreen(want)
		if got := tests.DisplayedGrid(t, data); !got.Equal(want) {
			t.Fatalf("step %d (%s): displayed %v, want %v", i, e, got, want)
		}
		tests.MustVerifyStructure(t, data)
		if hasExif {
			v, ok := tests.ExifOrientation(t, data)
			switch {
			case !ok:
				t.Fatalf("step %d (%s): Exif Orientation lost", i, e)
			case want.Equal(prev) && v != before:
				t.Fatalf("step %d (%s): display unchanged but Exif Orientation %d -> %d", i, e, before, v)
			case !want.Equal(prev) && !tests.BaseGrid().Exif(v).Equal(want):
				t.Fatalf("step %d (%s): Exif Orientation %d doesn't display as %v", i, e, v, want)
			}
		}
		if err := core.VerifyReversible(data, core.ReverseOptions{}); err != nil {
			t.Fatalf("step %d (%s): VerifyReversible: %v", i, e, err)
		}
		if back := tests.MustReverse(t, data); !bytes.Equal(back, original) {
			t.Fatalf("step %d (%s): reverse is not the original", i, e)
		}
	}
	return data
}

func TestMirrorSequences(t *testing.T) {
	seqs := sequences([]edit{"H", "V", "R1", "R3", "R0"}, 3)
	for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
		for irot := -1; irot < 4; irot++ {
			for _, exif := range []bool{false, true} {
				name := fmt.Sprintf("%s/irot=%d/exif=%v", layout, irot, exif)
				original := tests.MustSynth(t, func(o *synth.Options) {
					o.Layout, o.IncludeIrot, o.IrotValue, o.IncludeExif = layout, irot >= 0, max(irot, 0), exif
					o.ExifOrientation = core.IrotToExifOrientation[max(irot, 0)]
				})
				t.Run(name, func(t *testing.T) {
					for _, seq := range seqs {
						if !hasMirror(seq) {
							continue // rotate-only sequences are the upstream suite's
						}
						checkEdits(t, original, seq)
					}
				})
			}
		}
	}
}

func hasMirror(seq []edit) bool {
	for _, e := range seq {
		if e == "H" || e == "V" {
			return true
		}
	}
	return false
}

// TestMirrorCrafted runs a few sequences over every crafted structure the
// rotate path accepts (wide ipma, ipma first, odd iloc layouts, ...).
func TestMirrorCrafted(t *testing.T) {
	for name, opts := range craft.Variants() {
		original := craft.Build(opts)
		// Only structures rotate can reversibly edit (an extra 'iprp' child,
		// for one, is dropped by the SLOW PATH - upstream behavior the CLI's
		// self-check refuses).
		if rot, err := core.ApplyRotation(original, 1, core.RotateOptions{}); err != nil ||
			core.VerifyReversible(rot, core.ReverseOptions{}) != nil {
			continue
		}
		// A flip always inserts or removes a property; like the rotate
		// SLOW PATH, that is refused for a 64-bit 'meta' header.
		if _, err := core.ApplyMirror(original, core.MirrorHorizontal, core.RotateOptions{}); err != nil &&
			strings.Contains(err.Error(), "64-bit/extends-to-EOF") {
			continue
		}
		t.Run(name, func(t *testing.T) {
			for _, seq := range [][]edit{{"H"}, {"V", "H"}, {"H", "R1", "H"}, {"R1", "V", "R3", "V"}} {
				data := original
				for _, e := range seq {
					out, err := e.apply(data)
					if err != nil {
						t.Fatalf("%v: %s: %v", seq, e, err)
					}
					data = out
				}
				tests.MustVerifyStructure(t, data)
				if back := tests.MustReverse(t, data); !bytes.Equal(back, original) {
					t.Fatalf("%v: reverse is not the original", seq)
				}
			}
		})
	}
}

// The first flip of a file with no 'irot' inserts irot=0 then imir; the
// second flip of the same axis removes the 'imir' again.
func TestMirrorStructure(t *testing.T) {
	original := tests.MustSynth(t, nil) // no irot
	base := len(tests.IpcoChildrenOf(t, original))

	once, err := core.ApplyMirror(original, core.MirrorHorizontal, core.RotateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	kids := tests.IpcoChildrenOf(t, once)
	if len(kids) != base+2 || !kids[base].Is("irot") || !kids[base+1].Is("imir") || once[kids[base+1].ContentStart] != 1 {
		t.Fatalf("after one flip: ipco = %v", kids)
	}
	version, entries, err := core.ParseProvenance(once, *tests.MustLocate(t, once).ProvenanceHdr)
	if err != nil {
		t.Fatal(err)
	}
	if version != core.MirrorFormatVersion {
		t.Errorf("provenance version %d, want %d", version, core.MirrorFormatVersion)
	}
	for tag, want := range map[string][]byte{"OIMR": {}, "OIRT": {}, "OIPF": {0, 0, 0}} {
		if got, ok := entries[tag]; !ok || !bytes.Equal(got, want) {
			t.Errorf("%s = %x (present %v), want %x", tag, got, ok, want)
		}
	}

	twice, err := core.ApplyMirror(once, core.MirrorHorizontal, core.RotateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	kids = tests.IpcoChildrenOf(t, twice)
	if len(kids) != base+1 || !kids[base].Is("irot") {
		t.Fatalf("after two flips: ipco = %v", kids)
	}
	if v, _, _ := core.ParseProvenance(twice, *tests.MustLocate(t, twice).ProvenanceHdr); v != core.MirrorFormatVersion {
		t.Errorf("after un-flipping, provenance version %d: OIMR must stay so reverse knows", v)
	}

	// A file with 'irot' gets only the 'imir', and keeps its 'irot' value.
	withIrot := tests.MustSynth(t, func(o *synth.Options) { o.IncludeIrot, o.IrotValue = true, 1 })
	flipped, err := core.ApplyMirror(withIrot, core.MirrorVertical, core.RotateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	kids = tests.IpcoChildrenOf(t, flipped)
	last := kids[len(kids)-1]
	if len(kids) != len(tests.IpcoChildrenOf(t, withIrot))+1 || !last.Is("imir") || flipped[last.ContentStart] != 0 {
		t.Fatalf("flip with irot: ipco = %v", kids)
	}
	if rot, _ := core.GetCurrentRotation(flipped); rot != 1 {
		t.Errorf("irot = %d after a flip, want unchanged 1", rot)
	}
	_, entries, _ = core.ParseProvenance(flipped, *tests.MustLocate(t, flipped).ProvenanceHdr)
	if !bytes.Equal(entries["OIRT"], []byte{1}) || !bytes.Equal(entries["OIPF"], []byte{0, 0, 0}) {
		t.Errorf("OIRT %x, OIPF %x", entries["OIRT"], entries["OIPF"])
	}
}

// Rotate-only edits still write Python's v2 record.
func TestRotateStaysV2(t *testing.T) {
	out := tests.MustApply(t, tests.MustSynth(t, nil), 1)
	if v, _, _ := core.ParseProvenance(out, *tests.MustLocate(t, out).ProvenanceHdr); v != core.FormatVersion {
		t.Errorf("version %d", v)
	}
}

// Files with their own 'imir': rotation turns the display (D8) in either
// association order, reverse is exact, and flipping is refused.
func TestOwnImir(t *testing.T) {
	seqs := sequences([]edit{"R1", "R2", "R3", "R0"}, 2)
	for _, axis := range []byte{0, 1} {
		for irot := -1; irot < 4; irot++ {
			for _, imirFirst := range []bool{false, true} {
				name := fmt.Sprintf("imir=%d/irot=%d/imir_first=%v", axis, irot, imirFirst)
				original := tests.MustSynth(t, func(o *synth.Options) {
					o.IncludeIrot, o.IrotValue, o.IncludeExif = irot >= 0, max(irot, 0), true
					o.Imir, o.ImirFirst = &axis, imirFirst
				})
				t.Run(name, func(t *testing.T) {
					for _, seq := range seqs {
						checkEdits(t, original, seq)
					}
					for _, e := range []edit{"H", "V"} {
						_, err := e.apply(original)
						var be *core.BoxParseError
						if !errors.As(err, &be) || !strings.Contains(be.Msg, "already has its own 'imir'") {
							t.Errorf("%s on a file with its own imir: %v", e, err)
						}
						// Still refused after this tool has rotated it.
						rotated := tests.MustApply(t, original, 1)
						if _, err := e.apply(rotated); !errors.As(err, &be) {
							t.Errorf("%s after rotate: %v", e, err)
						}
					}
				})
			}
		}
	}
}

// Python's arithmetic, irot += delta, turns a mirrored display clockwise
// when 'imir' follows 'irot'; D8 solves for the right value instead.
func TestRotateMirroredD8(t *testing.T) {
	axis := byte(1)
	original := tests.MustSynth(t, func(o *synth.Options) { o.IncludeIrot, o.IrotValue, o.Imir = true, 0, &axis })
	out := tests.MustApply(t, original, 1)
	if rot, _ := core.GetCurrentRotation(out); rot != 3 {
		t.Errorf("irot = %d, want 3 (Python would write 1)", rot)
	}
}

func TestMirrorInfo(t *testing.T) {
	original := tests.MustSynth(t, nil)
	mirror := func(data []byte, axis core.MirrorAxis) []byte {
		out, err := core.ApplyMirror(data, axis, core.RotateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	info := func(data []byte) (string, int) {
		i, err := core.GatherInfo(data)
		if err != nil {
			t.Fatal(err)
		}
		return core.FormatInfo(i, false)
	}
	flipped := mirror(original, core.MirrorHorizontal)
	for _, tt := range []struct {
		name  string
		data  []byte
		lines []string
		code  int
	}{
		{"flip", flipped, []string{
			"  heic-lossless-rotate has added: horizontal flip",
			"  file's orientation: 0 deg originally -> horizontal flip now"}, 5},
		{"flip twice", mirror(flipped, core.MirrorHorizontal), []string{
			"  heic-lossless-rotate has added: 0 deg",
			"  file's orientation: 0 deg originally -> 0 deg now"}, 4},
		{"flip both ways", mirror(flipped, core.MirrorVertical), []string{
			"  heic-lossless-rotate has added: 180 deg",
			"  file's orientation: 0 deg originally -> 180 deg now"}, 2},
		{"flip then rotate", tests.MustApply(t, flipped, 1), []string{
			"  heic-lossless-rotate has added: 270 deg + horizontal flip",
			"  file's orientation: 0 deg originally -> 270 deg + horizontal flip now"}, 5},
	} {
		t.Run(tt.name, func(t *testing.T) {
			text, code := info(tt.data)
			got := strings.Split(text, "\n")
			if code != tt.code || len(got) < 3 || got[1] != tt.lines[0] || got[2] != tt.lines[1] {
				t.Errorf("FormatInfo =\n%s\nexit %d; want lines %q, exit %d", text, code, tt.lines, tt.code)
			}
			if !strings.HasPrefix(got[0], "Rotated with heic-lossless-rotate v1.7.1, provenance format v3.") {
				t.Errorf("first line %q", got[0])
			}
		})
	}

	// A file's own imir, never edited: noted as its own orientation.
	axis := byte(0)
	own := tests.MustSynth(t, func(o *synth.Options) { o.Imir = &axis })
	if text, code := info(own); code != 0 || !strings.Contains(text, "own orientation of vertical flip from elsewhere") {
		t.Errorf("own imir: %q, %d", text, code)
	}
	// ... and rotated by this tool: the delta is the on-screen turn.
	if text, code := info(tests.MustApply(t, own, 1)); code != 1 || !strings.Contains(text, "has added: 90 deg\n") {
		t.Errorf("own imir rotated: %q, %d", text, code)
	}
}

// A v3 record without OIMR is still "newer" (Python's error), and info
// still flags it.
func TestV3WithoutOimr(t *testing.T) {
	rot := tests.MustApply(t, tests.MustSynth(t, nil), 1)
	prov := tests.MustLocate(t, rot).ProvenanceHdr
	v3 := bytes.Clone(rot)
	v3[prov.ContentStart+16] = core.MirrorFormatVersion
	_, err := core.ReverseRotation(v3, core.ReverseOptions{IgnoreTamper: true})
	if err == nil || !strings.Contains(err.Error(), "format version 3 is newer than this script understands (max 2)") {
		t.Errorf("reverse v3 without OIMR: %v", err)
	}
	if i, err := core.GatherInfo(v3); err != nil || i.VersionSupported {
		t.Errorf("GatherInfo: %+v, %v", i, err)
	}
}

func TestMirrorInvalidAxis(t *testing.T) {
	if _, err := core.ApplyMirror(tests.MustSynth(t, nil), 2, core.RotateOptions{}); err == nil {
		t.Error("axis 2: want an error")
	}
}

// An 'imir' inserted at property index 128 sets the wide 'ipma' flag; after
// an un-flip removes it, reverse must still put the narrow flags back.
func TestMirrorWideFlagUnflipped(t *testing.T) {
	for _, layout := range []string{"mdat_first", "meta_first"} {
		opts := craft.Variants()[layout+"/default"]
		opts.ExtraProps, opts.Irot = 125, 1 // irot is index 127, the imir 128
		original := craft.Build(opts)
		for _, seq := range [][]edit{{"H"}, {"H", "H"}, {"H", "R1", "V", "R2"}} {
			data := checkEdits(t, original, seq)
			_, flags, _, err := ipmaOf(t, data)
			if err != nil || flags&1 == 0 {
				t.Errorf("%s %v: ipma flags %d, want the wide bit still set before reverse (%v)", layout, seq, flags, err)
			}
		}
	}
}

func ipmaOf(t *testing.T, data []byte) (uint8, uint32, []core.IpmaEntry, error) {
	t.Helper()
	s := tests.MustLocate(t, data)
	ipma, _, err := core.FindChild(data, s.IprpHdr.ContentStart, s.IprpHdr.ContentEnd, "ipma")
	if err != nil {
		return 0, 0, nil, err
	}
	return core.ParseIpma(data, ipma.ContentStart, ipma.ContentEnd)
}
