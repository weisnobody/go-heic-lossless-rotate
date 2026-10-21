package files

// The rotate/reverse/info commands as library calls, doing everything
// cli.py's _run does around the core - reading the input, the structure
// check and post-rotate self-check, default output naming, overwrite
// refusal, dry-run, and the write - with the same messages through
// FileOptions.Log, so the CLI is a thin layer over it.
//
// Writes are atomic (docs/COMPAT.md D3): a crash or full disk can't leave a
// truncated output, and in-place edits (input == output) can't destroy the
// original. They otherwise behave like Python's in-place open(path, 'wb'):
// writes go through symlinks, a read-only or directory output fails the same
// way, an overwritten file keeps its mode (and, best effort, owner), new
// files get 0666 minus the umask, and a hard-linked output is updated in
// place so its other links see the change.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/weisnobody/go-heic-lossless-rotate/pkg/core"
	"github.com/weisnobody/go-heic-lossless-rotate/pkg/stream"
)

// FileOptions are RotateFile's and ReverseFile's options - the CLI's flags.
// Fields that don't apply to a function are ignored.
type FileOptions struct {
	Force        bool      // overwrite an existing output (-f/--force)
	DryRun       bool      // do everything except write the output (--dry-run)
	NoExifSync   bool      // RotateFile only: don't sync the legacy Exif Orientation tag (--no-exif-sync)
	IgnoreTamper bool      // ReverseFile only: reverse despite a CCRC mismatch (--ignore-tamper-check)
	Log          io.Writer // receives the CLI's informational lines; nil = silent (-q)
}

// Result describes the output of RotateFile or ReverseFile. On a dry run it
// describes what would have been written.
type Result struct {
	Output    string // the output path, as given or defaulted
	Size      int    // output size in bytes
	InputSize int    // input size in bytes; Size-InputSize is the "+N vs input" delta
	DryRun    bool   // true if nothing was written
}

// OutputExistsError reports that the output already exists and
// FileOptions.Force wasn't set. errors.Is(err, fs.ErrExist) is true.
type OutputExistsError struct {
	Path string
}

func (e *OutputExistsError) Error() string {
	return fmt.Sprintf("refusing to overwrite existing file '%s' - use -f/--force to overwrite it", e.Path)
}

// Is makes errors.Is(err, fs.ErrExist) true.
func (e *OutputExistsError) Is(target error) bool { return target == fs.ErrExist }

// verifyReversibleHook is RotateFile's post-rotate self-check, indirected so
// tests can force a failure (upstream patches cli.verify_reversible).
var verifyReversibleHook = func(out *stream.View) error {
	return stream.VerifyReversibleReaderAt(out, out.Size(), core.ReverseOptions{})
}

// logger writes the CLI's stdout lines; a nil writer is silent.
type logger struct{ w io.Writer }

func (l logger) printf(format string, args ...any) {
	if l.w != nil {
		fmt.Fprintf(l.w, format+"\n", args...)
	}
}

// input is an opened input file for the streaming engine.
type input struct {
	r     io.ReaderAt
	size  int64
	info  fs.FileInfo // nil when the input isn't a regular file
	close func()
}

// openInput opens path for the streaming engine. A non-regular file (a
// pipe, a device - or a directory, which then fails exactly as Python's
// open(path, 'rb') does) is read into memory instead.
func openInput(path string) (*input, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	if fi, err := f.Stat(); err == nil && fi.Mode().IsRegular() {
		return &input{r: f, size: fi.Size(), info: fi, close: func() { f.Close() }}, nil
	}
	f.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return &input{r: bytes.NewReader(data), size: int64(len(data)), close: func() {}}, nil
}

// RotateFile rotates input by deltaTurns quarter turns COUNTER-CLOCKWISE on
// top of its current rotation and writes the result to output (""
// means DefaultRotateOutput(input)) - the rotate half of cli.py's _run.
//
// Before anything is written it checks the output's box structure and
// reverses the output in memory to confirm it reconstructs the original
// exactly; a failed self-check is a *ProvenanceError and nothing is
// written.
func RotateFile(input, output string, deltaTurns int, opts FileOptions) (*Result, error) {
	in, err := openInput(input)
	if err != nil {
		return nil, err
	}
	defer in.close()
	log := logger{opts.Log}
	out, err := stream.ApplyRotationReaderAt(in.r, in.size, deltaTurns, core.RotateOptions{NoExifSync: opts.NoExifSync, Log: opts.Log})
	if err != nil {
		return nil, err
	}
	if err := stream.VerifyStructureView(out); err != nil {
		return nil, err
	}
	log.printf("[ok] output box structure is internally consistent")

	// Post-rotate reversibility self-check: verify - in memory, before
	// writing anything - that reversing our own freshly-produced output
	// reconstructs the true original, rather than producing a file this
	// tool claims to be able to reverse but actually can't.
	if err := verifyReversibleHook(out); err != nil {
		return nil, &core.ProvenanceError{
			Msg: "post-rotate reversibility self-check failed - refusing " +
				"to write output: " + errorMessage(err),
			Err: err,
		}
	}
	log.printf("[ok] reversibility self-check passed - output can be " +
		"fully reversed back to the original file")

	if output == "" {
		output = DefaultRotateOutput(input)
	}
	return finishWrite("rotate", output, out, in, opts)
}

