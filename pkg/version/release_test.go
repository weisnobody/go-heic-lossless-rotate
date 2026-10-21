package version

import (
	"os"
	"strconv"
	"testing"
)

// expectedTag is the release tag for a Go port version: plain vX.Y.Z for
// the first Go release of an upstream version, vX.Y.Z+go.N after that
// (docs/DESIGN.md, "Go port version and release tags").
func expectedTag(version string, revision int) string {
	if revision == 1 {
		return "v" + version
	}
	return "v" + version + "+go." + strconv.Itoa(revision)
}

func TestExpectedTag(t *testing.T) {
	for _, tc := range []struct {
		version  string
		revision int
		want     string
	}{
		{"1.7.1", 1, "v1.7.1"},
		{"1.7.1", 2, "v1.7.1+go.2"},
		{"1.8.0", 12, "v1.8.0+go.12"},
	} {
		if got := expectedTag(tc.version, tc.revision); got != tc.want {
			t.Errorf("expectedTag(%q, %d) = %q, want %q", tc.version, tc.revision, got, tc.want)
		}
	}
	// The tag carries GoVersion, apart from revision 1's dropped +go.1.
	if GoRevision > 1 && expectedTag(Version, GoRevision) != "v"+GoVersion {
		t.Errorf("expectedTag = %q, want v%s", expectedTag(Version, GoRevision), GoVersion)
	}
}

// TestReleaseTag is the release workflow's tag check: it fails unless
// RELEASE_TAG is the tag for this source's version. Without RELEASE_TAG it
// is skipped. Run it with make release-check TAG=vX.Y.Z.
func TestReleaseTag(t *testing.T) {
	tag := os.Getenv("RELEASE_TAG")
	if tag == "" {
		t.Skip("RELEASE_TAG not set")
	}
	if want := expectedTag(Version, GoRevision); tag != want {
		t.Fatalf("tag %s doesn't match GoVersion %s; want %s (or bump GoRevision/Version in pkg/version)", tag, GoVersion, want)
	}
}
