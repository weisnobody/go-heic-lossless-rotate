// Package difftest runs the pinned upstream Python heic-lossless-rotate
// side by side with this Go port and asserts they agree byte for byte.
//
// Everything except this file is behind the "difftest" build tag, so a plain
// `go test ./...` never needs Python. Run the suite with:
//
//	make difftest
//
// which fetches the pinned upstream checkout (see upstream.commit) into
// .upstream/ and runs `go test -tags difftest ./difftest/...`.
//
// Environment:
//
//	HEIC_ROTATE_PY_SRC             upstream checkout root (required; make sets it)
//	HEIC_ROTATE_PYTHON             Python interpreter (default "python3"; >= 3.11)
//	HEIC_ROTATE_PY_ALLOW_UNPINNED  "1" to allow a checkout at another commit/version
//
// Once the tag is set, a missing interpreter or checkout is a test failure,
// not a skip: asking for differential tests and silently getting none would
// hide exactly the problem they exist to catch.
package difftest
