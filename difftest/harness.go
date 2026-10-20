//go:build difftest

package difftest

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/pjg/go-heic-lossless-rotate/pkg/core"
	"github.com/pjg/go-heic-lossless-rotate/pkg/version"
)

// Py is a verified handle on the pinned upstream Python implementation.
type Py struct {
	Python  string // interpreter path
	Version string // interpreter version, e.g. "3.14.6"
	Src     string // upstream checkout root
	Commit  string // checked-out upstream commit
}

var (
	upstreamOnce sync.Once
	upstream     *Py
	upstreamErr  error
)

// Upstream returns the verified upstream handle, failing the test if the
// interpreter or checkout is missing or isn't at the pinned commit/version.
// Verification runs once per test binary.
func Upstream(t testing.TB) *Py {
	t.Helper()
	upstreamOnce.Do(func() { upstream, upstreamErr = locateUpstream() })
	if upstreamErr != nil {
		t.Fatalf("difftest: %v", upstreamErr)
	}
	return upstream
}

func locateUpstream() (*Py, error) {
	src := os.Getenv("HEIC_ROTATE_PY_SRC")
	if src == "" {
		return nil, errors.New("HEIC_ROTATE_PY_SRC is not set - run `make difftest`, " +
			"or point it at a heic-lossless-rotate checkout")
	}
	src, err := filepath.Abs(src)
	if err != nil {
		return nil, err
	}
	for _, rel := range []string{"src/heic_rotate/core.py", "src/heic_rotate/_version.py", "tests/synth_heic.py"} {
		if _, err := os.Stat(filepath.Join(src, rel)); err != nil {
			return nil, fmt.Errorf("HEIC_ROTATE_PY_SRC=%s doesn't look like a heic-lossless-rotate checkout: %v", src, err)
		}
	}

	python := os.Getenv("HEIC_ROTATE_PYTHON")
	if python == "" {
		python = "python3"
	}
	python, err = exec.LookPath(python)
	if err != nil {
		return nil, fmt.Errorf("python interpreter: %v", err)
	}
	out, err := exec.Command(python, "-c", "import sys; print('%d.%d.%d' % sys.version_info[:3])").Output()
	if err != nil {
		return nil, fmt.Errorf("%s: can't determine version: %v", python, err)
	}
	pyVersion := strings.TrimSpace(string(out))
	if !atLeast(pyVersion, 3, 11) {
		return nil, fmt.Errorf("%s is Python %s; difftest needs >= 3.11 (for -P)", python, pyVersion)
	}

	py := &Py{Python: python, Version: pyVersion, Src: src}

	pinned, err := os.ReadFile("upstream.commit")
	if err != nil {
		return nil, fmt.Errorf("reading pin: %v", err)
	}
	want := strings.TrimSpace(string(pinned))
	head, err := exec.Command("git", "-C", src, "rev-parse", "HEAD").Output()
	if err != nil {
		return nil, fmt.Errorf("git rev-parse HEAD in %s: %v", src, err)
	}
	py.Commit = strings.TrimSpace(string(head))

	verSrc, err := os.ReadFile(filepath.Join(src, "src/heic_rotate/_version.py"))
	if err != nil {
		return nil, err
	}
	m := regexp.MustCompile(`(?m)^VERSION\s*=\s*['"]([^'"]+)['"]`).FindSubmatch(verSrc)
	if m == nil {
		return nil, errors.New("can't find VERSION in upstream _version.py")
	}
	upstreamVersion := string(m[1])

	if os.Getenv("HEIC_ROTATE_PY_ALLOW_UNPINNED") != "1" {
		if py.Commit != want {
			return nil, fmt.Errorf("upstream checkout %s is at %s, pinned commit is %s (run `make upstream`, "+
				"or set HEIC_ROTATE_PY_ALLOW_UNPINNED=1)", src, py.Commit, want)
		}
		if upstreamVersion != version.Version {
			return nil, fmt.Errorf("upstream VERSION is %s but version.Version is %s", upstreamVersion, version.Version)
		}
	}
	return py, nil
}

