package heicrotate

import (
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestVersionIsMajorMinorPatch(t *testing.T) {
	// Upstream's _version_tuple() requires MAJOR.MINOR.PATCH with each
	// component in 0-255 (one TVER byte each).
	m := regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)$`).FindStringSubmatch(Version)
	if m == nil {
		t.Fatalf("Version = %q, want MAJOR.MINOR.PATCH", Version)
	}
	for _, part := range m[1:] {
		if n, err := strconv.Atoi(part); err != nil || n > 255 {
			t.Errorf("Version component %q doesn't fit in a TVER byte (0-255)", part)
		}
	}
}

func TestGoVersionFormat(t *testing.T) {
	re := regexp.MustCompile(`^\d+\.\d+\.\d+\+go\.[1-9]\d*$`)
	if !re.MatchString(GoVersion) {
		t.Fatalf("GoVersion = %q, want <upstream>+go.<N>", GoVersion)
	}
	if !strings.HasPrefix(GoVersion, Version+"+go.") {
		t.Fatalf("GoVersion = %q does not extend Version %q", GoVersion, Version)
	}
}

func TestGoVersionString(t *testing.T) {
	want := []string{
		"heic-lossless-rotate (Go port) " + GoVersion,
		"implements upstream heic-lossless-rotate " + Version,
		"built with " + runtime.Version(),
	}
	got := strings.Split(GoVersionString(), "\n")
	if len(got) != len(want) {
		t.Fatalf("GoVersionString() has %d lines, want %d:\n%s", len(got), len(want), GoVersionString())
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i+1, got[i], want[i])
		}
	}
}
