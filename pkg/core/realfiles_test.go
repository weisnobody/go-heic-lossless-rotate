package core_test

// ---------------------------------------------------------------------------
// 4. Real-file tests - auto-skip if not present
// ---------------------------------------------------------------------------

import (
	"bytes"
	"fmt"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/weisnobody/go-heic-lossless-rotate/internal/tests"
)

// Both directories are at the module root (gitignored, like upstream's).
const (
	testdataDir = "../../testdata"
	rotatedDir  = "../../rotated"
)

func discoverRealTestFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(testdataDir)
	if err != nil {
		return nil
	}
	var files []string
	for _, e := range entries {
		if strings.HasSuffix(strings.ToLower(e.Name()), ".heic") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files
}

// clearDirContents empties a directory in place - creating it if needed -
// without removing the directory entry itself, so it works identically when
// path is a symlink to a real photo library or scratch disk
// (← _clear_dir_contents).
func clearDirContents(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(path, e.Name())); err != nil {
			t.Fatal(err)
		}
	}
}

// TestRealFiles runs the round-trip check against any real .heic files
// dropped into testdata/, skipping cleanly if there are none.
func TestRealFiles(t *testing.T) {
	files := discoverRealTestFiles(t)

	t.Run("round_trip_every_file_in_testdata", func(t *testing.T) {
		if len(files) == 0 {
			t.Skip("no .heic files in testdata/")
		}
		for _, name := range files {
			t.Run(name, func(t *testing.T) {
				original, err := os.ReadFile(filepath.Join(testdataDir, name))
				if err != nil {
					t.Fatal(err)
				}
				rotated := tests.MustApply(t, original, 1)
				tests.MustVerifyStructure(t, rotated)
				restored := tests.MustReverse(t, rotated)
				if !bytes.Equal(restored, original) {
					t.Errorf("%s: reverse did not reconstruct the exact original", name)
				}
				// confirm the PCRC self-check would also have caught any mismatch
				if crc32.ChecksumIEEE(restored) != crc32.ChecksumIEEE(original) {
					t.Errorf("%s: CRC32 mismatch", name)
				}
			})
		}
	})

	t.Run("write_all_rotations_for_visual_inspection", func(t *testing.T) {
		// Structural round-tripping can't prove the pixels LOOK rotated, so
		// write 0/90/180/270 deg copies to rotated/ for a human to open in an
		// image viewer. rotated/ is cleared and repopulated each run, then
		// left as-is afterward.
		if len(files) == 0 {
			t.Skip("no .heic files in testdata/")
		}
		clearDirContents(t, rotatedDir)
		for _, name := range files {
			original, err := os.ReadFile(filepath.Join(testdataDir, name))
			if err != nil {
				t.Fatal(err)
			}
			ext := filepath.Ext(name)
			stem := strings.TrimSuffix(name, ext)
			for _, angle := range []int{0, 90, 180, 270} {
				rotated := tests.MustApply(t, original, angle/90%4)
				tests.MustVerifyStructure(t, rotated)
				out := filepath.Join(rotatedDir, fmt.Sprintf("%s_rot%d%s", stem, angle, ext))
				if err := os.WriteFile(out, rotated, 0o644); err != nil {
					t.Fatal(err)
				}
			}
		}
		t.Logf("[visual check] wrote rotated test files to %s - open them in an image viewer "+
			"to confirm each displays correctly, then delete the directory when done.", rotatedDir)
	})
}