func atLeast(version string, major, minor int) bool {
	parts := strings.Split(version, ".")
	if len(parts) < 2 {
		return false
	}
	maj, err1 := strconv.Atoi(parts[0])
	mnr, err2 := strconv.Atoi(parts[1])
	return err1 == nil && err2 == nil && (maj > major || maj == major && mnr >= minor)
}

// env returns a hermetic environment for running upstream Python code with
// the given PYTHONPATH entries (relative to the checkout root).
func (p *Py) env(pathEntries ...string) []string {
	var paths []string
	for _, e := range pathEntries {
		paths = append(paths, filepath.Join(p.Src, e))
	}
	env := []string{}
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "PYTHON") { // drop any inherited PYTHONPATH etc.
			env = append(env, kv)
		}
	}
	return append(env,
		"PYTHONPATH="+strings.Join(paths, string(os.PathListSeparator)),
		"PYTHONDONTWRITEBYTECODE=1", // never write __pycache__ into the checkout
		"PYTHONIOENCODING=utf-8",
		"PYTHONHASHSEED=0",
	)
}

// Result is a completed CLI invocation.
type Result struct {
	Code           int
	Stdout, Stderr []byte
}

// CLI runs `python -P -m heic_rotate args...` in dir, exactly as upstream's
// own test suite runs it (PYTHONPATH pointed at src/).
func (p *Py) CLI(t testing.TB, dir string, args ...string) Result {
	t.Helper()
	cmd := exec.Command(p.Python, append([]string{"-P", "-m", "heic_rotate"}, args...)...)
	cmd.Dir = dir
	cmd.Env = p.env("src")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("running Python CLI: %v", err)
	}
	return Result{Code: cmd.ProcessState.ExitCode(), Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
}

// Request is one pydriver.py operation.
type Request struct {
	ID        string            `json:"id"`
	Op        string            `json:"op"`
	Args      map[string]any    `json:"args,omitempty"`
	BytesArgs map[string][]byte `json:"bytes_args,omitempty"` // base64 in JSON
	InputArgs map[string]string `json:"input_args,omitempty"` // kwarg -> Drive input name
	Timeout   float64           `json:"timeout,omitempty"`    // seconds; 0 = the driver's default (5)
}

// Response is one pydriver.py result.
type Response struct {
	ID   string          `json:"id"`
	Out  []byte          `json:"out"`  // the op's bytes result, if any
	JSON json.RawMessage `json:"json"` // the op's structured result, if any
	Log  string          `json:"log"`  // everything the op printed (verbose output)
	Err  string          `json:"err"`  // "<PythonExceptionType>: <message>", or "" on success
}

var validID = regexp.MustCompile(`^[A-Za-z0-9_.:/=#-]+$`)

// Drive runs every request through a single pydriver.py process and returns
// the responses in request order. inputs are byte strings that requests
// reference by name through InputArgs, so each is sent only once.
func (p *Py) Drive(t testing.TB, inputs map[string][]byte, reqs []Request) []Response {
	t.Helper()
	seen := map[string]bool{}
	for _, r := range reqs {
		if !validID.MatchString(r.ID) || seen[r.ID] {
			t.Fatalf("invalid or duplicate request id %q", r.ID)
		}
		seen[r.ID] = true
		for _, name := range r.InputArgs {
			if _, ok := inputs[name]; !ok {
				t.Fatalf("request %q references unknown input %q", r.ID, name)
			}
		}
	}

	dir := t.TempDir()
	batchPath := filepath.Join(dir, "batch.json")
	resultsPath := filepath.Join(dir, "results.jsonl")
	batch, err := json.Marshal(map[string]any{"inputs": inputs, "requests": reqs})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(batchPath, batch, 0o644); err != nil {
		t.Fatal(err)
	}
	driver, err := filepath.Abs("pydriver.py")
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(p.Python, "-P", driver, batchPath, resultsPath)
	cmd.Dir = dir
	cmd.Env = p.env("src", "tests")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pydriver.py (Python %s) failed: %v\n%s", p.Version, err, out)
	}

	f, err := os.Open(resultsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	resps := make([]Response, len(reqs))
	for i, r := range reqs {
		if err := dec.Decode(&resps[i]); err != nil {
			t.Fatalf("reading pydriver.py result %d (%s): %v", i, r.ID, err)
		}
		if resps[i].ID != r.ID {
			t.Fatalf("pydriver.py result %d is for %q, want %q", i, resps[i].ID, r.ID)
		}
	}
	return resps
}