// ReverseFile undoes every heic-lossless-rotate edit on input, writing the
// byte-identical original to output ("" means DefaultReverseOutput(input))
// - the reverse half of cli.py's _run.
func ReverseFile(input, output string, opts FileOptions) (*Result, error) {
	in, err := openInput(input)
	if err != nil {
		return nil, err
	}
	defer in.close()
	log := logger{opts.Log}
	out, err := stream.ReverseRotationReaderAt(in.r, in.size, core.ReverseOptions{IgnoreTamper: opts.IgnoreTamper, Log: opts.Log})
	if err != nil {
		return nil, err
	}
	if err := stream.VerifyStructureView(out); err != nil {
		return nil, err
	}
	log.printf("[ok] output box structure is internally consistent")
	if output == "" {
		output = DefaultReverseOutput(input)
	}
	return finishWrite("reverse", output, out, in, opts)
}

// InfoFile reads path and returns GatherInfo's summary of it.
func InfoFile(path string) (*core.Info, error) {
	in, err := openInput(path)
	if err != nil {
		return nil, err
	}
	defer in.close()
	return stream.GatherInfoReaderAt(in.r, in.size)
}

// errorMessage is Python's str(e) for this package's error types.
func errorMessage(err error) string {
	var be *core.BoxParseError
	var pe *core.ProvenanceError
	switch {
	case errors.As(err, &be):
		return be.Msg
	case errors.As(err, &pe):
		return pe.Msg
	}
	return err.Error()
}

// finishWrite is _run's shared tail: overwrite refusal, dry-run, write.
func finishWrite(cmd, output string, out *stream.View, in *input, opts FileOptions) (*Result, error) {
	log := logger{opts.Log}
	size, inputSize := int(out.Size()), int(in.size)
	if _, err := os.Stat(output); err == nil && !opts.Force { // os.path.exists
		return nil, &OutputExistsError{output}
	}
	res := &Result{Output: output, Size: size, InputSize: inputSize, DryRun: opts.DryRun}
	if opts.DryRun {
		log.printf("[dry-run] %s would succeed - would write %s (%d bytes, %+d vs input). No file written.",
			cmd, output, size, size-inputSize)
		return res, nil
	}
	if err := writeOutput(output, out, in); err != nil {
		return nil, err
	}
	log.printf("[ok] wrote %s (%d bytes, %+d vs input)", output, size, size-inputSize)
	return res, nil
}

// pathStem strips the input's extension, taken from its base name only - a
// dot in a directory name never counts, and neither does a dot-file's
// leading dot (docs/COMPAT.md D1; Python uses input.rsplit('.', 1)[0] on the
// whole path). For every ordinary "dir/name.ext" the result is the same.
func pathStem(input string) string {
	ext := filepath.Ext(input)
	base := input[strings.LastIndexFunc(input, func(r rune) bool { return r < 0x80 && os.IsPathSeparator(byte(r)) })+1:]
	if ext == "" || ext == base {
		return input
	}
	return strings.TrimSuffix(input, ext)
}

// DefaultRotateOutput is rotate's default output path:
// <input stem>_rotated.heic - or, if the input looks like reverse's own
// default output (<stem>_restored), that suffix stripped instead.
func DefaultRotateOutput(input string) string {
	stem := pathStem(input)
	if strings.HasSuffix(stem, "_restored") {
		return strings.TrimSuffix(stem, "_restored") + ".heic"
	}
	return stem + "_rotated.heic"
}

// DefaultReverseOutput is reverse's default output path:
// <input stem>_restored.heic.
func DefaultReverseOutput(input string) string {
	return pathStem(input) + "_restored.heic"
}

// testHookBeforeRename, if set, runs after the temp file is fully written
// and before it replaces the target (tests inject failures here).
var testHookBeforeRename func() error

