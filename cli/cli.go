// Package cli is the heic-lossless-rotate command-line interface
// (← heic_rotate/cli.py): the heic-rotate and heic-lossless-rotate binaries
// are thin wrappers around Main. It is a drop-in replacement for the Python
// CLI - same subcommands, flags, operational messages, and exit codes (see
// docs/SPEC.md and docs/COMPAT.md).
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	heicrotate "github.com/pjg/go-heic-lossless-rotate"
	"github.com/pjg/go-heic-lossless-rotate/internal/pyfmt"
)

// buildParser builds the argument parser (← build_parser). Shared flags are
// defined at the top level, so they work before the subcommand; the same
// flags on each subcommand let them work after it too (Python's
// default=SUPPRESS layering: both levels set the same field).
func buildParser(prog string) *parser {
	quiet := option{"-q", "--quiet", kindFlag, "quiet", "suppress informational stdout messages"}
	force := option{"-f", "--force", kindFlag, "force", "overwrite the output file if it already exists"}
	help := option{"-h", "--help", kindHelp, "", "show this help message and exit"}

	rotationSet := func(a *args, value string) error {
		n, ok := parsePyInt(value)
		if !ok {
			return fmt.Errorf("invalid int value: %s", pyRepr(value))
		}
		switch n {
		case 0, 90, 180, 270:
			a.rotation = n
			return nil
		}
		return fmt.Errorf("invalid choice: %d (choose from 0, 90, 180, 270)", n)
	}
	inputSet := func(a *args, value string) error { a.input = value; return nil }
	outputSet := func(a *args, value string) error { a.output = value; return nil }

	rotate := &parser{
		prog: prog + " rotate", name: "rotate",
		summary: "apply a rotation (counter-clockwise)",
		description: "Apply a lossless rotation to a HEIC/HEIF file. " +
			"Positive angles are COUNTER-CLOCKWISE (per the HEIF " +
			"'irot' box spec) - use 270 for a 90 deg clockwise " +
			"rotation.",
		opts: []option{
			help,
			{"", "--no-exif-sync", kindFlag, "no_exif_sync", "don't sync the legacy embedded Exif Orientation tag"},
			{"", "--dry-run", kindFlag, "dry_run", "do everything except write the output file: " +
				"verify the rotation can be performed, print " +
				"diagnostics, and set the exit code accordingly"},
			force,
			quiet,
		},
		positionals: []positional{
			{name: "rotation", metavar: "{0,90,180,270}", set: rotationSet,
				help: "rotation angle in degrees, COUNTER-CLOCKWISE " +
					"(e.g. use 270 for a 90 deg clockwise turn), " +
					"applied ON TOP OF " +
					"the file's existing rotation (0 if none) - " +
					"NOT an absolute target angle. `rotate 90` " +
					"twice lands at 180 deg, not back at 90. " +
					"0 is a true no-op for the displayed image; " +
					"it only adds heic-lossless-rotate's tracking " +
					"metadata (and makes an implicit 0 deg " +
					"orientation explicit if no 'irot' exists yet)."},
			{name: "input", set: inputSet, help: "input .heic file"},
			{name: "output", optional: true, set: outputSet,
				help: "output .heic file (default: <input>_rotated.heic; " +
					"or, if <input> ends with '_restored', that " +
					"suffix is stripped instead of adding '_rotated')"},
		},
	}

	reverse := &parser{
		prog: prog + " reverse", name: "reverse",
		summary: "undo all heic-lossless-rotate edits, restoring the exact original file",
		description: "Undo every heic-lossless-rotate edit on a file, restoring " +
			"it to be byte-identical to the true original.",
		opts: []option{
			help,
			{"", "--ignore-tamper-check", kindFlag, "ignore_tamper_check", "reverse even if the file appears to have been " +
				"modified by something else since the last edit " +
				"(was previously named --force; -f/--force now " +
				"means 'overwrite the output file' instead)"},
			{"", "--dry-run", kindFlag, "dry_run", "do everything except write the output file: " +
				"useful as a check for whether this file was " +
				"edited by heic-lossless-rotate and can cleanly be " +
				"reversed, without actually touching it. Exits " +
				"0 if reversible, non-zero with a stderr " +
				"diagnostic otherwise."},
			force,
			quiet,
		},
		positionals: []positional{
			{name: "input", set: inputSet, help: "input .heic file (previously edited by this tool)"},
			{name: "output", optional: true, set: outputSet, help: "output .heic file (default: <input>_restored.heic)"},
		},
	}

	info := &parser{
		prog: prog + " info", name: "info",
		summary: "report the rotation heic-lossless-rotate has applied, " +
			"without the heavier checks `reverse` does",
		description: "Report what heic-lossless-rotate knows about a file: " +
			"whether it carries a provenance record, and - if " +
			"so - the CUMULATIVE rotation this tool has added " +
			"on top of whatever the file started with (not " +
			"just the file's absolute current orientation, " +
			"which may also include rotation the file already " +
			"had before this tool ever touched it), plus the " +
			"original file's CRC32 (independently verifiable " +
			"by hashing your own original file). Cheap and " +
			"read-only - never writes anything, and never does " +
			"the full byte-level reconstruction/verification " +
			"that `reverse` does. " +
			"With -q: no text output, just an exit code. Exit 0 " +
			"is reserved EXCLUSIVELY for 'no heic-lossless-rotate " +
			"provenance record found' (this tool never touched " +
			"the file). Once a provenance record exists, the " +
			"exit code is always non-zero: 1/2/3 for a " +
			"cumulative rotation ADDED BY THIS TOOL of " +
			"90/180/270 deg, or 4 if this tool's net " +
			"contribution is 0 deg (e.g. `rotate 0` was used " +
			"just to add tracking, or opposing rotations " +
			"cancelled out). A genuine error " +
			fmt.Sprintf("(e.g. unreadable file) exits %d, ", heicrotate.ErrorExitCode) +
			"kept outside 0-4 so it's never mistaken for a " +
			"rotation-state result.",
		opts: []option{
			help,
			{"-q", "--quiet", kindFlag, "quiet", "no text output; exit code alone reports state " +
				"(see command help above)"},
		},
		positionals: []positional{{name: "input", set: inputSet, help: "input .heic file"}},
	}

	return &parser{
		prog:        prog,
		description: upstreamDocstring,
		opts: []option{
			help,
			{"-V", "--version", kindVersion, "", "show version and metadata format info, then exit"},
			{"-G", "--goversion", kindGoVersion, "", "show the Go port's own version, then exit (Go-only)"},
			{"-q", "--quiet", kindFlag, "quiet", "suppress informational stdout messages " +
				"(errors still go to stderr)"},
			{"", "--dry-run", kindFlag, "dry_run", "for rotate/reverse: do everything except write " +
				"the output file (no effect on info, which " +
				"never writes a file anyway)"},
			{"-f", "--force", kindFlag, "force", "for rotate/reverse: overwrite the output file " +
				"if it already exists (refused by default)"},
		},
		subcommands: []*parser{rotate, reverse, info},
		version:     heicrotate.VersionString,
		goVersion:   heicrotate.GoVersionString,
	}
}

