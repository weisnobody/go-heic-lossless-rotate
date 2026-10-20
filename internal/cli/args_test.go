package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode"
)

// argparseCase is one entry of testdata/argparse_py314.json: an argv run
// through upstream's real build_parser() + _insert_implicit_rotate() under
// CPython 3.14, with its exit code ("parsed" when parsing succeeded),
// whether stdout/stderr got output, and the parsed namespace.
type argparseCase struct {
	Argv   []string       `json:"argv"`
	Code   any            `json:"code"`
	Stdout bool           `json:"stdout"`
	Stderr bool           `json:"stderr"`
	NS     map[string]any `json:"ns"`
}

func loadArgparseCases(t *testing.T) []argparseCase {
	t.Helper()
	data, err := os.ReadFile("testdata/argparse_py314.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []argparseCase
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

// isGoVersionFlag reports whether argv uses -G/--goversion (or a prefix of
// it) at the top level - Go-only (docs/COMPAT.md E1), so Python rejects it.
func isGoVersionFlag(argv []string) bool {
	for _, a := range argv {
		if !strings.HasPrefix(a, "-") {
			return false
		}
		if a == "-G" || (len(a) > 3 && strings.HasPrefix("--goversion", a)) {
			return true
		}
	}
	return false
}

func parseForTest(argv []string) (a *args, code any, stdout, stderr string) {
	var out, errb bytes.Buffer
	a = &args{}
	err := buildParser("heic-rotate").parse(insertImplicitRotate(argv), a, &out, &errb)
	var ee *exitError
	switch {
	case err == nil:
		code = "parsed"
	case errors.As(err, &ee):
		code = float64(ee.code)
	default:
		code = err.Error()
	}
	return a, code, out.String(), errb.String()
}

func TestParserMatchesPython314(t *testing.T) {
	for _, tc := range loadArgparseCases(t) {
		if len(tc.Argv) == 0 {
			continue // no-args help is Main's job (TestCLI)
		}
		t.Run(fmt.Sprintf("%q", tc.Argv), func(t *testing.T) {
			a, code, stdout, stderr := parseForTest(tc.Argv)
			wantCode, wantStdout, wantStderr := tc.Code, tc.Stdout, tc.Stderr
			if isGoVersionFlag(tc.Argv) { // E1: Go prints its version, exit 0
				wantCode, wantStdout, wantStderr = float64(0), true, false
			}
			if code != wantCode {
				t.Fatalf("exit = %v, want %v (stderr: %s)", code, wantCode, stderr)
			}
			if (stdout != "") != wantStdout || (stderr != "") != wantStderr {
				t.Errorf("stdout/stderr non-empty = %v/%v, want %v/%v", stdout != "", stderr != "", wantStdout, wantStderr)
			}
			if code != "parsed" {
				return
			}
			ns := tc.NS
			str := func(k string) string {
				if s, ok := ns[k].(string); ok {
					return s
				}
				return "" // Python None
			}
			flag := func(k string) bool { b, _ := ns[k].(bool); return b }
			got := map[string]any{
				"cmd": a.cmd, "input": a.input, "output": a.output, "quiet": a.quiet, "dry_run": a.dryRun,
				"force": a.force, "no_exif_sync": a.noExifSync, "ignore_tamper_check": a.ignoreTamperCheck,
			}
			want := map[string]any{
				"cmd": str("cmd"), "input": str("input"), "output": str("output"), "quiet": flag("quiet"),
				"dry_run": flag("dry_run"), "force": flag("force"), "no_exif_sync": flag("no_exif_sync"),
				"ignore_tamper_check": flag("ignore_tamper_check"),
			}
			if r, ok := ns["rotation"].(float64); ok {
				got["rotation"], want["rotation"] = a.rotation, int(r)
			}
			for k := range want {
				if got[k] != want[k] {
					t.Errorf("%s = %v, want %v", k, got[k], want[k])
				}
			}
		})
	}
}

func TestParsePyInt(t *testing.T) {
	// Python: int(s) for each; captured from CPython 3.14.
	valid := map[string]int{
		"90": 90, "090": 90, "+90": 90, " 90": 90, "90 ": 90, "  90": 90, "9_0": 90, "0": 0, "00": 0, "-0": 0,
		"-90": -90, "９０": 90, "٩٠": 90, "\u096f\u0966": 90, "\u0969\u0966": 30, "\U0001D7EB\U0001D7E2": 90,
	}
	for s, want := range valid {
		if got, ok := parsePyInt(s); !ok || got != want {
			t.Errorf("parsePyInt(%q) = %d, %v; want %d", s, got, ok, want)
		}
	}
	for _, s := range []string{"", " ", "90.0", "0x5a", "9__0", "_90", "90_", "+-90", "−90", "9 0", "+", "1e2", "99999999999999999999", "\x1c90"} {
		if got, ok := parsePyInt(s); ok {
			t.Errorf("parsePyInt(%q) = %d; Python rejects it", s, got)
		}
	}
}

func TestInsertImplicitRotate(t *testing.T) {
	tests := []struct{ in, want []string }{
		{[]string{"90", "x"}, []string{"rotate", "90", "x"}},
		{[]string{"-q", "180", "x"}, []string{"-q", "rotate", "180", "x"}},
		{[]string{"info", "90"}, []string{"info", "90"}},
		{[]string{"x", "90"}, []string{"x", "90"}},
		{[]string{"-h"}, []string{"-h"}},
		{[]string{"45", "x"}, []string{"45", "x"}},
	}
	for _, tt := range tests {
		if got := insertImplicitRotate(tt.in); strings.Join(got, " ") != strings.Join(tt.want, " ") {
			t.Errorf("insertImplicitRotate(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestHelpMentionsSubcommands(t *testing.T) {
	_, code, stdout, _ := parseForTest([]string{"-h"})
	if code != float64(0) || !strings.HasPrefix(stdout, "usage: heic-rotate ") {
		t.Fatalf("-h: code %v, stdout %q", code, stdout[:min(60, len(stdout))])
	}
	for _, want := range []string{"rotate", "reverse", "info", "ROTATION DIRECTION: COUNTER-CLOCKWISE", "heic-rotate rotate <0|90|180|270>"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("help is missing %q", want)
		}
	}
	if strings.Contains(stdout, "%(prog)s") {
		t.Error("help still contains %(prog)s")
	}
}

func TestDecimalValueTable(t *testing.T) {
	// decimalValue assumes every unicode.Nd range is a stride-1 run of whole
	// 0-9 blocks starting at a zero, so value = offset mod 10.
	check := func(lo, hi, stride uint32) {
		if stride != 1 || (hi-lo+1)%10 != 0 {
			t.Errorf("Nd range %#x-%#x (stride %d) isn't whole digit blocks", lo, hi, stride)
		}
	}
	for _, r := range unicode.Nd.R16 {
		check(uint32(r.Lo), uint32(r.Hi), uint32(r.Stride))
	}
	for _, r := range unicode.Nd.R32 {
		check(r.Lo, r.Hi, r.Stride)
	}
	t.Logf("Go unicode.Version %s", unicode.Version)
}
