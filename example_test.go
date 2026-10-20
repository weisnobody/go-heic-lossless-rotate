package heicrotate_test

import (
	"fmt"
	"os"
	"path/filepath"

	heicrotate "github.com/pjg/go-heic-lossless-rotate"
	"github.com/pjg/go-heic-lossless-rotate/internal/synth"
	"github.com/pjg/go-heic-lossless-rotate/pkg/core"
)

// Rotate a file 90 degrees counter-clockwise, inspect it, and restore the
// byte-identical original.
func ExampleRotateFile() {
	dir, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(dir)
	photo := filepath.Join(dir, "photo.heic")
	data, _ := synth.BuildSyntheticHEIC(synth.DefaultOptions()) // stand-in for a real photo
	os.WriteFile(photo, data, 0o644)

	res, err := heicrotate.RotateFile(photo, "", 1, heicrotate.FileOptions{})
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(filepath.Base(res.Output))

	info, _ := heicrotate.InfoFile(res.Output)
	fmt.Println("added:", *info.DeltaQuarterTurns*90, "deg")

	restored, _ := heicrotate.ReverseFile(res.Output, "", heicrotate.FileOptions{})
	original, _ := os.ReadFile(photo)
	again, _ := os.ReadFile(restored.Output)
	fmt.Println("identical:", string(original) == string(again))
	// Output:
	// photo_rotated.heic
	// added: 90 deg
	// identical: true
}

// Work on bytes in memory instead of files.
func ExampleApplyRotation() {
	data, _ := synth.BuildSyntheticHEIC(synth.DefaultOptions())
	rotated, err := core.ApplyRotation(data, 3, core.RotateOptions{}) // 270 deg CCW = 90 deg clockwise
	if err != nil {
		fmt.Println(err)
		return
	}
	turns, _ := core.GetCurrentRotation(rotated)
	restored, _ := core.ReverseRotation(rotated, core.ReverseOptions{})
	fmt.Println(turns*90, string(restored) == string(data))
	// Output: 270 true
}
