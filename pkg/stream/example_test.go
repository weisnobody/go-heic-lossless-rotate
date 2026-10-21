package stream_test

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/weisnobody/go-heic-lossless-rotate/internal/synth"
	"github.com/weisnobody/go-heic-lossless-rotate/pkg/core"
	"github.com/weisnobody/go-heic-lossless-rotate/pkg/stream"
)

// Rotate a file to a new file, holding only its metadata in memory, with
// the same checks the command-line tool runs before writing.
func ExampleApplyRotationReaderAt() {
	dir, _ := os.MkdirTemp("", "example")
	defer os.RemoveAll(dir)
	data, _ := synth.BuildSyntheticHEIC(synth.DefaultOptions()) // stand-in for a real photo
	os.WriteFile(filepath.Join(dir, "photo.heic"), data, 0o644)

	in, err := os.Open(filepath.Join(dir, "photo.heic"))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer in.Close() // only after the View has been written
	fi, _ := in.Stat()

	view, err := stream.ApplyRotationReaderAt(in, fi.Size(), 1, core.RotateOptions{})
	if err == nil {
		err = stream.VerifyStructureView(view)
	}
	if err == nil {
		err = stream.VerifyReversibleReaderAt(view, view.Size(), core.ReverseOptions{})
	}
	if err != nil {
		fmt.Println(err)
		return
	}

	out, _ := os.Create(filepath.Join(dir, "photo_rotated.heic"))
	n, err := view.WriteTo(out)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	fmt.Println(n, err)

	written, _ := os.ReadFile(filepath.Join(dir, "photo_rotated.heic"))
	inMemory, _ := core.ApplyRotation(data, 1, core.RotateOptions{})
	fmt.Println("same as core.ApplyRotation:", bytes.Equal(written, inMemory))
	// Output:
	// 656 <nil>
	// same as core.ApplyRotation: true
}

// A View is an io.ReaderAt; io.NewSectionReader makes it an io.ReadSeeker,
// which is what http.ServeContent takes.
func ExampleView() {
	data, _ := synth.BuildSyntheticHEIC(synth.DefaultOptions())
	view, err := stream.ApplyRotationReaderAt(bytes.NewReader(data), int64(len(data)), 1, core.RotateOptions{})
	if err != nil {
		fmt.Println(err)
		return
	}
	rs := io.NewSectionReader(view, 0, view.Size())
	header := make([]byte, 8)
	io.ReadFull(rs, header)
	fmt.Printf("%d bytes, first box %q\n", view.Size(), header[4:8])
	// Output:
	// 656 bytes, first box "ftyp"
}
