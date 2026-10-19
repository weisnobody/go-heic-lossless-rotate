package cli

// Port of upstream tests/test_heic_rotate.py's CLI tests (same methods, as
// subtests). Upstream runs the CLI as a subprocess; these call Main
// in-process (cmd/cmd_test.go and the difftest suite cover real processes).

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	heicrotate "github.com/pjg/go-heic-lossless-rotate"
	"github.com/pjg/go-heic-lossless-rotate/internal/synth"
)

// runCLI mirrors upstream's run_cli: returns (returncode, stdout, stderr).
func runCLI(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	rc := Main(append([]string{"heic-rotate"}, args...), &stdout, &stderr)
	return rc, stdout.String(), stderr.String()
}

func mustSynthFile(t *testing.T, path string, mutate func(*synth.Options)) []byte {
	t.Helper()
	o := synth.DefaultOptions()
	if mutate != nil {
		mutate(&o)
	}
	data, err := synth.BuildSyntheticHEIC(o)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return data
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

// ---------------------------------------------------------------------------
// 3. CLI-level tests
// ---------------------------------------------------------------------------

func TestCLI(t *testing.T) {
	setUp := func(t *testing.T) (tmpdir, inputPath string) {
		tmpdir = t.TempDir()
		inputPath = filepath.Join(tmpdir, "input.heic")
		mustSynthFile(t, inputPath, func(o *synth.Options) { o.Layout = synth.MetaFirst })
		return
	}

	t.Run("no_args_shows_help_and_exits_nonzero", func(t *testing.T) {
		rc, out, err := runCLI()
		if rc == 0 {
			t.Error("rc = 0, want nonzero")
		}
		for _, w := range []string{"rotate", "reverse", "info"} {
			if !strings.Contains(out+err, w) {
				t.Errorf("help is missing %q", w)
			}
		}
	})

	t.Run("version_flag_reports_version_and_metadata_versions", func(t *testing.T) {
		for _, flag := range []string{"-V", "--version"} {
			rc, out, err := runCLI(flag)
			if rc != 0 {
				t.Fatalf("%s: rc %d: %s", flag, rc, err)
			}
			want := []string{heicrotate.Version, fmt.Sprint(heicrotate.FormatVersion)}
			for _, v := range heicrotate.SupportedReverseFormatVersions {
				want = append(want, fmt.Sprint(v))
			}
			for _, w := range want {
				if !strings.Contains(out, w) {
					t.Errorf("%s output missing %q", flag, w)
				}
			}
		}
	})

	t.Run("rotate_refuses_to_overwrite_existing_output_without_force", func(t *testing.T) {
		tmpdir, input := setUp(t)
		outPath := filepath.Join(tmpdir, "out.heic")
		if rc, _, err := runCLI("rotate", "90", input, outPath); rc != 0 {
			t.Fatal(err)
		}
		firstWrite := readFile(t, outPath)
		rc, out, err := runCLI("rotate", "90", input, outPath)
		if rc == 0 || !strings.Contains(strings.ToLower(out+err), "overwrite") {
			t.Errorf("second rotate: rc %d, output %q", rc, out+err)
		}
		if !bytes.Equal(readFile(t, outPath), firstWrite) {
			t.Error("refused overwrite must leave the existing file untouched")
		}
	})

	t.Run("rotate_force_allows_overwrite", func(t *testing.T) {
		tmpdir, input := setUp(t)
		outPath := filepath.Join(tmpdir, "out.heic")
		if rc, _, err := runCLI("rotate", "90", input, outPath); rc != 0 {
			t.Fatal(err)
		}
		for _, flag := range []string{"-f", "--force"} {
			if rc, _, err := runCLI("rotate", "90", input, outPath, flag); rc != 0 {
				t.Errorf("%s: rc %d: %s", flag, rc, err)
			}
		}
	})

	t.Run("rotate_reports_reversibility_self_check_passed", func(t *testing.T) {
		tmpdir, input := setUp(t)
		rc, out, err := runCLI("rotate", "90", input, filepath.Join(tmpdir, "out.heic"))
		if rc != 0 || !strings.Contains(out, "reversibility self-check passed") {
			t.Errorf("rc %d, stdout %q, stderr %q", rc, out, err)
		}
	})

	t.Run("rotate_quiet_suppresses_self_check_message", func(t *testing.T) {
		tmpdir, input := setUp(t)
		rc, out, err := runCLI("rotate", "90", input, filepath.Join(tmpdir, "out.heic"), "-q")
		if rc != 0 || out != "" {
			t.Errorf("rc %d, stdout %q, stderr %q", rc, out, err)
		}
	})

	t.Run("info_reports_recorded_tool_version", func(t *testing.T) {
		tmpdir, input := setUp(t)
		outPath := filepath.Join(tmpdir, "out.heic")
		if rc, _, err := runCLI("rotate", "90", input, outPath, "-q"); rc != 0 {
			t.Fatal(err)
		}
		rc, out, err := runCLI("info", outPath)
		if rc != 1 || !strings.Contains(out, "heic-lossless-rotate v"+heicrotate.Version+", provenance format") {
			t.Errorf("rc %d, stdout %q, stderr %q", rc, out, err)
		}
	})

	t.Run("reverse_force_overwrites_and_ignore_tamper_check_is_separate", func(t *testing.T) {
		// -f/--force on reverse means "overwrite the output file", NOT the
		// old tamper override (renamed --ignore-tamper-check).
		tmpdir, input := setUp(t)
		rotated := filepath.Join(tmpdir, "rotated.heic")
		restored := filepath.Join(tmpdir, "restored.heic")
		if rc, _, err := runCLI("rotate", "90", input, rotated); rc != 0 {
			t.Fatal(err)
		}
		if rc, _, err := runCLI("reverse", rotated, restored); rc != 0 {
			t.Fatal(err)
		}
		if rc, out, err := runCLI("reverse", rotated, restored); rc == 0 || !strings.Contains(strings.ToLower(out+err), "overwrite") {
			t.Errorf("second reverse: rc %d, %q", rc, out+err)
		}
		if rc, _, err := runCLI("reverse", rotated, restored, "-f"); rc != 0 {
			t.Errorf("reverse -f: %s", err)
		}
		if rc, _, err := runCLI("reverse", rotated, filepath.Join(tmpdir, "restored2.heic"), "--ignore-tamper-check"); rc != 0 {
			t.Errorf("--ignore-tamper-check: %s", err)
		}
	})

	t.Run("flag_before_subcommand_works", func(t *testing.T) {
		tmpdir, input := setUp(t)
		outPath := filepath.Join(tmpdir, "out1.heic")
		if rc, _, err := runCLI("-q", "rotate", "90", input, outPath); rc != 0 || !exists(outPath) {
			t.Errorf("rc %d: %s", rc, err)
		}
	})

	t.Run("flag_after_subcommand_works", func(t *testing.T) {
		tmpdir, input := setUp(t)
		outPath := filepath.Join(tmpdir, "out2.heic")
		if rc, _, err := runCLI("rotate", "90", input, outPath, "-q"); rc != 0 || !exists(outPath) {
			t.Errorf("rc %d: %s", rc, err)
		}
	})

	t.Run("implicit_rotate_bare_value_matches_explicit", func(t *testing.T) {
		tmpdir, input := setUp(t)
		explicit, implicit := filepath.Join(tmpdir, "explicit.heic"), filepath.Join(tmpdir, "implicit.heic")
		rc1, _, err1 := runCLI("rotate", "90", input, explicit)
		rc2, _, err2 := runCLI("90", input, implicit)
		if rc1 != 0 || rc2 != 0 {
			t.Fatalf("%s / %s", err1, err2)
		}
		if !bytes.Equal(readFile(t, explicit), readFile(t, implicit)) {
			t.Error("implicit rotate output differs from explicit")
		}
	})

	t.Run("implicit_rotate_zero_is_accepted", func(t *testing.T) {
		tmpdir, input := setUp(t)
		outPath := filepath.Join(tmpdir, "implicit_zero.heic")
		if rc, _, err := runCLI("0", input, outPath); rc != 0 || !exists(outPath) {
			t.Errorf("rc %d: %s", rc, err)
		}
	})

	t.Run("implicit_rotate_flag_before_bare_value_works", func(t *testing.T) {
		tmpdir, input := setUp(t)
		outPath := filepath.Join(tmpdir, "implicit_flagged.heic")
		if rc, _, err := runCLI("-q", "180", input, outPath); rc != 0 || !exists(outPath) {
			t.Errorf("rc %d: %s", rc, err)
		}
	})

	t.Run("implicit_rotate_does_not_hijack_other_subcommands", func(t *testing.T) {
		tmpdir, input := setUp(t)
		outPath := filepath.Join(tmpdir, "r90.heic")
		runCLI("rotate", "90", input, outPath, "-q")
		if rc, _, err := runCLI("info", outPath); rc != 1 || strings.Contains(strings.ToLower(err), "usage:") {
			t.Errorf("rc %d, stderr %q", rc, err)
		}
	})

	t.Run("dry_run_writes_nothing", func(t *testing.T) {
		tmpdir, input := setUp(t)
		outPath := filepath.Join(tmpdir, "should_not_exist.heic")
		if rc, _, err := runCLI("rotate", "90", input, outPath, "--dry-run"); rc != 0 || exists(outPath) {
			t.Errorf("rc %d, exists %v: %s", rc, exists(outPath), err)
		}
	})

	t.Run("info_exit_codes", func(t *testing.T) {
		tmpdir, input := setUp(t)
		if rc, _, _ := runCLI("info", input, "-q"); rc != 0 {
			t.Errorf("never touched: rc %d, want 0", rc)
		}
		for _, tc := range []struct {
			angle string
			want  int
		}{{"90", 1}, {"180", 2}, {"0", 4}} {
			out := filepath.Join(tmpdir, "r"+tc.angle+".heic")
			runCLI("rotate", tc.angle, input, out, "-q")
			if rc, _, _ := runCLI("info", out, "-q"); rc != tc.want {
				t.Errorf("rotate %s: info rc %d, want %d", tc.angle, rc, tc.want)
			}
		}
	})

	t.Run("reverse_dry_run_as_provenance_check", func(t *testing.T) {
		tmpdir, input := setUp(t)
		if rc, _, _ := runCLI("reverse", input, "--dry-run", "-q"); rc == 0 {
			t.Error("untouched file: reverse --dry-run must fail")
		}
		outPath := filepath.Join(tmpdir, "r90.heic")
		runCLI("rotate", "90", input, outPath, "-q")
		if rc, _, err := runCLI("reverse", outPath, "--dry-run", "-q"); rc != 0 {
			t.Errorf("rotated file: rc %d: %s", rc, err)
		}
	})

	t.Run("error_exit_code_isolated_from_info_codes", func(t *testing.T) {
		tmpdir, _ := setUp(t)
		rc, _, _ := runCLI("info", filepath.Join(tmpdir, "nonexistent.heic"))
		if rc != heicrotate.ErrorExitCode || (rc >= 0 && rc <= 4) {
			t.Errorf("rc %d, want %d", rc, heicrotate.ErrorExitCode)
		}
	})
}

// ---------------------------------------------------------------------------
// 5. CLI output-path robustness (the CLI half of upstream group 5)
// ---------------------------------------------------------------------------

func TestSameFileInputOutput(t *testing.T) {
	setUp := func(t *testing.T) (string, string, []byte) {
		tmpdir := t.TempDir()
		path := filepath.Join(tmpdir, "photo.heic")
		original := mustSynthFile(t, path, func(o *synth.Options) { o.Layout = synth.MetaFirst })
		return tmpdir, path, original
	}

	t.Run("rotate_with_output_equal_to_input", func(t *testing.T) {
		_, path, original := setUp(t)
		if rc, _, err := runCLI("rotate", "90", path, path, "-f", "-q"); rc != 0 {
			t.Fatal(err)
		}
		result := readFile(t, path)
		if bytes.Equal(result, original) {
			t.Fatal("file unchanged")
		}
		if err := heicrotate.VerifyStructure(result); err != nil {
			t.Fatal(err)
		}
		restored, err := heicrotate.ReverseRotation(result, heicrotate.ReverseOptions{})
		if err != nil || !bytes.Equal(restored, original) {
			t.Errorf("rotating a file onto itself must still be fully reversible: %v", err)
		}
	})

	t.Run("reverse_with_output_equal_to_input", func(t *testing.T) {
		tmpdir, path, original := setUp(t)
		rotated := filepath.Join(tmpdir, "rotated.heic")
		if rc, _, err := runCLI("rotate", "90", path, rotated, "-q"); rc != 0 {
			t.Fatal(err)
		}
		if rc, _, err := runCLI("reverse", rotated, rotated, "-f", "-q"); rc != 0 {
			t.Fatal(err)
		}
		if !bytes.Equal(readFile(t, rotated), original) {
			t.Error("reversing a file onto itself must reconstruct the exact true original")
		}
	})
}

func TestDryRunSizeReport(t *testing.T) {
	t.Run("dry_run_reports_correct_size_and_delta", func(t *testing.T) {
		tmpdir := t.TempDir()
		input := filepath.Join(tmpdir, "input.heic")
		payload := make([]byte, 64<<10)
		for i := range payload {
			payload[i] = byte(i)
		}
		original := mustSynthFile(t, input, func(o *synth.Options) { o.MdatPayload = payload })
		expected, err := heicrotate.ApplyRotation(original, 1, heicrotate.RotateOptions{})
		if err != nil {
			t.Fatal(err)
		}
		outPath := filepath.Join(tmpdir, "out.heic")
		rc, out, stderr := runCLI("rotate", "90", input, outPath, "--dry-run")
		if rc != 0 || exists(outPath) {
			t.Fatalf("rc %d, exists %v: %s", rc, exists(outPath), stderr)
		}
		for _, w := range []string{fmt.Sprintf("%d bytes", len(expected)), fmt.Sprintf("%+d", len(expected)-len(original))} {
			if !strings.Contains(out, w) {
				t.Errorf("dry-run output missing %q:\n%s", w, out)
			}
		}
	})
}

// ---------------------------------------------------------------------------
// Go-only: -G (docs/COMPAT.md E1) and exact error text. (Default naming and
// the post-rotate self-check moved to the root package's file API tests.)
// ---------------------------------------------------------------------------

func TestGoVersionFlag(t *testing.T) {
	for _, flag := range []string{"-G", "--goversion", "--gov"} {
		rc, out, err := runCLI(flag)
		if rc != 0 || out != heicrotate.GoVersionString()+"\n" || err != "" {
			t.Errorf("%s: rc %d, stdout %q, stderr %q", flag, rc, out, err)
		}
	}
	if rc, _, _ := runCLI("rotate", "-G"); rc != 2 {
		t.Errorf("rotate -G: rc %d, want 2 (top-level only, like -V)", rc)
	}
	if _, out, _ := runCLI("-V"); strings.Contains(out, "+go.") {
		t.Errorf("-V must report only the upstream version: %q", out)
	}
}

func TestErrorMessages(t *testing.T) {
	tmpdir := t.TempDir()
	missing := filepath.Join(tmpdir, "missing.heic")
	rc, out, stderr := runCLI("info", missing)
	want := "error: [Errno 2] No such file or directory: '" + missing + "'\n"
	if rc != heicrotate.ErrorExitCode || out != "" || stderr != want {
		t.Errorf("missing input: rc %d, stdout %q, stderr %q; want %q", rc, out, stderr, want)
	}

	input, outPath := filepath.Join(tmpdir, "in.heic"), filepath.Join(tmpdir, "out.heic")
	mustSynthFile(t, input, nil)
	os.WriteFile(outPath, []byte("existing"), 0o644)
	_, _, stderr = runCLI("rotate", "90", input, outPath, "-q")
	if want := "error: refusing to overwrite existing file '" + outPath + "' - use -f/--force to overwrite it\n"; stderr != want {
		t.Errorf("overwrite refusal: %q, want %q", stderr, want)
	}

	_, _, stderr = runCLI("reverse", input, "-q")
	if !strings.HasPrefix(stderr, "error: no heic-lossless-rotate provenance record found in this file") {
		t.Errorf("reverse of untouched file: %q", stderr)
	}
	if err := errors.New("x"); errorText(err) != "x" {
		t.Error("errorText fallback")
	}
	// Wrapped core errors print their own message, as Python's {e} does.
	if got := errorText(&heicrotate.ProvenanceError{Msg: "post-rotate reversibility self-check failed - refusing to write output: x"}); got != "post-rotate reversibility self-check failed - refusing to write output: x" {
		t.Errorf("errorText(ProvenanceError) = %q", got)
	}
}
