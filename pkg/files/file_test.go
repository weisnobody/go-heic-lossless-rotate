package files

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/pjg/go-heic-lossless-rotate/internal/pyfmt"
	"github.com/pjg/go-heic-lossless-rotate/internal/synth"
	"github.com/pjg/go-heic-lossless-rotate/internal/tests"
	"github.com/pjg/go-heic-lossless-rotate/pkg/core"
	"github.com/pjg/go-heic-lossless-rotate/pkg/stream"
)

func writeSynth(t *testing.T, path string, mutate func(*synth.Options)) []byte {
	t.Helper()
	data := tests.MustSynth(t, mutate)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return data
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func fileExists(path string) bool { _, err := os.Lstat(path); return err == nil }

// leftoverTemps lists any .heic-rotate-* temp files in dir.
func leftoverTemps(t *testing.T, dir string) []string {
	t.Helper()
	m, err := filepath.Glob(filepath.Join(dir, ".heic-rotate-*"))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestRotateAndReverseFile(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "photo.heic")
	original := writeSynth(t, in, func(o *synth.Options) { o.IncludeExif = true })

	var log bytes.Buffer
	res, err := RotateFile(in, "", 1, FileOptions{Log: &log})
	if err != nil {
		t.Fatal(err)
	}
	wantOut := filepath.Join(dir, "photo_rotated.heic")
	want := tests.MustApply(t, original, 1)
	if *res != (Result{Output: wantOut, Size: len(want), InputSize: len(original)}) {
		t.Errorf("Result = %+v", *res)
	}
	if !bytes.Equal(mustRead(t, wantOut), want) {
		t.Error("output differs from ApplyRotation")
	}
	// The log is exactly the CLI's stdout: the core's lines, then _run's.
	var coreLog bytes.Buffer
	core.ApplyRotation(original, 1, core.RotateOptions{Log: &coreLog})
	wantLog := coreLog.String() +
		"[ok] output box structure is internally consistent\n" +
		"[ok] reversibility self-check passed - output can be fully reversed back to the original file\n" +
		fmt.Sprintf("[ok] wrote %s (%d bytes, %+d vs input)\n", wantOut, len(want), len(want)-len(original))
	if log.String() != wantLog {
		t.Errorf("log =\n%s\nwant\n%s", log.String(), wantLog)
	}

	log.Reset()
	res, err = ReverseFile(wantOut, "", FileOptions{Log: &log})
	if err != nil {
		t.Fatal(err)
	}
	restoredPath := filepath.Join(dir, "photo_rotated_restored.heic")
	if res.Output != restoredPath || !bytes.Equal(mustRead(t, restoredPath), original) {
		t.Errorf("reverse: %+v; restored == original: %v", *res, bytes.Equal(mustRead(t, restoredPath), original))
	}
	if !strings.HasSuffix(log.String(), fmt.Sprintf("[ok] output box structure is internally consistent\n[ok] wrote %s (%d bytes, %+d vs input)\n",
		restoredPath, len(original), len(original)-len(want))) {
		t.Errorf("reverse log:\n%s", log.String())
	}

	info, err := InfoFile(wantOut)
	if err != nil || !info.HasProvenance || *info.DeltaQuarterTurns != 1 {
		t.Errorf("InfoFile = %+v, %v", info, err)
	}
	if _, err := InfoFile(filepath.Join(dir, "missing.heic")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("InfoFile(missing) err = %v", err)
	}
}

func TestOverwriteRefusalAndForce(t *testing.T) {
	dir := t.TempDir()
	in, out := filepath.Join(dir, "in.heic"), filepath.Join(dir, "out.heic")
	original := writeSynth(t, in, nil)
	os.WriteFile(out, []byte("existing"), 0o644)

	_, err := RotateFile(in, out, 1, FileOptions{})
	var oe *OutputExistsError
	if !errors.As(err, &oe) || !errors.Is(err, fs.ErrExist) ||
		err.Error() != "refusing to overwrite existing file '"+out+"' - use -f/--force to overwrite it" {
		t.Fatalf("err = %v", err)
	}
	if string(mustRead(t, out)) != "existing" {
		t.Error("refused overwrite must leave the existing file untouched")
	}
	if _, err := RotateFile(in, out, 1, FileOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mustRead(t, out), tests.MustApply(t, original, 1)) {
		t.Error("Force didn't overwrite")
	}
	// Dry run still refuses an existing output without Force, as Python does.
	if _, err := RotateFile(in, out, 1, FileOptions{DryRun: true}); !errors.Is(err, fs.ErrExist) {
		t.Errorf("dry run over existing output: err = %v", err)
	}
}

// TestSameFileInputOutput (← upstream group 5, at the file-API level).
func TestSameFileInputOutput(t *testing.T) {
	t.Run("rotate_with_output_equal_to_input", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "photo.heic")
		original := writeSynth(t, path, func(o *synth.Options) { o.Layout = synth.MetaFirst })
		if _, err := RotateFile(path, path, 1, FileOptions{Force: true}); err != nil {
			t.Fatal(err)
		}
		result := mustRead(t, path)
		if bytes.Equal(result, original) {
			t.Fatal("file unchanged")
		}
		tests.MustVerifyStructure(t, result)
		if !bytes.Equal(tests.MustReverse(t, result), original) {
			t.Error("rotating a file onto itself must still produce a fully reversible result")
		}
	})
	t.Run("reverse_with_output_equal_to_input", func(t *testing.T) {
		dir := t.TempDir()
		path, rotated := filepath.Join(dir, "photo.heic"), filepath.Join(dir, "rotated.heic")
		original := writeSynth(t, path, func(o *synth.Options) { o.Layout = synth.MetaFirst })
		if _, err := RotateFile(path, rotated, 1, FileOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := ReverseFile(rotated, rotated, FileOptions{Force: true}); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(mustRead(t, rotated), original) {
			t.Error("reversing a file onto itself must reconstruct the exact true original")
		}
	})
}

