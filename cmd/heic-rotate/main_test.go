package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/weisnobody/go-heic-lossless-rotate/internal/synth"
	"github.com/weisnobody/go-heic-lossless-rotate/pkg/core"
	"github.com/weisnobody/go-heic-lossless-rotate/pkg/version"
)

// TestBinaries builds both commands and runs them as real processes.
func TestBinaries(t *testing.T) {
	if testing.Short() {
		t.Skip("builds binaries")
	}
	bin := t.TempDir()
	build := exec.Command("go", "build", "-o", bin+string(filepath.Separator), "github.com/weisnobody/go-heic-lossless-rotate/cmd/...")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	exe := func(name string) string {
		p := filepath.Join(bin, name)
		if _, err := os.Stat(p + ".exe"); err == nil {
			p += ".exe"
		}
		return p
	}
	run := func(name string, args ...string) (int, string) {
		cmd := exec.Command(exe(name), args...)
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		err := cmd.Run()
		if err != nil {
			if _, ok := err.(*exec.ExitError); !ok {
				t.Fatalf("%s: %v", name, err)
			}
		}
		return cmd.ProcessState.ExitCode(), stdout.String()
	}

	// Both names are the same program and identify as heic-lossless-rotate.
	for _, name := range []string{"heic-rotate", "heic-lossless-rotate"} {
		if rc, out := run(name, "-V"); rc != 0 || out != core.VersionString()+"\n" {
			t.Errorf("%s -V: rc %d, %q", name, rc, out)
		}
		if rc, out := run(name, "-G"); rc != 0 || out != version.GoVersionString()+"\n" {
			t.Errorf("%s -G: rc %d, %q", name, rc, out)
		}
		if rc, _ := run(name); rc != 2 {
			t.Errorf("%s with no args: rc %d, want 2", name, rc)
		}
	}

	// A real rotate writes the same bytes as the library.
	dir := t.TempDir()
	data, err := synth.BuildSyntheticHEIC(synth.DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	in, out := filepath.Join(dir, "in.heic"), filepath.Join(dir, "out.heic")
	if err := os.WriteFile(in, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if rc, _ := run("heic-rotate", "90", in, out, "-q"); rc != 0 {
		t.Fatalf("rotate: rc %d", rc)
	}
	want, err := core.ApplyRotation(data, 1, core.RotateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(out); !bytes.Equal(got, want) {
		t.Error("binary output differs from core.ApplyRotation")
	}
	if rc, _ := run("heic-lossless-rotate", "info", out, "-q"); rc != 1 {
		t.Errorf("info exit = %d, want 1", rc)
	}
}
