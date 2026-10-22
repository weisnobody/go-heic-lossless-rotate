// Package core losslessly and reversibly rotates HEIC/HEIF images by
// editing container metadata only - the 'irot' item property plus a synced
// legacy Exif Orientation tag - never touching the encoded image data.
//
// It is a Go port of heic-lossless-rotate by Oleg Ostrozhansky
// (https://github.com/olegoz/heic-lossless-rotate). Functions mirror the
// Python heic_rotate.core module one-to-one, and output files are
// byte-identical to the Python implementation's, so files rotated by either
// tool can be reversed by the other.
//
// # Choosing a package
//
// This package works on a whole file held in memory: []byte in, []byte
// out. Two Go-only packages build on it:
//
//   - [github.com/weisnobody/go-heic-lossless-rotate/pkg/files] rotates, reverses,
//     and inspects files on disk exactly as the command-line tool does,
//     including its safety checks and atomic writes. Start here for files.
//   - [github.com/weisnobody/go-heic-lossless-rotate/pkg/stream] works over any
//     io.ReaderAt and holds only the file's metadata in memory. Use it for
//     large inputs or data that isn't a local file.
//
// All three give identical results.
//
// # Rotation
//
// [ApplyRotation] turns the image by deltaTurns quarter turns
// COUNTER-CLOCKWISE, added to whatever rotation the file already has: it
// never sets an absolute angle. Rotating 1 twice lands at 180 degrees.
// deltaTurns is taken modulo 4, so 3 or -1 is 90 degrees clockwise, and 0
// changes nothing visible but adds or updates the tracking record.
//
// [ApplyMirror] (Go-only) flips the image left-right or upside down, as it
// currently displays, through the 'imir' property, with the same tracking
// and reversibility; see docs/COMPAT.md E2.
//
// The first edit stores a provenance record (a private 'uuid' box) holding
// the file's pristine state and CRC32s. [ReverseRotation] uses it to undo
// every edit at once, returning the byte-identical original, and refuses if
// something else has modified the file since (unless
// [ReverseOptions].IgnoreTamper is set). [GatherInfo] reads the record
// without changing anything; [FormatInfo] turns that into the CLI's info
// text and exit code.
//
// # Safety checks
//
// The command-line tool checks every output before writing it. This
// package leaves those checks to the caller, as upstream's library does. To
// match the tool after [ApplyRotation], run [VerifyStructure] and
// [VerifyReversible] on the output; package files does both for you.
//
// # Logging
//
// Upstream's verbose output becomes a Log io.Writer in [RotateOptions] and
// [ReverseOptions]. A nil Log (the zero value) is silent; pass os.Stdout to
// get the lines the Python library prints.
//
// # Errors
//
// Errors are a *[BoxParseError] for malformed or unsupported structure, or a
// *[ProvenanceError] for a missing, inconsistent, or tampered provenance
// record. errors.Is(err, [ErrNoProvenance]) reports a file this tool has
// never touched. Nothing panics on malformed input: where the Python code
// would crash with a traceback, this package returns a *BoxParseError.
//
// # Concurrency
//
// Every function is safe to call from multiple goroutines at once, on the
// same input or different ones. Inputs are never modified. The exported
// lookup tables ([ProvenanceUUID], [SupportedReverseFormatVersions],
// [IrotToExifOrientation], [ExifOrientationToIrot]) are read-only; don't
// change them.
//
// # Go-only hooks
//
// [FileEnv], [ApplyRotationEnv], [PrepareReversalEnv], [Reversal], [Chunk],
// [ReconstructedPlan], [CheckReconstructedCrc], [ExifFieldInBlob], and
// [ListBoxes] exist so package stream can run this package's orchestration. They aren't part of
// upstream's API, and ordinary callers don't need them.
//
// See docs/SPEC.md for the compatibility contract, docs/COMPAT.md for every
// difference from the Python tool, and docs/UPSTREAM.md for the pinned
// upstream version and the Python-to-Go function map.
package core
