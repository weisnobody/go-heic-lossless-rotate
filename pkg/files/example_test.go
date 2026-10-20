package files_test

import (
	"fmt"
	"os"
	"path/filepath"

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
