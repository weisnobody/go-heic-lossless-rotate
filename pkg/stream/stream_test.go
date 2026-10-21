package stream

// Cross-check: the streaming engine (…ReaderAt) must equal the []byte
// engine on every input - output bytes, log text, Info, and error type and
// message - and must actually stream (not fall back) for well-formed files.

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/weisnobody/go-heic-lossless-rotate/internal/craft"
	"github.com/weisnobody/go-heic-lossless-rotate/internal/synth"
	"github.com/weisnobody/go-heic-lossless-rotate/internal/tests"
	"github.com/weisnobody/go-heic-lossless-rotate/pkg/core"
)

func errString(err error) string {
	var be *core.BoxParseError
	var pe *core.ProvenanceError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &be):
		return "BoxParseError: " + be.Msg
	case errors.As(err, &pe):
		return "ProvenanceError: " + pe.Msg
	}
	return fmt.Sprintf("%T: %v", err, err)
}

type outcome struct {
	out  []byte
	log  string
	info *core.Info
	err  string
}

func viewBytes(t *testing.T, v *View) []byte {
	t.Helper()
	if v == nil {
		return nil
	}
	b, err := v.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// crossCheck runs every operation on data through both engines; it returns
// a description of the first difference ("" if none) and how many
// operations that SUCCEEDED fell back instead of streaming (an erroring
// operation always falls back, to report the []byte engine's exact error).
func crossCheck(t *testing.T, data []byte) (string, int) {
	t.Helper()
	pending, fallbacks := 0, 0
	streamFallbackHook = func(string, error) { pending++ }
	defer func() { streamFallbackHook = nil }()
	// settle attributes the fallbacks since the last call to an operation.
	settle := func(succeeded bool) {
		if succeeded {
			fallbacks += pending
		}
		pending = 0
	}
	r, n := bytes.NewReader(data), int64(len(data))

	compare := func(op string, mem, str outcome) string {
		switch {
		case mem.err != str.err:
			return fmt.Sprintf("%s: error\n  []byte %q\n  stream %q", op, mem.err, str.err)
		case mem.log != str.log:
			return fmt.Sprintf("%s: log\n  []byte %q\n  stream %q", op, mem.log, str.log)
		case !bytes.Equal(mem.out, str.out):
			return fmt.Sprintf("%s: output bytes differ (%d vs %d bytes)", op, len(mem.out), len(str.out))
		case !reflect.DeepEqual(mem.info, str.info):
			return fmt.Sprintf("%s: info\n  []byte %+v\n  stream %+v", op, mem.info, str.info)
		}
		return ""
	}
	for turns := range 4 {
		for _, sync := range []bool{true, false} {
			var ml, sl bytes.Buffer
			mo, merr := core.ApplyRotation(data, turns, core.RotateOptions{NoExifSync: !sync, Log: &ml})
			v, serr := ApplyRotationReaderAt(r, n, turns, core.RotateOptions{NoExifSync: !sync, Log: &sl})
			settle(merr == nil)
			op := fmt.Sprintf("apply(%d, sync=%v)", turns, sync)
			if d := compare(op, outcome{out: mo, log: ml.String(), err: errString(merr)},
				outcome{out: viewBytes(t, v), log: sl.String(), err: errString(serr)}); d != "" {
				return d, fallbacks
			}
			if v != nil {
				a, b := errString(core.VerifyStructure(mo)), errString(VerifyStructureView(v))
				settle(a == "")
				if a != b {
					return fmt.Sprintf("%s: VerifyStructure %q vs %q", op, a, b), fallbacks
				}
			}
		}
	}
	for _, axis := range []core.MirrorAxis{core.MirrorHorizontal, core.MirrorVertical} {
		for _, sync := range []bool{true, false} {
			var ml, sl bytes.Buffer
			mo, merr := core.ApplyMirror(data, axis, core.RotateOptions{NoExifSync: !sync, Log: &ml})
			v, serr := ApplyMirrorReaderAt(r, n, axis, core.RotateOptions{NoExifSync: !sync, Log: &sl})
			settle(merr == nil)
			op := fmt.Sprintf("mirror(%s, sync=%v)", axis, sync)
			if d := compare(op, outcome{out: mo, log: ml.String(), err: errString(merr)},
				outcome{out: viewBytes(t, v), log: sl.String(), err: errString(serr)}); d != "" {
				return d, fallbacks
			}
			if v != nil {
				a, b := errString(core.VerifyStructure(mo)), errString(VerifyStructureView(v))
				settle(a == "")
				if a != b {
					return fmt.Sprintf("%s: VerifyStructure %q vs %q", op, a, b), fallbacks
				}
			}
		}
	}
	for _, force := range []bool{false, true} {
		var ml, sl bytes.Buffer
		mo, merr := core.ReverseRotation(data, core.ReverseOptions{IgnoreTamper: force, Log: &ml})
		v, serr := ReverseRotationReaderAt(r, n, core.ReverseOptions{IgnoreTamper: force, Log: &sl})
		settle(merr == nil)
		if d := compare(fmt.Sprintf("reverse(force=%v)", force), outcome{out: mo, log: ml.String(), err: errString(merr)},
			outcome{out: viewBytes(t, v), log: sl.String(), err: errString(serr)}); d != "" {
			return d, fallbacks
		}
		ml.Reset()
		sl.Reset()
		merr = core.VerifyReversible(data, core.ReverseOptions{IgnoreTamper: force, Log: &ml})
		serr = VerifyReversibleReaderAt(r, n, core.ReverseOptions{IgnoreTamper: force, Log: &sl})
		settle(merr == nil)
		if d := compare(fmt.Sprintf("verify(force=%v)", force), outcome{log: ml.String(), err: errString(merr)},
			outcome{log: sl.String(), err: errString(serr)}); d != "" {
			return d, fallbacks
		}
	}
	mi, merr := core.GatherInfo(data)
	si, serr := GatherInfoReaderAt(r, n)
	settle(merr == nil)
	if d := compare("info", outcome{info: mi, err: errString(merr)}, outcome{info: si, err: errString(serr)}); d != "" {
		return d, fallbacks
	}
	return "", fallbacks
}

type crossGroup struct {
	name       string
	inputs     map[string][]byte
	mustStream bool // well-formed: zero fallbacks required
}

func crossCorpus(t *testing.T) []crossGroup {
	synthIn, rotated, twice, legacy, tamper := map[string][]byte{}, map[string][]byte{}, map[string][]byte{}, map[string][]byte{}, map[string][]byte{}
	mirrored, ownImir := map[string][]byte{}, map[string][]byte{}
	for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
		for _, irot := range []int{-1, 0, 1, 2, 3} {
			for _, exif := range []string{"none", "end", "middle"} {
				for _, extra := range []bool{false, true} {
					name := fmt.Sprintf("%s/irot=%d/exif=%s/extra=%v", layout, irot, exif, extra)
					d := tests.MustSynth(t, func(o *synth.Options) {
						o.Layout, o.ExtraMdat = layout, extra
						o.IncludeIrot, o.IrotValue = irot >= 0, max(irot, 0)
						if exif != "none" {
							o.IncludeExif, o.ExifOrientation, o.ExifPosition = true, 6, synth.ExifPosition(exif)
						}
					})
					synthIn[name] = d
					m1 := mustMirror(t, d, core.MirrorHorizontal)
					mirrored[name] = m1
					mirrored[name+"/rot"] = tests.MustApply(t, m1, 1)
					mirrored[name+"/unflipped"] = mustMirror(t, m1, core.MirrorHorizontal)
					axis := byte(irot & 1)
					ownImir[name] = tests.MustSynth(t, func(o *synth.Options) {
						o.Layout, o.ExtraMdat, o.Imir, o.ImirFirst = layout, extra, &axis, extra
						o.IncludeIrot, o.IrotValue = irot >= 0, max(irot, 0)
						if exif != "none" {
							o.IncludeExif, o.ExifOrientation, o.ExifPosition = true, 6, synth.ExifPosition(exif)
						}
					})
					r1 := tests.MustApply(t, d, 1)
					rotated[name] = r1
					twice[name] = tests.MustApply(t, r1, 3)
					legacy[name+"/no-tver"] = tests.StripProvenanceTag(t, r1, "TVER")
					if layout == synth.MdatFirst {
						legacy[name+"/v1"] = tests.MakeV1StyleRecord(t, d, r1)
					}
					tf := bytes.Clone(r1)
					tf[10] ^= 0xFF
					tamper[name+"/ftyp"] = tf
					for i, c := range tests.IpcoChildrenOf(t, r1) {
						tc := bytes.Clone(r1)
						tc[c.ContentStart] ^= 0xFF
						tamper[fmt.Sprintf("%s/ipco%d", name, i)] = tc
					}
				}
			}
		}
	}
	large := map[string][]byte{}
	for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
		d := tests.MustSynth(t, func(o *synth.Options) {
			o.Layout, o.MdatPayload, o.IncludeExif, o.ExifPosition = layout, tests.CyclicPayload(2<<20), true, synth.ExifMiddle
		})
		large[string(layout)] = d
		large[string(layout)+"/rotated"] = tests.MustApply(t, d, 1)
	}
	crafted := map[string][]byte{}
	for name, o := range craft.Variants() {
		d := craft.Build(o)
		crafted[name] = d
		if r, err := core.ApplyRotation(d, 1, core.RotateOptions{}); err == nil {
			crafted[name+"/rotated"] = r
		}
		if m, err := core.ApplyMirror(d, core.MirrorVertical, core.RotateOptions{}); err == nil {
			crafted[name+"/mirrored"] = m
		}
	}
	corrupt := map[string][]byte{}
	for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
		for _, irot := range []bool{false, true} {
			d := tests.MustSynth(t, func(o *synth.Options) {
				o.Layout, o.IncludeIrot, o.IncludeExif, o.ExifPosition = layout, irot, true, synth.ExifMiddle
			})
			for _, base := range [][]byte{d, tests.MustApply(t, d, 1), mustMirror(t, d, core.MirrorHorizontal)} {
				for i := range base {
					for _, m := range []byte{0xFF, 0x01} {
						b := bytes.Clone(base)
						b[i] ^= m
						corrupt[fmt.Sprintf("%s/irot=%v/len%d/byte%d^%x", layout, irot, len(base), i, m)] = b
					}
				}
				for n := range len(base) {
					corrupt[fmt.Sprintf("%s/irot=%v/len%d/prefix%d", layout, irot, len(base), n)] = base[:n]
				}
			}
		}
	}
	return []crossGroup{
		{"synth originals", synthIn, true},
		{"synth rotated", rotated, true},
		{"synth rotated twice", twice, true},
		{"synth mirrored", mirrored, true},
		{"synth own imir", ownImir, true},
		{"legacy records", legacy, true},
		{"tamper", tamper, true},
		{"large", large, true},
		{"crafted", crafted, false},
		{"corruption", corrupt, false},
	}
}

func TestStreamMatchesBytesEngine(t *testing.T) {
	if testing.Short() {
		t.Skip("large corpus")
	}
	for _, g := range crossCorpus(t) {
		t.Run(strings.ReplaceAll(g.name, " ", "_"), func(t *testing.T) {
			names := make([]string, 0, len(g.inputs))
			for name := range g.inputs {
				names = append(names, name)
			}
			sort.Strings(names)
			totalFallbacks, streamed := 0, 0
			for _, name := range names {
				diff, fallbacks := crossCheck(t, g.inputs[name])
				if diff != "" {
					t.Fatalf("%s: %s", name, diff)
				}
				if g.mustStream && fallbacks > 0 {
					t.Errorf("%s: %d successful operations fell back on a well-formed file", name, fallbacks)
				}
				totalFallbacks += fallbacks
				if fallbacks == 0 {
					streamed++
				}
			}
			t.Logf("%d inputs; %d streamed every successful operation; %d successful operations fell back", len(names), streamed, totalFallbacks)
		})
	}
}

func mustMirror(t *testing.T, data []byte, axis core.MirrorAxis) []byte {
	t.Helper()
	out, err := core.ApplyMirror(data, axis, core.RotateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return out
}
