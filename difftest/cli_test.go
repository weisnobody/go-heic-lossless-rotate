//go:build difftest

package difftest

// The Phase 3 gate: the Go CLI (cli.Main) against upstream's CLI, run as
// `heic-rotate argv...` in twin directories. For every case: the exit code,
// stdout, stderr, and the resulting directory tree must match, except where
// docs/COMPAT.md allows a difference:
//   - help/usage text (SPEC C6): both must print "usage:" help to stdout for
//     -h, and something to stderr for a usage error (exit 2);
//   - E1: -G/--goversion is Go-only;
//   - D1: default output naming for dotted directories and dot-files;
//   - D2: where Python crashes with a traceback (exit 1), Go reports
//     "error: ..." and exits 10.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/pjg/go-heic-lossless-rotate/internal/cli"
	"github.com/pjg/go-heic-lossless-rotate/internal/synth"
	"github.com/pjg/go-heic-lossless-rotate/pkg/core"
	"github.com/pjg/go-heic-lossless-rotate/pkg/version"
)

type cliCase struct {
	group  string
	argv   []string
	setup  func(t *testing.T, dir string) // populates the case directory
	compat string                         // "", "E1", or "D1"
}

type cliOutcome struct {
	Code   int    `json:"code"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

// tree returns every entry under dir: relative path -> a description of
// its type, permission bits, and contents (or symlink target). Comparing
// trees therefore checks what Python's in-place write and Go's atomic one
// leave behind: bytes, modes, symlinks still being symlinks, and hard
// links still sharing content.
func tree(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.Walk(dir, func(p string, info os.FileInfo, err error) error { // Walk uses Lstat
		if err != nil || p == dir {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(p)
			out[rel] = "symlink -> " + target
			return err
		case info.IsDir():
			out[rel] = fmt.Sprintf("dir %v", info.Mode().Perm())
			return nil
		}
		b, err := os.ReadFile(p)
		out[rel] = fmt.Sprintf("file %v\n%s", info.Mode().Perm(), b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func runGoCLI(t *testing.T, dir string, argv []string) cliOutcome {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	var stdout, stderr bytes.Buffer
	code := cli.Main(append([]string{"heic-rotate"}, argv...), &stdout, &stderr)
	return cliOutcome{code, stdout.String(), stderr.String()}
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func files(fs map[string][]byte) func(*testing.T, string) {
	return func(t *testing.T, dir string) {
		for name, data := range fs {
			if data == nil {
				if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
					t.Fatal(err)
				}
				continue
			}
			writeFile(t, filepath.Join(dir, name), data)
		}
	}
}

// compareCLI returns "" if Go's outcome matches Python's under the rules
// above.
func compareCLI(c cliCase, py, gor cliOutcome, pyTree, goTree map[string]string) string {
	switch c.compat {
	case "E1":
		if py.Code != 2 || gor.Code != 0 || gor.Stdout != version.GoVersionString()+"\n" {
			return fmt.Sprintf("E1: python exit %d, go exit %d stdout %q", py.Code, gor.Code, gor.Stdout)
		}
		return ""
	case "D1":
		// Same outcome, same output bytes - under each tool's own name.
		values := func(m map[string]string) []string {
			var v []string
			for _, s := range m {
				v = append(v, s)
			}
			sort.Strings(v)
			return v
		}
		if py.Code != gor.Code || !equalStrings(values(pyTree), values(goTree)) {
			return fmt.Sprintf("D1: exit %d/%d, trees %v / %v", py.Code, gor.Code, keys(pyTree), keys(goTree))
		}
		return ""
	}

	if py.Code == 1 && strings.Contains(py.Stderr, "Traceback") { // D2
		if gor.Code != core.ErrorExitCode || !strings.HasPrefix(gor.Stderr, "error: ") {
			return fmt.Sprintf("D2: python traceback, go exit %d stderr %q", gor.Code, gor.Stderr)
		}
	} else {
		if py.Code != gor.Code {
			return fmt.Sprintf("exit: python %d, go %d\n  python stderr: %q\n  go stderr:     %q", py.Code, gor.Code, py.Stderr, gor.Stderr)
		}
		switch {
		case strings.HasPrefix(py.Stdout, "usage:"):
			if !strings.HasPrefix(gor.Stdout, "usage:") {
				return fmt.Sprintf("python printed help, go stdout %q", gor.Stdout)
			}
		case py.Stdout != gor.Stdout:
			return fmt.Sprintf("stdout:\n  python %q\n  go     %q", py.Stdout, gor.Stdout)
		}
		switch {
		case py.Code == 2:
			if (py.Stderr == "") != (gor.Stderr == "") {
				return fmt.Sprintf("usage error stderr: python %q, go %q", py.Stderr, gor.Stderr)
			}
		case py.Stderr != gor.Stderr:
			return fmt.Sprintf("stderr:\n  python %q\n  go     %q", py.Stderr, gor.Stderr)
		}
	}
	for name := range mergeKeys(pyTree, goTree) {
		if pyTree[name] != goTree[name] {
			return fmt.Sprintf("file %q differs (python %d bytes, go %d bytes)", name, len(pyTree[name]), len(goTree[name]))
		}
	}
	return ""
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func keys(m map[string]string) []string {
	var k []string
	for name := range m {
		k = append(k, name)
	}
	sort.Strings(k)
	return k
}

func mergeKeys(a, b map[string]string) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// isGoVersionArgv: -G/--goversion (or an abbreviation) before the subcommand.
func isGoVersionArgv(argv []string) bool {
	for _, a := range argv {
		if !strings.HasPrefix(a, "-") || a == "--" {
			return false
		}
		if a == "-G" || (len(a) > 3 && strings.HasPrefix("--goversion", a)) {
			return true
		}
	}
	return false
}

func TestCLIMatchesPython(t *testing.T) {
	py := Upstream(t)
	if !atLeast(py.Version, 3, 14) {
		t.Skipf("the Go CLI follows Python 3.14's argparse; this interpreter is %s (docs/COMPAT.md)", py.Version)
	}

	base := synth.DefaultOptions()
	base.Layout, base.IncludeExif = synth.MetaFirst, true
	x, err := synth.BuildSyntheticHEIC(base)
	if err != nil {
		t.Fatal(err)
	}
	var cases []cliCase
	add := func(group string, setup func(*testing.T, string), argv ...string) {
		compat := ""
		if isGoVersionArgv(argv) {
			compat = "E1"
		}
		cases = append(cases, cliCase{group: group, argv: argv, setup: setup, compat: compat})
	}
	withX := files(map[string][]byte{"x": x})

	// 1. Every argv captured by the parser probe (internal/cli/testdata), with a
	//    real input file "x".
	var probe []struct {
		Argv []string `json:"argv"`
	}
	raw, err := os.ReadFile("../internal/cli/testdata/argparse_py314.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		t.Fatal(err)
	}
	for _, p := range probe {
		add("argv table", withX, p.Argv...)
	}

	// 2. Generated argv sequences from a token pool.
	pool := []string{"rotate", "reverse", "info", "90", "180", "0", "45", "x", "y", "z", "-q", "-f",
		"--dry-run", "--dry", "-h", "-V", "--bogus", "--", "-qf", "--no-exif-sync", "--ignore-tamper-check",
		"--ign", "-5", "", "-", "--quiet=1", "-qz", "-x.heic", "--force", "--q", "-fq",
		"-hz", "-zq", "-q=1", "-Vz", "--he", "-quiet", "-f-q", "270", "-1.5", "--no", "--dry-run="}
	rng := rand.New(rand.NewPCG(3, 14))
	for range 2000 {
		argv := make([]string, 1+rng.IntN(6))
		for i := range argv {
			argv[i] = pool[rng.IntN(len(pool))]
		}
		add("generated argv", withX, argv...)
	}

	// 3. Operational corpus over fixtures.
	type fixture struct {
		name string
		data []byte
	}
	var fixtures []fixture
	for _, layout := range []synth.Layout{synth.MdatFirst, synth.MetaFirst} {
		for _, irot := range []bool{false, true} {
			for _, exif := range []string{"none", "end", "middle"} {
				o := synth.DefaultOptions()
				o.Layout, o.IncludeIrot, o.IrotValue = layout, irot, 1
				if exif != "none" {
					o.IncludeExif, o.ExifPosition = true, synth.ExifPosition(exif)
				}
				d, err := synth.BuildSyntheticHEIC(o)
				if err != nil {
					t.Fatal(err)
				}
				fixtures = append(fixtures, fixture{fmt.Sprintf("%s/irot=%v/exif=%s", layout, irot, exif), d})
			}
		}
	}
	for _, f := range fixtures {
		r1 := mustApply(t, f.data, 1)
		r2 := mustApply(t, r1, 2)
		tampered := bytes.Clone(r1)
		tampered[10] ^= 0xFF
		set := files(map[string][]byte{"in.heic": f.data, "r.heic": r1, "r2.heic": r2, "t.heic": tampered,
			"photo_restored.heic": f.data, "adir": nil})
		g := "operations " + f.name
		for _, angle := range []string{"0", "90", "180", "270"} {
			add(g, set, "rotate", angle, "in.heic", "out.heic")
		}
		add(g, set, "rotate", "90", "in.heic", "out.heic", "--dry-run")
		add(g, set, "rotate", "90", "in.heic", "out.heic", "--no-exif-sync")
		add(g, set, "rotate", "90", "in.heic", "out.heic", "-q")
		add(g, set, "90", "in.heic")
		add(g, set, "rotate", "270", "photo_restored.heic")
		add(g, set, "rotate", "90", "in.heic", "in.heic", "-f")
		add(g, set, "rotate", "90", "in.heic", "r.heic")
		add(g, set, "rotate", "90", "in.heic", "r.heic", "-f")
		add(g, set, "reverse", "in.heic")
		add(g, set, "reverse", "r.heic")
		add(g, set, "reverse", "r.heic", "out.heic")
		add(g, set, "reverse", "r2.heic", "out.heic", "-q")
		add(g, set, "reverse", "r.heic", "--dry-run")
		add(g, set, "reverse", "r.heic", "r.heic", "-f")
		add(g, set, "reverse", "t.heic", "out.heic")
		add(g, set, "reverse", "t.heic", "out.heic", "--ignore-tamper-check")
		for _, name := range []string{"in.heic", "r.heic", "r2.heic", "t.heic"} {
			add(g, set, "info", name)
			add(g, set, "info", name, "-q")
		}
		add(g, set, "rotate", "90", "r.heic", "out.heic")
		add(g, set, "rotate", "90", "in.heic", "adir", "-f")
	}

	// 4. Errors: missing files, directories, unwritable outputs, malformed
	//    input (including D2 crashes), and D1 naming.
	pcrcShort := editProvenance(t, mustApply(t, fixtures[0].data, 1), func(_ *uint8, e core.ProvenanceEntries) {
		e["PCRC"] = e["PCRC"][:3]
	})
	errSet := files(map[string][]byte{"in.heic": fixtures[0].data, "adir": nil, "empty.heic": {}, "junk.heic": []byte("not a heic file"),
		"pcrc.heic": pcrcShort, "dir.v2/photo": fixtures[0].data, ".hidden": fixtures[0].data})
	for _, argv := range [][]string{
		{"info", "missing.heic"}, {"info", "adir"}, {"info", "empty.heic"}, {"info", "junk.heic"},
		{"rotate", "90", "in.heic", "nodir/out.heic"}, {"rotate", "90", "missing.heic"},
		{"reverse", "junk.heic"}, {"info", "pcrc.heic"}, {"reverse", "pcrc.heic", "out.heic", "--ignore-tamper-check"},
		{"info", "in.heic/x"},
	} {
		add("errors", errSet, argv...)
	}
	cases = append(cases,
		cliCase{group: "D1 naming", argv: []string{"rotate", "90", "dir.v2/photo"}, setup: errSet, compat: "D1"},
		cliCase{group: "D1 naming", argv: []string{"rotate", "90", ".hidden"}, setup: errSet, compat: "D1"},
	)

	// 5. D3: Go's atomic write must leave the same tree as Python's
	//    in-place open('wb') - through symlinks, keeping modes, failing on
	//    read-only outputs and directories, keeping hard links shared.
	r1 := mustApply(t, fixtures[0].data, 1)
	d3Set := func(t *testing.T, dir string) {
		files(map[string][]byte{"in.heic": fixtures[0].data, "r.heic": r1, "target.heic": []byte("old target"),
			"m640.heic": []byte("old"), "ro.heic": []byte("old"), "hl.heic": []byte("old"), "rodir": nil})(t, dir)
		must := func(err error) {
			if err != nil {
				t.Fatal(err)
			}
		}
		must(os.Symlink("target.heic", filepath.Join(dir, "link.heic")))
		must(os.Symlink("missing_target.heic", filepath.Join(dir, "dangling.heic")))
		must(os.Symlink("loop2", filepath.Join(dir, "loop1")))
		must(os.Symlink("loop1", filepath.Join(dir, "loop2")))
		must(os.Link(filepath.Join(dir, "hl.heic"), filepath.Join(dir, "hl2.heic")))
		must(os.Chmod(filepath.Join(dir, "m640.heic"), 0o640))
		must(os.Chmod(filepath.Join(dir, "ro.heic"), 0o444))
		must(os.Chmod(filepath.Join(dir, "rodir"), 0o555))
		t.Cleanup(func() { os.Chmod(filepath.Join(dir, "rodir"), 0o755) })
	}
	for _, argv := range [][]string{
		{"rotate", "90", "in.heic", "link.heic", "-f"},
		{"rotate", "90", "in.heic", "link.heic"},
		{"rotate", "90", "in.heic", "dangling.heic"},
		{"rotate", "90", "in.heic", "loop1"},
		{"rotate", "90", "in.heic", "m640.heic", "-f"},
		{"rotate", "90", "in.heic", "hl.heic", "-f"},
		{"rotate", "90", "in.heic", "rodir/out.heic"},
		{"reverse", "r.heic", "link.heic", "-f"},
		{"rotate", "90", "link.heic", "link.heic", "-f"},
	} {
		add("D3 writes", d3Set, argv...)
	}
	if os.Geteuid() != 0 { // root can write read-only files
		add("D3 writes", d3Set, "rotate", "90", "in.heic", "ro.heic", "-f")
	}

	// Run Python for every case in one driver process, then Go.
	root := t.TempDir()
	reqs := make([]Request, len(cases))
	pyDirs, goDirs := make([]string, len(cases)), make([]string, len(cases))
	for i, c := range cases {
		pyDirs[i], goDirs[i] = filepath.Join(root, fmt.Sprintf("%d-py", i)), filepath.Join(root, fmt.Sprintf("%d-go", i))
		for _, d := range []string{pyDirs[i], goDirs[i]} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
			c.setup(t, d)
		}
		reqs[i] = Request{ID: fmt.Sprintf("cli#%d", i), Op: "cli", Args: map[string]any{"argv": c.argv, "cwd": pyDirs[i]}}
	}
	resps := py.Drive(t, nil, reqs)

	type stats struct {
		n, bad  int
		compat  map[string]int
		details []string
	}
	byGroup := map[string]*stats{}
	var order []string
	for i, c := range cases {
		g := c.group
		if strings.HasPrefix(g, "operations ") {
			g = "operations"
		}
		st := byGroup[g]
		if st == nil {
			st = &stats{compat: map[string]int{}}
			byGroup[g] = st
			order = append(order, g)
		}
		st.n++
		if resps[i].Err != "" {
			t.Fatalf("driver error for %q: %s", c.argv, resps[i].Err)
		}
		var pyOut cliOutcome
		if err := json.Unmarshal(resps[i].JSON, &pyOut); err != nil {
			t.Fatal(err)
		}
		goOut := runGoCLI(t, goDirs[i], c.argv)
		if problem := compareCLI(c, pyOut, goOut, tree(t, pyDirs[i]), tree(t, goDirs[i])); problem != "" {
			st.bad++
			if len(st.details) < 5 {
				st.details = append(st.details, fmt.Sprintf("%s %q:\n%s", c.group, c.argv, problem))
			}
			continue
		}
		switch {
		case c.compat != "":
			st.compat[c.compat]++
		case pyOut.Code == 1 && strings.Contains(pyOut.Stderr, "Traceback"):
			st.compat["D2"]++
		}
	}
	for _, g := range order {
		st := byGroup[g]
		t.Run(strings.ReplaceAll(g, " ", "_"), func(t *testing.T) {
			t.Logf("%d cases, allowed divergences: %v", st.n, st.compat)
			if st.bad > 0 {
				t.Errorf("%d of %d cases differ from Python:\n%s", st.bad, st.n, strings.Join(st.details, "\n---\n"))
			}
		})
	}
	t.Logf("%d CLI cases, Python %s", len(cases), py.Version)
}

// TestCLIRealProcess cross-checks the in-process Python runner against real
// `python -m heic_rotate` subprocesses for a few cases.
func TestCLIRealProcess(t *testing.T) {
	py := Upstream(t)
	x, err := synth.BuildSyntheticHEIC(synth.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"-V"}, {"rotate", "90", "x", "y"}, {"info", "x"}, {"bogus"}, {"info", "missing.heic"}, {"rotate", "45", "x"},
	} {
		pyDir, goDir := t.TempDir(), t.TempDir()
		writeFile(t, filepath.Join(pyDir, "x"), x)
		writeFile(t, filepath.Join(goDir, "x"), x)
		r := py.CLI(t, pyDir, argv...)
		pyOut := cliOutcome{r.Code, string(r.Stdout), string(r.Stderr)}
		goOut := runGoCLI(t, goDir, argv)
		if problem := compareCLI(cliCase{argv: argv}, pyOut, goOut, tree(t, pyDir), tree(t, goDir)); problem != "" {
			t.Errorf("%q: %s", argv, problem)
		}
	}
}
