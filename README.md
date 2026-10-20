# go-heic-lossless-rotate

Losslessly rotate HEIC images by modifying metadata.

A Go port of [heic-lossless-rotate](https://github.com/olegoz/heic-lossless-rotate)
by Oleg Ostrozhansky. It's meant both as a **drop-in replacement** for the Python
CLI (same commands, flags, messages, and exit codes, and byte-identical output
files) and as an **importable Go library** whose functions mirror the
Python source one to one. It has no dependencies outside the Go standard library.

> **Status: pre-release.** The library core, the file API, the streaming engine,
> and the CLI are complete and verified against the Python tool (see
> [docs/PLAN.md](docs/PLAN.md)). Still to come: release binaries.

Memory: files are streamed. Only the `meta` and provenance boxes (typically a
few KB) are held in memory, so rotating a 64 MiB file allocates under 1 MB.
Malformed or unusual files fall back to an in-memory path that gives the
identical result.

## Usage

```bash
make build                                   # ./bin/heic-rotate and ./bin/heic-lossless-rotate
./bin/heic-rotate rotate 90 photo.heic       # -> photo_rotated.heic
./bin/heic-rotate info photo_rotated.heic    # exit code 1 = this tool added 90 deg
./bin/heic-rotate reverse photo_rotated.heic # -> photo_rotated_restored.heic, byte-identical to photo.heic
./bin/heic-rotate -h
```

Commands, flags, messages, and exit codes are the Python tool's (see its
[README](https://github.com/olegoz/heic-lossless-rotate#usage)). The only
addition is `-G`/`--goversion`, which shows the Go port's own version.

Implements upstream **v1.7.1** (see [docs/UPSTREAM.md](docs/UPSTREAM.md)).

## Library

```go
import heicrotate "github.com/pjg/go-heic-lossless-rotate"

// Files: everything the CLI does (self-check, default naming, overwrite
// refusal, atomic write), with the CLI's messages if you pass a Log writer.
res, err := heicrotate.RotateFile("photo.heic", "", 1, heicrotate.FileOptions{}) // 1 quarter turn CCW
info, err := heicrotate.InfoFile(res.Output)       // *info.DeltaQuarterTurns == 1
_, err = heicrotate.ReverseFile(res.Output, "", heicrotate.FileOptions{})

// Streams: any io.ReaderAt, result as a View (io.ReaderAt + io.WriterTo).
view, err := heicrotate.ApplyRotationReaderAt(f, size, 1, heicrotate.RotateOptions{})
_, err = view.WriteTo(w)

// Bytes: the 1:1 port of upstream's core.py.
rotated, err := heicrotate.ApplyRotation(data, 3, heicrotate.RotateOptions{}) // 270 CCW = 90 CW
original, err := heicrotate.ReverseRotation(rotated, heicrotate.ReverseOptions{})
```

Errors are `*heicrotate.BoxParseError` (malformed or unsupported structure),
`*heicrotate.ProvenanceError` (missing or inconsistent provenance record, or a
failed self-check), `*heicrotate.OutputExistsError`, or file-system errors.
The library never panics on malformed input. See `example_test.go` for runnable
examples.

## Development

Requires Go 1.23+. The differential suite also needs Python 3.14 and git (the
core tests run on 3.11+, but the CLI tests follow 3.14's argparse).

```bash
make test       # unit tests (no Python needed)
make lint       # go vet (+ staticcheck if installed)
make build      # compile; binaries land in ./bin/
make run ARGS="info photo.heic"
make difftest   # fetch the pinned upstream into .upstream/ and compare Go vs Python
```

`make difftest` compares this port against the real Python
implementation at the exact upstream commit in
[`difftest/upstream.commit`](difftest/upstream.commit), and requires their output to be
byte-identical. See [`difftest/doc.go`](difftest/doc.go) for its environment
variables.

Drop real `.heic` files into `testdata/` to include them in round-trip
tests. The directory's contents are gitignored.

## Documentation

- [docs/SPEC.md](docs/SPEC.md): what "drop-in" guarantees, and the scope
- [docs/DESIGN.md](docs/DESIGN.md): architecture and key decisions
- [docs/PLAN.md](docs/PLAN.md): implementation roadmap
- [docs/UPSTREAM.md](docs/UPSTREAM.md): pinned upstream version and the Python → Go function map
- [docs/COMPAT.md](docs/COMPAT.md): every deliberate difference from the Python tool

## License

MIT. See [LICENSE](LICENSE). Portions are derived from heic-lossless-rotate,
Copyright (c) 2026 Oleg Ostrozhansky.