// TestDryRunSizeReport (← upstream group 5, at the file-API level).
func TestDryRunSizeReport(t *testing.T) {
	dir := t.TempDir()
	in, out := filepath.Join(dir, "input.heic"), filepath.Join(dir, "out.heic")
	original := writeSynth(t, in, func(o *synth.Options) { o.MdatPayload = tests.CyclicPayload(64 << 10) })
	expected := tests.MustApply(t, original, 1)
	var log bytes.Buffer
	res, err := RotateFile(in, out, 1, FileOptions{DryRun: true, Log: &log})
	if err != nil {
		t.Fatal(err)
	}
	if fileExists(out) {
		t.Error("dry run wrote a file")
	}
	if *res != (Result{Output: out, Size: len(expected), InputSize: len(original), DryRun: true}) {
		t.Errorf("Result = %+v", *res)
	}
	wantLine := fmt.Sprintf("[dry-run] rotate would succeed - would write %s (%d bytes, %+d vs input). No file written.\n",
		out, len(expected), len(expected)-len(original))
	if !strings.HasSuffix(log.String(), wantLine) {
		t.Errorf("log does not end with %q:\n%s", wantLine, log.String())
	}
}

// From upstream TestProvenanceVersionAndSelfCheck: the self-check now lives
// in RotateFile, which the CLI calls.
func TestRotateFileSelfCheckAbortsAndWritesNothingOnFailure(t *testing.T) {
	dir := t.TempDir()
	in, out := filepath.Join(dir, "input.heic"), filepath.Join(dir, "out.heic")
	writeSynth(t, in, nil)
	real := verifyReversibleHook
	t.Cleanup(func() { verifyReversibleHook = real })
	verifyReversibleHook = func(*stream.View) error { return &core.ProvenanceError{Msg: "simulated failure"} }

	_, err := RotateFile(in, out, 1, FileOptions{})
	var pe *core.ProvenanceError
	if !errors.As(err, &pe) || pe.Msg != "post-rotate reversibility self-check failed - refusing to write output: simulated failure" {
		t.Errorf("err = %v", err)
	}
	if fileExists(out) || len(leftoverTemps(t, dir)) > 0 {
		t.Error("rotate must not write anything if the post-rotate self-check fails")
	}
}

