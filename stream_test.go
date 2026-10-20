package heicrotate

// Cross-check: the streaming engine (…ReaderAt) must equal the []byte
// engine on every input - output bytes, log text, Info, and error type and
// message - and must actually stream (not fall back) for well-formed files.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pjg/go-heic-lossless-rotate/internal/craft"
	"github.com/pjg/go-heic-lossless-rotate/internal/synth"
)

func errString(err error) string {
	var be *BoxParseError
	var pe *ProvenanceError
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
	info *Info
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
			mo, merr := ApplyRotation(data, turns, RotateOptions{NoExifSync: !sync, Log: &ml})
			v, serr := ApplyRotationReaderAt(r, n, turns, RotateOptions{NoExifSync: !sync, Log: &sl})
			settle(merr == nil)
			op := fmt.Sprintf("apply(%d, sync=%v)", turns, sync)
			if d := compare(op, outcome{out: mo, log: ml.String(), err: errString(merr)},
				outcome{out: viewBytes(t, v), log: sl.String(), err: errString(serr)}); d != "" {
				return d, fallbacks
			}
			if v != nil {
				a, b := errString(VerifyStructure(mo)), errString(verifyStructureView(v))
				settle(a == "")
				if a != b {
					return fmt.Sprintf("%s: VerifyStructure %q vs %q", op, a, b), fallbacks
				}
			}
		}
	}
	for _, force := range []bool{false, true} {
		var ml, sl bytes.Buffer
		mo, merr := ReverseRotation(data, ReverseOptions{IgnoreTamper: force, Log: &ml})
		v, serr := ReverseRotationReaderAt(r, n, ReverseOptions{IgnoreTamper: force, Log: &sl})
		settle(merr == nil)
		if d := compare(fmt.Sprintf("reverse(force=%v)", force), outcome{out: mo, log: ml.String(), err: errString(merr)},
			outcome{out: viewBytes(t, v), log: sl.String(), err: errString(serr)}); d != "" {
			return d, fallbacks
		}
		ml.Reset()
		sl.Reset()
		merr = VerifyReversible(data, ReverseOptions{IgnoreTamper: force, Log: &ml})
		serr = VerifyReversibleReaderAt(r, n, ReverseOptions{IgnoreTamper: force, Log: &sl})
		settle(merr == nil)
		if d := compare(fmt.Sprintf("verify(force=%v)", force), outcome{log: ml.String(), err: errString(merr)},
			outcome{log: sl.String(), err: errString(serr)}); d != "" {
			return d, fallbacks
		}
	}
	mi, merr := GatherInfo(data)
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
	for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
		for _, irot := range []int{-1, 0, 1, 2, 3} {
			for _, exif := range []string{"none", "end", "middle"} {
				for _, extra := range []bool{false, true} {
					name := fmt.Sprintf("%s/irot=%d/exif=%s/extra=%v", layout, irot, exif, extra)
					d := mustSynth(t, func(o *synth.Options) {
						o.Layout, o.ExtraMdat = layout, extra
						o.IncludeIrot, o.IrotValue = irot >= 0, max(irot, 0)
						if exif != "none" {
							o.IncludeExif, o.ExifOrientation, o.ExifPosition = true, 6, synth.ExifPosition(exif)
						}
					})
					synthIn[name] = d
					r1 := mustApply(t, d, 1)
					rotated[name] = r1
					twice[name] = mustApply(t, r1, 3)
					legacy[name+"/no-tver"] = stripProvenanceTag(t, r1, "TVER")
					if layout == synth.MdatFirst {
						legacy[name+"/v1"] = makeV1StyleRecord(t, d, r1)
					}
					tf := bytes.Clone(r1)
					tf[10] ^= 0xFF
					tamper[name+"/ftyp"] = tf
					for i, c := range ipcoChildrenOf(t, r1) {
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
		d := mustSynth(t, func(o *synth.Options) {
			o.Layout, o.MdatPayload, o.IncludeExif, o.ExifPosition = layout, cyclicPayload(2<<20), true, synth.ExifMiddle
		})
		large[string(layout)] = d
		large[string(layout)+"/rotated"] = mustApply(t, d, 1)
	}
	crafted := map[string][]byte{}
	for name, o := range craft.Variants() {
		d := craft.Build(o)
		crafted[name] = d
		if r, err := ApplyRotation(d, 1, RotateOptions{}); err == nil {
			crafted[name+"/rotated"] = r
		}
	}
	corrupt := map[string][]byte{}
	for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
		for _, irot := range []bool{false, true} {
			d := mustSynth(t, func(o *synth.Options) {
				o.Layout, o.IncludeIrot, o.IncludeExif, o.ExifPosition = layout, irot, true, synth.ExifMiddle
			})
			for _, base := range [][]byte{d, mustApply(t, d, 1)} {
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

func TestRotateFileSameFileHardLinked(t *testing.T) {
	// Input == output, and the target is hard-linked: the in-place write
	// must not overwrite bytes the streaming engine is still reading.
	dir := t.TempDir()
	path, other := filepath.Join(dir, "photo.heic"), filepath.Join(dir, "link.heic")
	original := writeSynth(t, path, func(o *synth.Options) {
		o.Layout, o.IncludeExif, o.MdatPayload = synth.MetaFirst, true, cyclicPayload(256<<10)
	})
	if err := os.Link(path, other); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, err := RotateFile(path, path, 1, FileOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	want := mustApply(t, original, 1)
	if !bytes.Equal(mustRead(t, path), want) || !bytes.Equal(mustRead(t, other), want) {
		t.Error("in-place rotate of a hard-linked file corrupted it or broke the link")
	}
	if _, err := ReverseFile(path, path, FileOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustRead(t, other), original) {
		t.Error("in-place reverse of a hard-linked file didn't restore the original")
	}
}

func TestFileAPIHeapBound(t *testing.T) {
	if raceEnabled || testing.Short() {
		t.Skip("allocation counts are inflated under -race; large file")
	}
	const payload = 64 << 20
	for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
		t.Run(string(layout), func(t *testing.T) {
			dir := t.TempDir()
			in, rotated, restored := filepath.Join(dir, "in.heic"), filepath.Join(dir, "r.heic"), filepath.Join(dir, "back.heic")
			writeSynth(t, in, func(o *synth.Options) {
				o.Layout, o.IncludeExif, o.ExifPosition, o.MdatPayload = layout, true, synth.ExifMiddle, cyclicPayload(payload)
			})
			start := time.Now()
			rot := allocatedBytes(func() {
				if _, err := RotateFile(in, rotated, 1, FileOptions{}); err != nil {
					t.Fatal(err)
				}
			})
			rotTime := time.Since(start)
			rev := allocatedBytes(func() {
				if _, err := ReverseFile(rotated, restored, FileOptions{}); err != nil {
					t.Fatal(err)
				}
			})
			t.Logf("64 MiB: rotate allocated %d bytes in %v, reverse %d bytes", rot, rotTime, rev)
			if rot > 2<<20 || rev > 2<<20 {
				t.Errorf("heap growth over 2 MiB: rotate %d, reverse %d", rot, rev)
			}
			if !bytes.Equal(mustRead(t, in), mustRead(t, restored)) {
				t.Error("round trip through the streaming file API isn't byte-identical")
			}
		})
	}
}