// Error classes, for ClassifyErr.
const (
	ErrClassMatch = ""   // identical outcome
	CompatD2      = "D2" // Python crashed (IndexError, struct.error, KeyError, ...)
	CompatD4      = "D4" // Python crashed with OverflowError on a negative iloc offset
	CompatD5      = "D5" // Go refused a >64-bit iloc value Python accepted
	CompatD6      = "D6" // Python hung (zero-size 64-bit box)
	CompatD7      = "D7" // Go refused a runaway zero-width iloc extent count
)

// typed reports whether a Python error string is one of upstream's own
// exception types, whose messages the port must reproduce exactly.
func typed(pyErr string) bool {
	return strings.HasPrefix(pyErr, "BoxParseError: ") || strings.HasPrefix(pyErr, "ProvenanceError: ")
}

// goErrString renders a Go error the way pydriver.py renders a Python one.
func goErrString(err error) string {
	var be *core.BoxParseError
	var pe *core.ProvenanceError
	switch {
	case err == nil:
		return ""
	case errors.As(err, &be):
		return "BoxParseError: " + be.Msg
	case errors.As(err, &pe):
		return "ProvenanceError: " + pe.Msg
	default:
		return fmt.Sprintf("%T: %v", err, err)
	}
}

// ClassifyErr compares a Python outcome (pyErr, "" on success) with Go's
// (goErr). ok is false for an unexplained difference. compat names the
// docs/COMPAT.md entry that explains an allowed difference ("" if the
// outcomes are identical):
//   - Python succeeded: Go must succeed - except for the D5/D7 refusals.
//   - Python raised BoxParseError/ProvenanceError: Go must return the same
//     type with the same message.
//   - Python crashed or hung: Go must return a *BoxParseError or
//     *ProvenanceError (any message).
func ClassifyErr(pyErr string, goErr error) (ok bool, compat string) {
	goStr := goErrString(goErr)
	switch {
	case pyErr == "" && goErr == nil:
		return true, ErrClassMatch
	case pyErr == "":
		if strings.Contains(goStr, "doesn't fit in 64 bits") {
			return true, CompatD5
		}
		if strings.Contains(goStr, "zero-width extents") {
			return true, CompatD7
		}
		return false, ""
	case typed(pyErr):
		return pyErr == goStr, ErrClassMatch
	case !typed(goStr):
		return false, ""
	case strings.HasPrefix(pyErr, "TimeoutError: "):
		return true, CompatD6
	case strings.HasPrefix(pyErr, "OverflowError: "):
		return true, CompatD4
	default:
		return true, CompatD2
	}
}

// DiffBytes reports an error if got differs from want, pinpointing the
// first differing offset with surrounding hex context. Returns true if equal.
func DiffBytes(t testing.TB, name string, want, got []byte) bool {
	t.Helper()
	if bytes.Equal(want, got) {
		return true
	}
	t.Errorf("%s: %s", name, describeDiff(want, got))
	return false
}

// describeDiff explains how got differs from want (which must differ).
func describeDiff(want, got []byte) string {
	i := 0
	for i < len(want) && i < len(got) && want[i] == got[i] {
		i++
	}
	const ctx = 16
	lo := max(0, i-ctx)
	window := func(b []byte) string {
		hi := min(len(b), i+ctx)
		if lo >= hi {
			return "(none)"
		}
		return fmt.Sprintf("[%d:%d] % x", lo, hi, b[lo:hi])
	}
	return fmt.Sprintf("first difference at offset %d (python len %d, go len %d)\n  python %s\n  go     %s",
		i, len(want), len(got), window(want), window(got))
}