func TestDefaultOutputNaming(t *testing.T) {
	tests := []struct{ input, rotate, reverse string }{
		{"photo.heic", "photo_rotated.heic", "photo_restored.heic"},
		{"dir/photo.HEIC", "dir/photo_rotated.heic", "dir/photo_restored.heic"},
		{"photo_restored.heic", "photo.heic", "photo_restored_restored.heic"},
		{"photo", "photo_rotated.heic", "photo_restored.heic"},
		{"photo.", "photo_rotated.heic", "photo_restored.heic"},
		{"a.tar.heic", "a.tar_rotated.heic", "a.tar_restored.heic"},
		{"..heic", "._rotated.heic", "._restored.heic"},
		// COMPAT D1: Python gives "dir_rotated.heic" and "_rotated.heic" here.
		{"dir.v2/photo", "dir.v2/photo_rotated.heic", "dir.v2/photo_restored.heic"},
		{".hidden", ".hidden_rotated.heic", ".hidden_restored.heic"},
		{"dir/.hidden", "dir/.hidden_rotated.heic", "dir/.hidden_restored.heic"},
	}
	for _, tt := range tests {
		if got := DefaultRotateOutput(tt.input); got != tt.rotate {
			t.Errorf("DefaultRotateOutput(%q) = %q, want %q", tt.input, got, tt.rotate)
		}
		if got := DefaultReverseOutput(tt.input); got != tt.reverse {
			t.Errorf("DefaultReverseOutput(%q) = %q, want %q", tt.input, got, tt.reverse)
		}
	}
}

// ---------------------------------------------------------------------------
// D3: atomic writes that behave like Python's in-place open(path, 'wb')
// ---------------------------------------------------------------------------

// rotateOnto rotates a fresh synthetic file onto out with Force.
func rotateOnto(t *testing.T, out string) ([]byte, error) {
	t.Helper()
	in := filepath.Join(t.TempDir(), "in.heic")
	original := writeSynth(t, in, nil)
	_, err := RotateFile(in, out, 1, FileOptions{Force: true})
	return tests.MustApply(t, original, 1), err
}

func TestWriteNewFileModeMatchesPython(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	dir := t.TempDir()
	// os.WriteFile(…, 0666) is what Python's open(path, 'wb') does: the
	// process umask applies.
	ref := filepath.Join(dir, "reference")
	os.WriteFile(ref, nil, 0o666)
	out := filepath.Join(dir, "new.heic")
	if _, err := rotateOnto(t, out); err != nil {
		t.Fatal(err)
	}
	refInfo, _ := os.Stat(ref)
	outInfo, _ := os.Stat(out)
	if outInfo.Mode() != refInfo.Mode() {
		t.Errorf("new file mode %v, want %v (0666 minus umask)", outInfo.Mode(), refInfo.Mode())
	}
}

func TestWriteKeepsExistingMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	out := filepath.Join(t.TempDir(), "m640.heic")
	os.WriteFile(out, []byte("old"), 0o640)
	os.Chmod(out, 0o640)
	want, err := rotateOnto(t, out)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Stat(out); fi.Mode().Perm() != 0o640 || !bytes.Equal(mustRead(t, out), want) {
		t.Errorf("mode %v (want 0640), content updated %v", fi.Mode().Perm(), bytes.Equal(mustRead(t, out), want))
	}
}

func TestWriteReadOnlyOutputFailsLikePython(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs POSIX permissions, not root")
	}
	out := filepath.Join(t.TempDir(), "ro.heic")
	os.WriteFile(out, []byte("old"), 0o444)
	_, err := rotateOnto(t, out)
	// Python: "[Errno 13] Permission denied: 'ro.heic'"
	if want := fmt.Sprintf("[Errno %d] Permission denied: %s", int(syscall.EACCES), pyfmt.StrRepr(out)); pyfmt.OSError(err) != want {
		t.Errorf("err = %q, want %q", pyfmt.OSError(err), want)
	}
	if string(mustRead(t, out)) != "old" {
		t.Error("read-only output must be left untouched")
	}
}

func TestWriteDirectoryOutputFailsLikePython(t *testing.T) {
	out := filepath.Join(t.TempDir(), "adir")
	os.Mkdir(out, 0o755)
	_, err := rotateOnto(t, out)
	if !errors.Is(err, syscall.EISDIR) {
		t.Fatalf("err = %v, want EISDIR", err)
	}
	if runtime.GOOS != "windows" {
		if want := fmt.Sprintf("[Errno %d] Is a directory: %s", int(syscall.EISDIR), pyfmt.StrRepr(out)); pyfmt.OSError(err) != want {
			t.Errorf("err = %q, want %q", pyfmt.OSError(err), want)
		}
	}
}

func TestWriteMissingDirectoryNamesOutput(t *testing.T) {
	out := filepath.Join(t.TempDir(), "nodir", "out.heic")
	_, err := rotateOnto(t, out)
	var pe *fs.PathError
	if !errors.As(err, &pe) || pe.Path != out || !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("err = %#v, want ENOENT for %s (as Python's open names it)", err, out)
	}
}

func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

func TestWriteThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	target, link := filepath.Join(dir, "target.heic"), filepath.Join(dir, "link.heic")
	os.WriteFile(target, []byte("old"), 0o644)
	symlinkOrSkip(t, "target.heic", link)
	want, err := rotateOnto(t, link)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&fs.ModeSymlink == 0 {
		t.Error("the symlink was replaced by a regular file")
	}
	if !bytes.Equal(mustRead(t, target), want) {
		t.Error("the symlink's target wasn't rewritten")
	}
}

func TestWriteThroughDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "dangling.heic")
	symlinkOrSkip(t, "missing_target.heic", link)
	want, err := rotateOnto(t, link)
	if err != nil {
		t.Fatal(err)
	}
	if fi, _ := os.Lstat(link); fi.Mode()&fs.ModeSymlink == 0 {
		t.Error("the dangling symlink was replaced")
	}
	if !bytes.Equal(mustRead(t, filepath.Join(dir, "missing_target.heic")), want) {
		t.Error("the dangling symlink's target wasn't created")
	}
}

func TestWriteSymlinkLoop(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "loop1"), filepath.Join(dir, "loop2")
	symlinkOrSkip(t, "loop2", a)
	symlinkOrSkip(t, "loop1", b)
	if _, err := rotateOnto(t, a); !errors.Is(err, syscall.ELOOP) {
		t.Errorf("err = %v, want ELOOP", err)
	}
}

func TestWriteHardLinkedOutputInPlace(t *testing.T) {
	dir := t.TempDir()
	out, other := filepath.Join(dir, "hl.heic"), filepath.Join(dir, "hl2.heic")
	os.WriteFile(out, []byte("old"), 0o644)
	if err := os.Link(out, other); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	want, err := rotateOnto(t, out)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" { // no link-count detection: atomic replace
		return
	}
	if !bytes.Equal(mustRead(t, other), want) {
		t.Error("the other hard link doesn't see the new content (link was broken)")
	}
}

func TestWriteIsAtomicOnFailure(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.heic")
	os.WriteFile(out, []byte("original content"), 0o644)
	injected := errors.New("simulated crash before rename")
	testHookBeforeRename = func() error { return injected }
	t.Cleanup(func() { testHookBeforeRename = nil })
	if _, err := rotateOnto(t, out); !errors.Is(err, injected) {
		t.Fatalf("err = %v", err)
	}
	if string(mustRead(t, out)) != "original content" {
		t.Error("a failed write must leave the target untouched")
	}
	if temps := leftoverTemps(t, dir); len(temps) > 0 {
		t.Errorf("temp files left behind: %v", temps)
	}
}

func TestWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "out.heic")
	for range 3 { // create, then overwrite twice
		if _, err := rotateOnto(t, out); err != nil {
			t.Fatal(err)
		}
	}
	if temps := leftoverTemps(t, dir); len(temps) > 0 {
		t.Errorf("temp files left behind: %v", temps)
	}
}

func TestRotateFileSameFileHardLinked(t *testing.T) {
	// Input == output, and the target is hard-linked: the in-place write
	// must not overwrite bytes the streaming engine is still reading.
	dir := t.TempDir()
	path, other := filepath.Join(dir, "photo.heic"), filepath.Join(dir, "link.heic")
	original := writeSynth(t, path, func(o *synth.Options) {
		o.Layout, o.IncludeExif, o.MdatPayload = synth.MetaFirst, true, tests.CyclicPayload(256<<10)
	})
	if err := os.Link(path, other); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	if _, err := RotateFile(path, path, 1, FileOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	want := tests.MustApply(t, original, 1)
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
	if tests.RaceEnabled || testing.Short() {
		t.Skip("allocation counts are inflated under -race; large file")
	}
	const payload = 64 << 20
	for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
		t.Run(string(layout), func(t *testing.T) {
			dir := t.TempDir()
			in, rotated, restored := filepath.Join(dir, "in.heic"), filepath.Join(dir, "r.heic"), filepath.Join(dir, "back.heic")
			writeSynth(t, in, func(o *synth.Options) {
				o.Layout, o.IncludeExif, o.ExifPosition, o.MdatPayload = layout, true, synth.ExifMiddle, tests.CyclicPayload(payload)
			})
			start := time.Now()
			rot := tests.AllocatedBytes(func() {
				if _, err := RotateFile(in, rotated, 1, FileOptions{}); err != nil {
					t.Fatal(err)
				}
			})
			rotTime := time.Since(start)
			rev := tests.AllocatedBytes(func() {
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
