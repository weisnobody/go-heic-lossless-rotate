// Package stream rotates, reverses, verifies, and inspects HEIC/HEIF images
// read through an io.ReaderAt, without holding the image data in memory
// (Go-only).
//
// Each function is its package core counterpart over an io.ReaderAt of a
// given size, with the identical result: the same output bytes, log lines,
// Info, and errors.
//
//	core.ApplyRotation     ->  ApplyRotationReaderAt
//	core.ReverseRotation   ->  ReverseRotationReaderAt
//	core.VerifyReversible  ->  VerifyReversibleReaderAt
//	core.GatherInfo        ->  GatherInfoReaderAt
//	core.VerifyStructure   ->  VerifyStructureView
//
// For files on disk, package files is usually simpler: it uses this package
// and adds the command-line tool's checks and atomic writes.
//
// # Views
//
// Rotate and reverse return a *[View]: the input's bytes with a few ranges
// replaced, read from the input on demand rather than copied. A View is an
// io.ReaderAt and an io.WriterTo, so the usual way to use one is
//
//	f, err := os.Open("photo.heic")
//	// ...
//	fi, err := f.Stat()
//	// ...
//	view, err := stream.ApplyRotationReaderAt(f, fi.Size(), 1, core.RotateOptions{})
//	// ...
//	_, err = view.WriteTo(out)
//
// To serve one over HTTP, wrap it as io.NewSectionReader(view, 0,
// view.Size()) and pass that to http.ServeContent.
//
// The input must stay open and unchanged until you're done with the View.
// In particular, don't write a View back over its own input file:
// files.RotateFile handles that case safely.
//
// # Memory
//
// For well-formed files only the 'meta' and provenance boxes (typically a
// few KB) are held in memory; rotating a 64 MiB file allocates under 1 MB.
// A file the streaming engine can't handle exactly - an unusual layout, a
// malformed structure, or an Exif item over 1 MiB - falls back to reading
// the whole input into memory and running package core, which costs about
// twice the file size but gives the same result. Errors take that path too
// (except "no provenance record"), to report core's exact error. A service
// handling untrusted uploads should therefore cap their size.
//
// # Safety checks
//
// As in package core, rotating doesn't verify its own output. To match the
// command-line tool, run [VerifyStructureView] on the View and
// [VerifyReversibleReaderAt] with the View as its input.
//
// # Concurrency
//
// Every function is safe to call from multiple goroutines at once,
// including on the same io.ReaderAt, as long as that reader's ReadAt is (as
// *os.File's and *bytes.Reader's are). A View may also be read from several
// goroutines at once.
package stream
