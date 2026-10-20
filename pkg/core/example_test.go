package core_test

import (
	"fmt"

	"github.com/pjg/go-heic-lossless-rotate/internal/synth"
	"github.com/pjg/go-heic-lossless-rotate/pkg/core"
)

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
