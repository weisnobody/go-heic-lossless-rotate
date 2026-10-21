package stream

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/weisnobody/go-heic-lossless-rotate/internal/tests"
	"github.com/weisnobody/go-heic-lossless-rotate/pkg/core"
)

// readAllView materializes v through WriteTo, the path a caller writing a
// file takes. The writer is a plain io.Writer: one with ReadFrom (like
// bytes.Buffer or *os.File) would bypass WriteTo's own copy loop.
func readAllView(v *View) ([]byte, error) {
	if v == nil {
		return nil, nil
	}
	var b bytes.Buffer
	_, err := v.WriteTo(struct{ io.Writer }{&b})
	return b.Bytes(), err
}

// streamOps are the public streaming operations on one shared reader. Each
// result is in the same shape as its []byte engine counterpart's.
var streamOps = map[string]func(r io.ReaderAt, size int64) outcome{
	"ApplyRotationReaderAt": func(r io.ReaderAt, size int64) outcome {
		var log strings.Builder
		var outs [][]byte
		var errs []string
		for turns := range 4 {
			v, err := ApplyRotationReaderAt(r, size, turns, core.RotateOptions{Log: &log})
			out, rerr := readAllView(v)
			if err == nil {
				err = rerr
			}
			outs, errs = append(outs, out), append(errs, tests.ErrText(err))
		}
		return outcome{out: bytes.Join(outs, []byte("|")), log: log.String(), err: strings.Join(errs, "|")}
	},
	"ReverseRotationReaderAt": func(r io.ReaderAt, size int64) outcome {
		var log strings.Builder
		v, err := ReverseRotationReaderAt(r, size, core.ReverseOptions{Log: &log})
		out, rerr := readAllView(v)
		if err == nil {
			err = rerr
		}
		return outcome{out: out, log: log.String(), err: tests.ErrText(err)}
	},
	"VerifyReversibleReaderAt": func(r io.ReaderAt, size int64) outcome {
		var log strings.Builder
		err := VerifyReversibleReaderAt(r, size, core.ReverseOptions{Log: &log})
		return outcome{log: log.String(), err: tests.ErrText(err)}
	},
	"GatherInfoReaderAt": func(r io.ReaderAt, size int64) outcome {
		info, err := GatherInfoReaderAt(r, size)
		return outcome{info: info, err: tests.ErrText(err)}
	},
}

// bytesOps are the []byte engine's results for the same operations: what
// every concurrent streaming call must return.
func bytesOps(data []byte) map[string]outcome {
	var rlog, vlog, alog strings.Builder
	var outs [][]byte
	var errs []string
	for turns := range 4 {
		out, err := core.ApplyRotation(data, turns, core.RotateOptions{Log: &alog})
		outs, errs = append(outs, out), append(errs, tests.ErrText(err))
	}
	rev, rerr := core.ReverseRotation(data, core.ReverseOptions{Log: &rlog})
	verr := core.VerifyReversible(data, core.ReverseOptions{Log: &vlog})
	info, ierr := core.GatherInfo(data)
	return map[string]outcome{
		"ApplyRotationReaderAt":    {out: bytes.Join(outs, []byte("|")), log: alog.String(), err: strings.Join(errs, "|")},
		"ReverseRotationReaderAt":  {out: rev, log: rlog.String(), err: tests.ErrText(rerr)},
		"VerifyReversibleReaderAt": {log: vlog.String(), err: tests.ErrText(verr)},
		"GatherInfoReaderAt":       {info: info, err: tests.ErrText(ierr)},
	}
}

// TestConcurrentUse runs every streaming operation from many goroutines at
// once, all sharing one io.ReaderAt per input - an in-memory reader and an
// open *os.File (run it with -race). Each call must return exactly the
// []byte engine's result.
func TestConcurrentUse(t *testing.T) {
	inputs := tests.ConcurrencyInputs(t)
	var names, ops []string
	for name := range inputs {
		names = append(names, name)
	}
	for op := range streamOps {
		ops = append(ops, op)
	}
	sort.Strings(names)
	sort.Strings(ops)

	want := map[string]map[string]outcome{}
	files := map[string]*os.File{}
	for i, name := range names {
		want[name] = bytesOps(inputs[name])
		path := filepath.Join(t.TempDir(), fmt.Sprintf("in%d.heic", i))
		if err := os.WriteFile(path, inputs[name], 0o644); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { f.Close() })
		files[name] = f
	}

	for _, source := range []string{"bytes.Reader", "os.File"} {
		t.Run(source, func(t *testing.T) {
			readers := map[string]io.ReaderAt{}
			for _, name := range names {
				readers[name] = bytes.NewReader(inputs[name])
				if source == "os.File" {
					readers[name] = files[name]
				}
			}
			tests.Parallel(tests.ConcurrentWorkers, func(w int) {
				for i := range len(names) * len(ops) {
					k := (i + w) % (len(names) * len(ops))
					name, op := names[k/len(ops)], ops[k%len(ops)]
					got := streamOps[op](readers[name], int64(len(inputs[name])))
					if !reflect.DeepEqual(got, want[name][op]) {
						t.Errorf("worker %d: %s on %q differs from the []byte engine", w, op, name)
					}
				}
			})
		})
	}
}

// TestConcurrentViewReads reads one View from many goroutines at once, by
// every method: a View may be shared, like the io.ReaderAt it reads from.
func TestConcurrentViewReads(t *testing.T) {
	data := tests.ConcurrencyInputs(t)["meta-first with Exif"]
	want := tests.MustApply(t, data, 1)
	v, err := ApplyRotationReaderAt(bytes.NewReader(data), int64(len(data)), 1, core.RotateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(v.segs) < 2 {
		t.Fatal("the View doesn't read from its input; this test needs one that does")
	}
	tests.Parallel(tests.ConcurrentWorkers, func(w int) {
		if b, err := v.Bytes(); err != nil || !bytes.Equal(b, want) {
			t.Errorf("worker %d: Bytes differs (%v)", w, err)
		}
		if b, err := readAllView(v); err != nil || !bytes.Equal(b, want) {
			t.Errorf("worker %d: WriteTo differs (%v)", w, err)
		}
		var rf bytes.Buffer // has ReadFrom, so WriteTo's copies take that path
		if _, err := v.WriteTo(&rf); err != nil || !bytes.Equal(rf.Bytes(), want) {
			t.Errorf("worker %d: WriteTo to an io.ReaderFrom differs (%v)", w, err)
		}
		// Small reads at offsets that differ per worker, crossing segment
		// boundaries.
		buf := make([]byte, 97+w)
		for off := int64(w); off < v.Size(); off += int64(len(buf)) * 3 {
			n, err := v.ReadAt(buf, off)
			if err != nil && err != io.EOF {
				t.Errorf("worker %d: ReadAt(%d): %v", w, off, err)
				return
			}
			if !bytes.Equal(buf[:n], want[off:off+int64(n)]) {
				t.Errorf("worker %d: ReadAt(%d) differs", w, off)
				return
			}
		}
	})
}
