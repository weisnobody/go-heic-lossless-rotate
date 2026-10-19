// Package heicrotate losslessly and reversibly rotates HEIC/HEIF images by
// editing container metadata only - the 'irot' item property plus a synced
// legacy Exif Orientation tag - never touching the encoded image data.
//
// It is a Go port of heic-lossless-rotate by Oleg Ostrozhansky
// (https://github.com/olegoz/heic-lossless-rotate), usable both as a
// library and, via cmd/heic-rotate, as a drop-in replacement for the Python
// command-line tool. Functions mirror the Python heic_rotate.core module
// one-to-one, and output files are byte-identical to the Python
// implementation's, so files rotated by either tool can be reversed by the
// other.
//
// See docs/SPEC.md for the compatibility contract, docs/DESIGN.md for the
// architecture, and docs/UPSTREAM.md for the pinned upstream version and
// the Python-to-Go function map.
package heicrotate
