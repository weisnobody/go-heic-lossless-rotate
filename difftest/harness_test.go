//go:build difftest

package difftest

import (
	"fmt"
	"strings"
	"testing"

	heicrotate "github.com/pjg/go-heic-lossless-rotate"
)

func TestUpstreamPinned(t *testing.T) {
	py := Upstream(t) // fails unless commit and VERSION match the pin
	t.Logf("upstream %s at %s, Python %s (%s)", py.Src, py.Commit, py.Version, py.Python)
}

func TestPythonCLISmoke(t *testing.T) {
	py := Upstream(t)
	r := py.CLI(t, t.TempDir(), "-V")
	if r.Code != 0 {
		t.Fatalf("python -V exited %d\nstderr: %s", r.Code, r.Stderr)
	}
	first, _, _ := strings.Cut(string(r.Stdout), "\n")
	if want := "heic-lossless-rotate " + heicrotate.Version; first != want {
		t.Errorf("python -V first line = %q, want %q", first, want)
	}
}

func TestDriverRejectsUnknownOp(t *testing.T) {
	// The driver must abort on an op it doesn't know rather than record a
	// per-request error a careless comparison could treat as "both failed".
	py := Upstream(t)
	ft := &fatalRecorder{TB: t}
	func() {
		defer func() { _ = recover() }() // fatalRecorder panics to stop like t.Fatal
		py.Drive(ft, nil, []Request{{ID: "x", Op: "no_such_op"}})
	}()
	if !ft.failed || !strings.Contains(ft.msg, "unknown op(s): no_such_op") {
		t.Errorf("Drive with unknown op: failed=%v msg=%q", ft.failed, ft.msg)
	}
}

// fatalRecorder captures a Fatalf instead of failing the real test.
type fatalRecorder struct {
	testing.TB
	failed bool
	msg    string
}

func (f *fatalRecorder) Fatalf(format string, args ...any) {
	f.failed = true
	f.msg = fmt.Sprintf(format, args...)
	panic("fatal")
}

func TestDescribeDiff(t *testing.T) {
	// A comparison that can't fail proves nothing: check the reporter
	// actually pinpoints differences.
	tests := []struct {
		name      string
		want, got []byte
		contains  []string
	}{
		{"middle byte", []byte("abcdef"), []byte("abcXef"),
			[]string{"first difference at offset 3", "python len 6, go len 6", "61 62 63 64 65 66", "61 62 63 58 65 66"}},
		{"go shorter", []byte("abcdef"), []byte("abc"),
			[]string{"offset 3", "python len 6, go len 3"}},
		{"go longer", []byte("ab"), []byte("abcd"),
			[]string{"offset 2", "python len 2, go len 4", "python [0:2] 61 62"}},
		{"empty python", nil, []byte("a"), []string{"offset 0", "python (none)"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeDiff(tt.want, tt.got)
			for _, s := range tt.contains {
				if !strings.Contains(got, s) {
					t.Errorf("describeDiff = %q, missing %q", got, s)
				}
			}
		})
	}
}