// asOpenError re-labels an error from any step of writing as Python's
// open(path, 'wb') failure for the output path the caller gave, so messages
// name that path rather than a temp file or symlink target.
func asOpenError(err error, path string) error {
	var pe *fs.PathError
	var le *os.LinkError
	switch {
	case errors.As(err, &pe):
		return &fs.PathError{Op: "open", Path: path, Err: pe.Err}
	case errors.As(err, &le):
		return &fs.PathError{Op: "open", Path: path, Err: le.Err}
	}
	return err
}

// resolveSymlinks follows path through any symlinks to the file a write
// would land on - which needn't exist (a dangling link's target).
func resolveSymlinks(path string) (string, error) {
	p := path
	for hops := 0; ; hops++ {
		fi, err := os.Lstat(p)
		if err != nil || fi.Mode()&fs.ModeSymlink == 0 {
			return p, nil // a regular file, a directory, or nothing yet
		}
		if hops == 40 {
			return "", &fs.PathError{Op: "open", Path: path, Err: syscall.ELOOP}
		}
		link, err := os.Readlink(p)
		if err != nil {
			return "", asOpenError(err, path)
		}
		if !filepath.IsAbs(link) {
			link = filepath.Join(filepath.Dir(p), link)
		}
		p = link
	}
}

// writeOutput writes out to path atomically, with Python's in-place
// open(path, 'wb') semantics for everything visible (see the file comment).
// out may read from in, which is closed before anything replaces or
// overwrites a file (Windows can't rename over an open file).
func writeOutput(path string, out *stream.View, in *input) error {
	target, err := resolveSymlinks(path)
	if err != nil {
		return err
	}
	existing, err := os.Stat(target)
	switch {
	case err == nil:
		if existing.IsDir() {
			return &fs.PathError{Op: "open", Path: path, Err: syscall.EISDIR}
		}
		// A file Python couldn't open for writing fails here too, rather
		// than being silently replaced by the rename.
		f, err := os.OpenFile(target, os.O_WRONLY, 0)
		if err != nil {
			return asOpenError(err, path)
		}
		f.Close()
		if linkCount(target, existing) > 1 { // keep the other hard links: write in place
			if in.info != nil && os.SameFile(existing, in.info) {
				// Overwriting the very file out reads from: materialize first.
				data, err := out.Bytes()
				if err != nil {
					return err
				}
				out = stream.ViewOfBytes(data)
				in.close() // nothing reads from it now; release it before writing
			}
			return writeInPlace(target, path, out)
		}
	case errors.Is(err, fs.ErrNotExist):
		existing = nil
	default:
		return asOpenError(err, path)
	}

	tmp, err := createTemp(filepath.Dir(target))
	if err != nil {
		return asOpenError(err, path)
	}
	tmpName := tmp.Name()
	fail := func(err error) error {
		tmp.Close()
		os.Remove(tmpName)
		return asOpenError(err, path)
	}
	if _, err := out.WriteTo(tmp); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if existing != nil {
		copyOwner(tmp, existing) // before chmod: chown can clear setuid/setgid
		if err := tmp.Chmod(existing.Mode() & (fs.ModePerm | fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)); err != nil {
			return fail(err)
		}
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return asOpenError(err, path)
	}
	in.close()
	if testHookBeforeRename != nil {
		if err := testHookBeforeRename(); err != nil {
			os.Remove(tmpName)
			return err
		}
	}
	if err := os.Rename(tmpName, target); err != nil {
		os.Remove(tmpName)
		return asOpenError(err, path)
	}
	syncDir(filepath.Dir(target))
	return nil
}

// writeInPlace is Python's exact open(path, 'wb') semantics, used for a
// hard-linked target so its other links see the new content.
func writeInPlace(target, path string, out *stream.View) error {
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_TRUNC, 0o666)
	if err != nil {
		return asOpenError(err, path)
	}
	if _, err := out.WriteTo(f); err != nil {
		f.Close()
		return asOpenError(err, path)
	}
	return asOpenError(f.Close(), path)
}

// createTemp creates a new temp file in dir with mode 0666 minus the umask
// - what Python's open(path, 'wb') gives a new file (os.CreateTemp would
// force 0600).
func createTemp(dir string) (*os.File, error) {
	for range 100 {
		name := filepath.Join(dir, fmt.Sprintf(".heic-rotate-%016x", rand.Uint64()))
		f, err := os.OpenFile(name, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o666)
		if !errors.Is(err, fs.ErrExist) {
			return f, err
		}
	}
	return nil, &fs.PathError{Op: "open", Path: dir, Err: fs.ErrExist}
}
