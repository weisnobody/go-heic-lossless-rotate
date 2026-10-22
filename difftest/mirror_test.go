//go:build difftest

package difftest

// Go-only features against the real Python tool (COMPAT E2, D8):
//   - Python refuses to rotate or reverse a Go-mirrored file (a v3 record),
//     cleanly and without writing anything, and its info still reports it;
//   - on a file with its own 'imir' after its 'irot', Python's rotate turns
//     the display the wrong way, which is the divergence D8 documents.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weisnobody/go-heic-lossless-rotate/internal/synth"
	"github.com/weisnobody/go-heic-lossless-rotate/pkg/core"
)

func TestPythonRefusesMirrored(t *testing.T) {
	py := Upstream(t)
	for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
		for _, irot := range []bool{false, true} {
			o := synth.DefaultOptions()
			o.Layout, o.IncludeIrot, o.IrotValue, o.IncludeExif = layout, irot, 1, true
			original, err := synth.BuildSyntheticHEIC(o)
			if err != nil {
				t.Fatal(err)
			}
			once, err := core.ApplyMirror(original, core.MirrorHorizontal, core.RotateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			unflipped, err := core.ApplyMirror(once, core.MirrorHorizontal, core.RotateOptions{})
			if err != nil {
				t.Fatal(err)
			}
			for name, data := range map[string][]byte{"mirrored": once, "unflipped": unflipped} {
				dir := t.TempDir()
				writeFile(t, filepath.Join(dir, "x.heic"), data)
				for _, argv := range [][]string{{"reverse", "x.heic", "y.heic"}, {"rotate", "90", "x.heic", "y.heic"}} {
					r := py.CLI(t, dir, argv...)
					if r.Code == 0 || !strings.Contains(string(r.Stderr), "format version 3 is newer than this script understands") {
						t.Errorf("%s/irot=%v/%s: python %q: exit %d, stderr %q", layout, irot, name, argv, r.Code, r.Stderr)
					}
					if _, err := os.Stat(filepath.Join(dir, "y.heic")); err == nil {
						t.Errorf("%s/irot=%v/%s: python %q wrote output", layout, irot, name, argv)
					}
				}
				r := py.CLI(t, dir, "info", "x.heic")
				if r.Code < 1 || r.Code > 4 || !strings.Contains(string(r.Stdout), "provenance format v3 (NEWER") {
					t.Errorf("%s/irot=%v/%s: python info: exit %d\n%s%s", layout, irot, name, r.Code, r.Stdout, r.Stderr)
				}
			}
		}
	}
}

func TestPythonRotatesMirroredFileTheWrongWay(t *testing.T) {
	py := Upstream(t)
	axis := byte(1)
	o := synth.DefaultOptions()
	o.IncludeIrot, o.IrotValue, o.Imir = true, 0, &axis // irot, then imir
	original, err := synth.BuildSyntheticHEIC(o)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "x.heic"), original)
	if r := py.CLI(t, dir, "rotate", "90", "x.heic", "y.heic"); r.Code != 0 {
		t.Fatalf("python rotate: exit %d %s", r.Code, r.Stderr)
	}
	pyOut, err := os.ReadFile(filepath.Join(dir, "y.heic"))
	if err != nil {
		t.Fatal(err)
	}
	goOut, err := core.ApplyRotation(original, 1, core.RotateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pyTurns, _ := core.GetCurrentRotation(pyOut)
	goTurns, _ := core.GetCurrentRotation(goOut)
	// A left-right flip after irot=1 displays as a 90 deg CLOCKWISE turn of
	// the flipped image; Go writes irot=3, which displays counter-clockwise.
	if pyTurns != 1 || goTurns != 3 {
		t.Errorf("irot after rotate 90: python %d (want 1), go %d (want 3)", pyTurns, goTurns)
	}
}
