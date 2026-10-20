package files

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/weisnobody/go-heic-lossless-rotate/internal/synth"
	"github.com/weisnobody/go-heic-lossless-rotate/internal/tests"
	"github.com/weisnobody/go-heic-lossless-rotate/pkg/core"
)

// TestConcurrentUse runs RotateFile, ReverseFile, and InfoFile from many
// goroutines at once on one shared input, each writing its own output (run
// it with -race). Every result, log line, and output file must match a
// sequential run.
func TestConcurrentUse(t *testing.T) {
	dir := t.TempDir()
	in := filepath.Join(dir, "photo.heic")
	original := writeSynth(t, in, func(o *synth.Options) {
		o.Layout, o.IncludeExif, o.ExifPosition, o.MdatPayload = synth.MetaFirst, true, synth.ExifMiddle, tests.CyclicPayload(256<<10)
	})
	rotated := tests.MustApply(t, original, 1)
	wantInfo, err := core.GatherInfo(rotated)
	if err != nil {
		t.Fatal(err)
	}
	// The log a sequential run writes, with its output path as a
	// placeholder.
	ref := filepath.Join(dir, "reference.heic")
	var refLog strings.Builder
	if _, err := RotateFile(in, ref, 1, FileOptions{Log: &refLog}); err != nil {
		t.Fatal(err)
	}

	tests.Parallel(tests.ConcurrentWorkers, func(w int) {
		out := filepath.Join(dir, fmt.Sprintf("rotated%d.heic", w))
		var log strings.Builder
		res, err := RotateFile(in, out, 1, FileOptions{Log: &log})
		if err != nil {
			t.Errorf("worker %d: RotateFile: %v", w, err)
			return
		}
		wantLog := strings.ReplaceAll(refLog.String(), ref, out)
		if res.Size != len(rotated) || log.String() != wantLog {
			t.Errorf("worker %d: RotateFile result %+v, log:\n%s", w, res, log.String())
		}
		if got, err := os.ReadFile(out); err != nil || !bytes.Equal(got, rotated) {
			t.Errorf("worker %d: rotated output differs (%v)", w, err)
		}

		info, err := InfoFile(out)
		if err != nil || !reflect.DeepEqual(info, wantInfo) {
			t.Errorf("worker %d: InfoFile = %+v, %v", w, info, err)
		}

		restored := filepath.Join(dir, fmt.Sprintf("restored%d.heic", w))
		if _, err := ReverseFile(out, restored, FileOptions{}); err != nil {
			t.Errorf("worker %d: ReverseFile: %v", w, err)
			return
		}
		if got, err := os.ReadFile(restored); err != nil || !bytes.Equal(got, original) {
			t.Errorf("worker %d: restored output isn't the original (%v)", w, err)
		}

		// Without Force, a second rotate onto the same output is refused.
		if _, err := RotateFile(in, out, 1, FileOptions{}); !errors.Is(err, fs.ErrExist) {
			t.Errorf("worker %d: rotate onto an existing output: err = %v, want fs.ErrExist", w, err)
		}
	})
	if got := mustRead(t, in); !bytes.Equal(got, original) {
		t.Error("the shared input was modified")
	}
	if left := leftoverTemps(t, dir); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}

// TestConcurrentWritesToOneOutput has many goroutines rotate onto the SAME
// output with Force while others read it. Because each write is a temp file
// renamed into place, a reader sees either no file or a complete one, never
// a partial write, and the final file is complete.
func TestConcurrentWritesToOneOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows refuses to rename over a file another goroutine has open")
	}
	dir := t.TempDir()
	in, out := filepath.Join(dir, "photo.heic"), filepath.Join(dir, "rotated.heic")
	original := writeSynth(t, in, func(o *synth.Options) {
		o.Layout, o.IncludeExif, o.MdatPayload = synth.MdatFirst, true, tests.CyclicPayload(256<<10)
	})
	want := tests.MustApply(t, original, 1)

	tests.Parallel(tests.ConcurrentWorkers, func(w int) {
		if w%2 == 0 {
			for range 4 {
				if _, err := RotateFile(in, out, 1, FileOptions{Force: true}); err != nil {
					t.Errorf("worker %d: RotateFile: %v", w, err)
				}
			}
			return
		}
		for range 50 {
			got, err := os.ReadFile(out)
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil || !bytes.Equal(got, want) {
				t.Errorf("worker %d: read a partial or wrong output (%d bytes, %v)", w, len(got), err)
				return
			}
		}
	})
	if !bytes.Equal(mustRead(t, out), want) {
		t.Error("the final output isn't the rotated file")
	}
	if left := leftoverTemps(t, dir); len(left) != 0 {
		t.Errorf("temp files left behind: %v", left)
	}
}
