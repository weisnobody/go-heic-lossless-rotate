package files_test

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pjg/go-heic-lossless-rotate/internal/synth"
	"github.com/pjg/go-heic-lossless-rotate/pkg/files"
)

// Rotate a file 90 degrees counter-clockwise, inspect it, and restore the
// byte-identical original.
func ExampleRotateFile() {
	dir, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(dir)
	photo := filepath.Join(dir, "photo.heic")
	data, _ := synth.BuildSyntheticHEIC(synth.DefaultOptions()) // stand-in for a real photo
	os.WriteFile(photo, data, 0o644)

	res, err := files.RotateFile(photo, "", 1, files.FileOptions{})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(filepath.Base(res.Output))

	info, _ := files.InfoFile(res.Output)
	fmt.Println("added:", *info.DeltaQuarterTurns*90, "deg")

	restored, _ := files.ReverseFile(res.Output, "", files.FileOptions{})
	original, _ := os.ReadFile(photo)
	again, _ := os.ReadFile(restored.Output)
	fmt.Println("identical:", string(original) == string(again))
	// Output:
	// photo_rotated.heic
	// added: 90 deg
	// identical: true
}

// Log receives exactly what the heic-rotate command prints; with DryRun,
// nothing is written.
func ExampleRotateFile_dryRun() {
	dir, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(dir)
	photo := filepath.Join(dir, "photo.heic")
	data, _ := synth.BuildSyntheticHEIC(synth.DefaultOptions())
	os.WriteFile(photo, data, 0o644)

	var log strings.Builder
	res, err := files.RotateFile(photo, "", 1, files.FileOptions{DryRun: true, Log: &log})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Print(strings.ReplaceAll(log.String(), dir+string(filepath.Separator), "")) // paths relative to dir, for the example
	_, err = os.Stat(res.Output)
	fmt.Println("written:", err == nil)
	// Output:
	// [info] primary item id = 1
	// [info] ipco properties (1-based) = [(1, 'ispe')]
	// [info] primary item associations = [(1, True)]
	// [slow path] no 'irot' property associated with the primary item (implied 0 deg) - inserting one with 90 deg after applying the 90 deg delta
	// [slow path] inserted new 'irot' property (index 2), grew container by 10 bytes
	// [exif sync] no embedded legacy Exif Orientation tag found - nothing to sync
	// [provenance] created provenance record (format v2, 128 bytes)
	// [ok] output box structure is internally consistent
	// [ok] reversibility self-check passed - output can be fully reversed back to the original file
	// [dry-run] rotate would succeed - would write photo_rotated.heic (656 bytes, +138 vs input). No file written.
	// written: false
}

// Without Force, an existing output is never overwritten.
func ExampleOutputExistsError() {
	dir, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(dir)
	photo := filepath.Join(dir, "photo.heic")
	data, _ := synth.BuildSyntheticHEIC(synth.DefaultOptions())
	os.WriteFile(photo, data, 0o644)

	files.RotateFile(photo, "", 1, files.FileOptions{})
	_, err := files.RotateFile(photo, "", 1, files.FileOptions{})
	fmt.Println("exists:", errors.Is(err, fs.ErrExist))

	_, err = files.RotateFile(photo, "", 1, files.FileOptions{Force: true})
	fmt.Println("with Force:", err)
	// Output:
	// exists: true
	// with Force: <nil>
}
