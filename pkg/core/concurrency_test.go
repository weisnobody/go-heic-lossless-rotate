package core_test

import (
	"bytes"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/pjg/go-heic-lossless-rotate/internal/tests"
	"github.com/pjg/go-heic-lossless-rotate/pkg/core"
)

// outcome is everything one call returns: output bytes, log text, Info, and
// error.
type outcome struct {
	out  []byte
	log  string
	info *core.Info
	err  string
}

// coreOps are the public operations, each on one input with its own Log.
var coreOps = map[string]func(data []byte) outcome{
	"ApplyRotation": func(data []byte) outcome {
		var log strings.Builder
		var outs [][]byte
		var errs []string
		for turns := range 4 {
			out, err := core.ApplyRotation(data, turns, core.RotateOptions{Log: &log})
			outs, errs = append(outs, out), append(errs, tests.ErrText(err))
		}
		return outcome{out: bytes.Join(outs, []byte("|")), log: log.String(), err: strings.Join(errs, "|")}
	},
	"ReverseRotation": func(data []byte) outcome {
		var log strings.Builder
		out, err := core.ReverseRotation(data, core.ReverseOptions{Log: &log})
		return outcome{out: out, log: log.String(), err: tests.ErrText(err)}
	},
	"VerifyReversible": func(data []byte) outcome {
		var log strings.Builder
		err := core.VerifyReversible(data, core.ReverseOptions{Log: &log})
		return outcome{log: log.String(), err: tests.ErrText(err)}
	},
	"GatherInfo": func(data []byte) outcome {
		info, err := core.GatherInfo(data)
		text := ""
		if info != nil {
			s, code := core.FormatInfo(info, false)
			text = fmt.Sprintf("%s (exit %d)", s, code)
		}
		return outcome{info: info, log: text, err: tests.ErrText(err)}
	},
	"VerifyStructure": func(data []byte) outcome {
		return outcome{err: tests.ErrText(core.VerifyStructure(data))}
	},
	"GetCurrentRotation": func(data []byte) outcome {
		turns, err := core.GetCurrentRotation(data)
		return outcome{log: fmt.Sprint(turns), err: tests.ErrText(err)}
	},
}

// TestConcurrentUse runs every public operation on the same inputs from
// many goroutines at once (run it with -race). Each call must give exactly
// what it gives when run alone, and the shared input slices must not be
// modified.
func TestConcurrentUse(t *testing.T) {
	inputs := tests.ConcurrencyInputs(t)
	var names, ops []string
	for name := range inputs {
		names = append(names, name)
	}
	for op := range coreOps {
		ops = append(ops, op)
	}
	sort.Strings(names)
	sort.Strings(ops)

	pristine := map[string][]byte{}
	want := map[string]outcome{}
	for _, name := range names {
		pristine[name] = bytes.Clone(inputs[name])
		for _, op := range ops {
			want[name+"/"+op] = coreOps[op](inputs[name])
		}
	}

	tests.Parallel(tests.ConcurrentWorkers, func(w int) {
		for i := range len(names) * len(ops) {
			// Each worker starts at a different (input, operation) pair, so
			// different operations overlap on the same input.
			k := (i + w) % (len(names) * len(ops))
			name, op := names[k/len(ops)], ops[k%len(ops)]
			if got := coreOps[op](inputs[name]); !reflect.DeepEqual(got, want[name+"/"+op]) {
				t.Errorf("worker %d: %s on %q differs from a sequential call", w, op, name)
			}
		}
	})

	for _, name := range names {
		if !bytes.Equal(inputs[name], pristine[name]) {
			t.Errorf("input %q was modified", name)
		}
	}
}