// bareRotationValues are the first non-option arguments that imply the
// 'rotate' subcommand (← _BARE_ROTATION_VALUES).
var bareRotationValues = []string{"0", "90", "180", "270"}

// insertImplicitRotate allows the 'rotate' subcommand name to be omitted:
// if the first non-option argument is exactly one of the bare rotation
// values, 'rotate' is inserted immediately before it
// (← _insert_implicit_rotate).
//
// It only ever looks at tokens up to (and not including) the first
// non-option argument, so it can't misfire on a value that happens to
// appear later (e.g. an output filename literally named "90"). Every
// top-level option is a boolean flag with no separate value token, so any
// argument starting with '-' is unambiguously an option to skip over -
// including -h/--help, which is left alone.
func insertImplicitRotate(argv []string) []string {
	for i, tok := range argv {
		if strings.HasPrefix(tok, "-") {
			continue
		}
		if slices.Contains(bareRotationValues, tok) {
			return append(append(append([]string{}, argv[:i]...), "rotate"), argv[i:]...)
		}
		break // first non-option token is something else (an explicit
		// subcommand name, or garbage the parser should report as such)
	}
	return argv
}

// Main runs the CLI with argv (argv[0] is the program name) and returns the
// process exit code (← main; Python calls sys.exit instead):
//
//	0       rotate/reverse succeeded, or -h/-V/-G
//	0-4     info: see FormatInfo
//	2       usage error, or no arguments at all (help is printed to stdout)
//	10      a genuine error (heicrotate.ErrorExitCode)
func Main(argv []string, stdout, stderr io.Writer) int {
	prog := "heic-lossless-rotate"
	if len(argv) > 0 {
		prog = filepath.Base(argv[0])
	}
	ap := buildParser(prog)
	if len(argv) <= 1 {
		fmt.Fprint(stdout, ap.formatHelp())
		return 2
	}
	a := &args{}
	if err := ap.parse(insertImplicitRotate(argv[1:]), a, stdout, stderr); err != nil {
		var ee *exitError
		if errors.As(err, &ee) {
			return ee.code
		}
		return 2
	}

	data, err := os.ReadFile(a.input)
	if err == nil {
		var code int
		if code, err = run(a, data, stdout); err == nil {
			return code
		}
	}
	fmt.Fprintf(stderr, "error: %s\n", errorText(err))
	return heicrotate.ErrorExitCode
}

// errorText renders an error the way Python's f"error: {e}" does for the
// exception types main() catches.
func errorText(err error) string {
	var be *heicrotate.BoxParseError
	var pe *heicrotate.ProvenanceError
	var refuse *refuseOverwriteError
	switch {
	case errors.As(err, &be):
		return be.Msg
	case errors.As(err, &pe):
		return pe.Msg
	case errors.As(err, &refuse):
		return refuse.msg
	default:
		return pyfmt.OSError(err)
	}
}

