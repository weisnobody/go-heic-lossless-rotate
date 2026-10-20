package core_test

import (
	"errors"
	"fmt"
	"os"

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

// ApplyRotation doesn't check its own output; this is how to run the same
// checks the command-line tool runs before it writes anything.
func ExampleVerifyReversible() {
	data, _ := synth.BuildSyntheticHEIC(synth.DefaultOptions())
	rotated, err := core.ApplyRotation(data, 1, core.RotateOptions{})
	if err == nil {
		err = core.VerifyStructure(rotated)
	}
	if err == nil {
		err = core.VerifyReversible(rotated, core.ReverseOptions{})
	}
	fmt.Println("safe to write:", err == nil)
	// Output: safe to write: true
}

// Rotation is relative and counts quarter turns counter-clockwise, modulo 4.
func ExampleApplyRotation_relative() {
	data, _ := synth.BuildSyntheticHEIC(synth.DefaultOptions())
	once, _ := core.ApplyRotation(data, 1, core.RotateOptions{})   // 90 deg CCW
	twice, _ := core.ApplyRotation(once, 1, core.RotateOptions{})  // another 90: now 180
	back, _ := core.ApplyRotation(twice, -2, core.RotateOptions{}) // -2 == 2 (mod 4): 360, back to 0
	for _, b := range [][]byte{once, twice, back} {
		turns, _ := core.GetCurrentRotation(b)
		fmt.Print(turns*90, " ")
	}
	fmt.Println()
	// Output:
	// 90 180 0
}

// The Log option receives the lines upstream's verbose mode prints.
func ExampleRotateOptions() {
	data, _ := synth.BuildSyntheticHEIC(synth.DefaultOptions())
	_, _ = core.ApplyRotation(data, 1, core.RotateOptions{Log: os.Stdout})
	// Output:
	// [info] primary item id = 1
	// [info] ipco properties (1-based) = [(1, 'ispe')]
	// [info] primary item associations = [(1, True)]
	// [slow path] no 'irot' property associated with the primary item (implied 0 deg) - inserting one with 90 deg after applying the 90 deg delta
	// [slow path] inserted new 'irot' property (index 2), grew container by 10 bytes
	// [exif sync] no embedded legacy Exif Orientation tag found - nothing to sync
	// [provenance] created provenance record (format v2, 128 bytes)
}

// GatherInfo reads the provenance record; FormatInfo gives the info
// command's text and exit code.
func ExampleGatherInfo() {
	data, _ := synth.BuildSyntheticHEIC(synth.DefaultOptions())
	rotated, _ := core.ApplyRotation(data, 3, core.RotateOptions{})
	info, err := core.GatherInfo(rotated)
	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println("added:", *info.DeltaQuarterTurns*90, "deg; now:", info.CurrentQuarterTurns*90, "deg")
	text, exitCode := core.FormatInfo(info, false)
	fmt.Println(text)
	fmt.Println("exit code:", exitCode)
	// Output:
	// added: 270 deg; now: 270 deg
	// Rotated with heic-lossless-rotate v1.7.1, provenance format v2.
	//   heic-lossless-rotate has added: 270 deg
	//   file's rotation: 0 deg originally -> 270 deg now
	//   original file CRC32:      0x45c9ba30
	//   also recorded: iloc snapshot
	// exit code: 3
}

// Telling the error kinds apart.
func Example_errors() {
	untouched, _ := synth.BuildSyntheticHEIC(synth.DefaultOptions())
	_, err := core.ReverseRotation(untouched, core.ReverseOptions{})
	fmt.Println("never rotated:", errors.Is(err, core.ErrNoProvenance))

	ftypOnly := []byte("\x00\x00\x00\x10ftypheic\x00\x00\x00\x00") // a valid box, but no 'meta'
	_, err = core.ApplyRotation(ftypOnly, 1, core.RotateOptions{})
	var be *core.BoxParseError
	fmt.Println("malformed:", errors.As(err, &be))
	fmt.Println(be.Msg)
	// Output:
	// never rotated: true
	// malformed: true
	// no top-level 'meta' box found - not a valid HEIF file?
}
