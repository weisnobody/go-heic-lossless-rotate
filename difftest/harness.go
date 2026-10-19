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

	heicrotate "github.com/pjg/go-heic-lossless-rotate"
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
	version := strings.TrimSpace(string(out))
	if !atLeast(version, 3, 11) {
		return nil, fmt.Errorf("%s is Python %s; difftest needs >= 3.11 (for -P)", python, version)
	}

	py := &Py{Python: python, Version: version, Src: src}

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
		if upstreamVersion != heicrotate.Version {
			return nil, fmt.Errorf("upstream VERSION is %s but heicrotate.Version is %s", upstreamVersion, heicrotate.Version)
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
}

// Response is one pydriver.py result: exactly one of Out or Err is set.
type Response struct {
	ID  string
	Out []byte
	Err string // "<PythonExceptionType>: <message>"
}

var validID = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// Drive runs every request through a single pydriver.py process and returns
// the responses in request order.
func (p *Py) Drive(t testing.TB, reqs []Request) []Response {
	t.Helper()
	seen := map[string]bool{}
	for _, r := range reqs {
		if !validID.MatchString(r.ID) || seen[r.ID] {
			t.Fatalf("invalid or duplicate request id %q", r.ID)
		}
		seen[r.ID] = true
	}

	dir := t.TempDir()
	reqPath := filepath.Join(dir, "requests.json")
	outDir := filepath.Join(dir, "out")
	if err := os.Mkdir(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	reqJSON, err := json.Marshal(reqs)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(reqPath, reqJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	driver, err := filepath.Abs("pydriver.py")
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(p.Python, "-P", driver, reqPath, outDir)
	cmd.Dir = dir
	cmd.Env = p.env("src", "tests")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("pydriver.py (Python %s) failed: %v\n%s", p.Version, err, out)
	}

	resps := make([]Response, len(reqs))
	for i, r := range reqs {
		resps[i].ID = r.ID
		if out, err := os.ReadFile(filepath.Join(outDir, r.ID+".bin")); err == nil {
			resps[i].Out = out
			continue
		}
		msg, err := os.ReadFile(filepath.Join(outDir, r.ID+".err"))
		if err != nil {
			t.Fatalf("pydriver.py produced no result for request %q", r.ID)
		}
		resps[i].Err = string(msg)
	}
	return resps
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