// refuseOverwriteError is the OSError upstream raises (with a plain
// message, no errno) when the output exists and -f wasn't given.
type refuseOverwriteError struct{ msg string }

func (e *refuseOverwriteError) Error() string { return e.msg }

// verifyReversible is the post-rotate self-check, indirected so tests can
// force a failure (upstream patches cli.verify_reversible the same way).
var verifyReversible = heicrotate.VerifyReversible

// run executes the parsed command against the input file's bytes
// (← _run) and returns the exit code.
func run(a *args, data []byte, stdout io.Writer) (int, error) {
	if a.cmd == "info" {
		result, err := heicrotate.GatherInfo(data)
		if err != nil {
			return 0, err
		}
		text, exitCode := heicrotate.FormatInfo(result, a.quiet)
		if text != "" {
			fmt.Fprintln(stdout, text)
		}
		return exitCode, nil
	}

	var log io.Writer
	if !a.quiet {
		log = stdout
	}
	say := func(format string, args ...any) {
		if !a.quiet {
			fmt.Fprintf(stdout, format+"\n", args...)
		}
	}

	var out []byte
	var outPath string
	if a.cmd == "rotate" {
		deltaTurns := (a.rotation / 90) % 4
		var err error
		out, err = heicrotate.ApplyRotation(data, deltaTurns, heicrotate.RotateOptions{NoExifSync: a.noExifSync, Log: log})
		if err != nil {
			return 0, err
		}
		if err := heicrotate.VerifyStructure(out); err != nil {
			return 0, err
		}
		say("[ok] output box structure is internally consistent")

		// Post-rotate reversibility self-check: verify - in memory, before
		// writing anything to disk - that reversing our own freshly-
		// produced output would reconstruct the true original. A failure
		// aborts the rotate rather than producing a file this tool claims
		// to be able to reverse but actually can't.
		if err := verifyReversible(out, heicrotate.ReverseOptions{}); err != nil {
			return 0, &heicrotate.ProvenanceError{
				Msg: "post-rotate reversibility self-check failed - refusing " +
					"to write output: " + errorText(err),
				Err: err,
			}
		}
		say("[ok] reversibility self-check passed - output can be " +
			"fully reversed back to the original file")

		outPath = a.output
		if outPath == "" {
			outPath = defaultRotateOutput(a.input)
		}
	} else {
		var err error
		out, err = heicrotate.ReverseRotation(data, heicrotate.ReverseOptions{IgnoreTamper: a.ignoreTamperCheck, Log: log})
		if err != nil {
			return 0, err
		}
		if err := heicrotate.VerifyStructure(out); err != nil {
			return 0, err
		}
		say("[ok] output box structure is internally consistent")
		outPath = a.output
		if outPath == "" {
			outPath = defaultReverseOutput(a.input)
		}
	}

	if _, err := os.Stat(outPath); err == nil && !a.force { // os.path.exists
		return 0, &refuseOverwriteError{fmt.Sprintf("refusing to overwrite existing file '%s' "+
			"- use -f/--force to overwrite it", outPath)}
	}

	if a.dryRun {
		say("[dry-run] %s would succeed - would write %s (%d bytes, %+d vs input). No file written.",
			a.cmd, outPath, len(out), len(out)-len(data))
		return 0, nil
	}

	if err := os.WriteFile(outPath, out, 0o666); err != nil {
		return 0, err
	}
	say("[ok] wrote %s (%d bytes, %+d vs input)", outPath, len(out), len(out)-len(data))
	return 0, nil
}

// pathStem strips the input's extension, taken from its base name only - a
// dot in a directory name never counts, and neither does a dot-file's
// leading dot (docs/COMPAT.md D1; Python uses input.rsplit('.', 1)[0] on
// the whole path). For every ordinary "dir/name.ext" the result is the same.
func pathStem(input string) string {
	ext := filepath.Ext(input)
	base := input[strings.LastIndexFunc(input, func(r rune) bool { return r < 0x80 && os.IsPathSeparator(byte(r)) })+1:]
	if ext == "" || ext == base {
		return input
	}
	return strings.TrimSuffix(input, ext)
}

// defaultRotateOutput is rotate's default output path: <input>_rotated.heic,
// or - if the input looks like reverse's own default output - the
// '_restored' suffix stripped instead.
func defaultRotateOutput(input string) string {
	stem := pathStem(input)
	if strings.HasSuffix(stem, "_restored") {
		return strings.TrimSuffix(stem, "_restored") + ".heic"
	}
	return stem + "_rotated.heic"
}

// defaultReverseOutput is reverse's default output path: <input>_restored.heic.
func defaultReverseOutput(input string) string {
	return pathStem(input) + "_restored.heic"
}
