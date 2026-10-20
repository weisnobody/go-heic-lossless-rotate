# go-heic-lossless-rotate Specification

## Problem Statement

[heic-lossless-rotate](https://github.com/olegoz/heic-lossless-rotate)
(Python, MIT) rotates HEIC/HEIF images losslessly. It edits the `irot`
item property, and inserts one when it's missing, which ExifTool can't do. It keeps the legacy Exif
`Orientation` tag in sync and records a provenance box that makes every
edit byte-for-byte reversible.

The tool is Python-only, so:

- **Go programs** that process photos (upload pipelines, photo managers,
  DAM tools) have to shell out to a Python interpreter to use it.
- **CLI users** need a Python 3.10+ install (pip/pipx/venv friction, or
  the `.pyz` plus an interpreter) instead of a single static binary.

This project is a Go port that is both:

1. a **drop-in replacement** for the Python CLI, and
2. an **importable Go library**,

with functions and code structure that mirror the Python source as
closely as Go allows. A reader should be able to put `core.py` and
`core.go` side by side and follow them function by function.

**Upstream baseline:** v1.7.1, commit `70d6c73b5827aabf958c0649b3ac1c9e0dd33d05`
(2026-10-08).

## Target Users

Both get first-class treatment. Neither is secondary.

- **CLI user.** Someone who already runs (or would run) `heic-rotate`
  from a shell or script. They want a single binary with the same commands,
  flags, output lines, and exit codes, so existing scripts keep working
  unchanged. *Trigger:* a sideways phone photo, or a batch script over a
  photo library.
- **Go developer.** Someone building a Go application that handles HEIC
  files and wants `heicrotate.ApplyRotation(data, 1, opts)` in-process: no
  subprocess, no Python, no cgo, and no third-party dependencies pulled into their
  module graph. *Trigger:* user-facing "rotate" button, ingest-time
  orientation fix-up.

## Compatibility Contract

These are the meaning of "drop-in". Each one is a hard requirement.

| # | Guarantee | Verified by |
|---|---|---|
| C1 | **Byte-identical output.** For any input Python 1.7.1 accepts, Go `rotate`/`reverse` produce exactly the same bytes. | Differential tests |
| C2 | **Cross-reversibility.** Python can `reverse` any Go-rotated file and Go can `reverse` any Python-rotated file, including v1-format records. | Follows from C1, plus explicit tests |
| C3 | **Mirrored version.** The Go port reports and writes the upstream version it implements (`1.7.1`): in `-V` output, in the `TVER` provenance entry, and in `info` output. | Unit + differential tests |
| C4 | **Same CLI surface.** Same subcommands, positional args, flags (including placement before or after the subcommand and the implicit-`rotate` shorthand), defaults, and exit codes (`0`–`4` for `info`, `10` errors, `2` usage errors). | Ported CLI tests |
| C5 | **Same operational text.** Every stdout line (`[info]`, `[fast path]`, `[slow path]`, `[angle 0]`, `[exif sync]`, `[provenance]`, `[reverse]`, `[ok]`, `[dry-run]`, `[warning]`) and every `error: ...` message is identical, including Python-style formatting such as `b'irot'` reprs and `0x%08x` CRCs. | Differential tests |
| C6 | **Help text may differ.** `-h` output and the wording of usage errors can differ from argparse's. The exit codes for them must still match. | — |

### Go-only extensions

| # | Extension | Notes |
|---|---|---|
| E1 | **`-G` / `--goversion`**: prints the Go port's own version (`1.7.1+go.N`), the upstream version it implements, and the Go toolchain it was built with, then exits `0`. It's a top-level flag only, like `-V`. | Python rejects it (usage error, exit 2). Listed in COMPAT.md and excluded from the differential argv table. `-V`, `TVER`, and `info` are unaffected and still report only `1.7.1` (C3). |

**Port versioning.** The Go port's version is the mirrored upstream version
plus semver build metadata: `<upstream>+go.<N>`, where `N` counts Go releases for
that upstream version and starts at 1. Example: `1.7.1+go.1`, then
`1.7.1+go.2` for a Go-only fix. The `+go.N` suffix never reaches a file and
appears only in `-G` output and release names.

### Deliberate divergences ("fix quirks")

The Go port fixes these upstream quirks. Each one is listed in
`docs/COMPAT.md` with the exact Python and Go behavior, and is excluded
from the differential corpus.

| # | Python 1.7.1 behavior | Go behavior |
|---|---|---|
| D1 | The default output path uses `input.rsplit('.', 1)[0]` on the **whole path**, so `dir.v2/photo` becomes `dir_rotated.heic` and `.hidden` becomes `_rotated.heic` | The extension is stripped from the **base name only**. A leading dot doesn't count as an extension. For every ordinary `name.ext` input the result is identical to Python's. |
| D2 | Malformed boxes or provenance entries crash with a Python traceback (`struct.error`, `IndexError`, `KeyError`) and exit `1` | Go returns `BoxParseError` or `ProvenanceError`, prints `error: ...`, and exits `10`. **The library never panics on malformed input.** |
| D3 | The output file is opened with `'wb'` and written in place, so a crash mid-write leaves a truncated file | The CLI writes to a temp file in the same directory, then renames it over the target, keeping the existing target's file mode when `-f` overwrites. |
| D4 | A shrinking `iloc` patch that would go negative raises an uncaught `OverflowError` | `BoxParseError` |
| D5 | `iloc` values are arbitrary-precision ints | Any field width 0–15 is accepted; only a value over 64 bits is refused with `BoxParseError` |
| D6 | A box with a 64-bit size of 0 makes `iter_boxes`/`verify_structure` loop forever | `BoxParseError` |
| D7 | Unlimited zero-width `iloc` extents are materialized (gigabytes for a crafted file) | Up to 2^20 are held, identically to Python; beyond that, `BoxParseError` |

## MVP Scope

### Must Have

1. **Core port** (`heic_rotate/core.py` becomes package `heicrotate`). Every
   function, constant, and error type is ported 1:1 and works on `[]byte`.
   Public Python names are exported, and `_private` names are unexported.
2. **CLI port** (`heic_rotate/cli.py` becomes package `cli`): `rotate`,
   `reverse`, `info`, `-V`, implicit `rotate`, all flags, the exact
   messages, and the exit codes. Parsed with the stdlib only. Plus the Go-only
   `-G`/`--goversion` (E1).
3. **Two binaries:** `heic-rotate` and `heic-lossless-rotate`, which behave
   identically and both identify as `heic-lossless-rotate`.
4. **Convenience file API:** `RotateFile`, `ReverseFile`, `InfoFile`, with the
   default output naming, overwrite refusal, dry-run, and atomic write. The CLI
   is a thin layer over these.
5. **Streaming / low-memory engine** (done: O(meta) memory for well-formed files, with an exact
   fallback to the `[]byte` engine for anything else; see DESIGN.md): an `io.ReaderAt` → `io.Writer`
   variant that never holds `mdat` in memory. It must produce output
   byte-identical to the `[]byte` engine. Once it passes that gate, the
   CLI and file API switch to it.
6. **Test parity:** the upstream suite (`test_heic_rotate.py`,
   `synth_heic.py`) ported to Go, plus a differential suite that runs the
   real Python tool against the Go one.
7. **Release binaries** via goreleaser: macOS, Linux, and Windows on
   amd64 and arm64, both binary names, plus `go install` support.

### Explicitly Excluded

- **New features beyond upstream** (batch/recursive mode, mirroring/`imir`,
  absolute-angle mode). Behavior stays pinned to upstream until upstream
  adds it.
- **Byte-identical help text.** Argparse's help formatter isn't worth
  reproducing (C6).
- **Multiple `ipma`/`ipco` boxes, 64-bit `meta` headers, Exif via
  `idat`.** These are upstream's known limitations, kept as refusals.
- **cgo / libheif.** The port is pure Go only.

## Success Criteria

- [ ] All ported upstream tests pass (`go test ./...`), except the
      Python-implementation-specific ones listed in PLAN.md, which get Go
      equivalents.
- [ ] The differential suite reports zero byte, stdout, or exit-code
      differences against Python 1.7.1 across every synthetic fixture
      variant and every file in `testdata/`.
- [ ] Python `reverse` restores Go-rotated files to the original bytes, and
      Go `reverse` restores Python-rotated files (both directions).
- [x] The streaming engine's output equals the `[]byte` engine's for the
      whole corpus, and its peak heap stays flat as `mdat` grows (the
      multi-MiB fixture uses less than 2 MiB of heap).
- [ ] The library has zero non-stdlib dependencies (`go.mod` has no
      `require`).
- [ ] No panics on malformed input: every Python-traceback case in D2
      yields a typed error.
- [ ] `go install github.com/pjg/go-heic-lossless-rotate/cmd/heic-rotate@latest`
      works, and goreleaser publishes both binaries for all targets.
- [ ] `-G` reports `1.7.1+go.N` matching the release tag, while `-V`, `TVER`,
      and `info` report only `1.7.1`.

## Scenarios

### S1: Drop-in CLI swap
- **Given:** a shell script calling `heic-rotate 270 "$f" -q` and branching
  on `heic-rotate info -q "$f"; echo $?`
- **When:** the Python install is replaced with the Go binary
- **Then:** the script behaves identically: same output files (byte-equal),
  same exit codes, same stdout.

### S2: Mixed-tool history
- **Given:** a file rotated once by Python 1.6.0 (v2 record, no TVER), and
  another rotated by Python 1.5.0 (v1 record with OPRP)
- **When:** Go runs `rotate 90` on each, then Python runs `reverse`
- **Then:** Go migrates the records exactly as Python would, and Python's
  reverse restores the true originals.

### S3: Library use in an upload pipeline
- **Given:** a Go service with an uploaded HEIC in `[]byte`
- **When:** it calls `heicrotate.ApplyRotation(data, 3, heicrotate.Options{})`
- **Then:** it gets the rotated bytes or a typed error (`*BoxParseError` /
  `*ProvenanceError`), with nothing written to stdout and no panic.

### S4: Large file, low memory
- **Given:** a 200 MB HEIF on disk
- **When:** the CLI rotates it
- **Then:** the engine holds only the `meta` and provenance span plus I/O buffers in memory,
  and the output matches what the `[]byte` engine would produce.

### S5: Malformed input
- **Given:** a file whose `iloc` is truncated mid-entry
- **When:** `heic-rotate info bad.heic`
- **Then:** `error: ...` goes to stderr and the exit code is `10` (Python
  would print a traceback and exit `1`; see D2).

## Full Vision (post-MVP)

- Fuzzing of all parsers and the rotate→reverse round trip (`go test -fuzz`)
- Track upstream releases (each one ported deliberately: bump the mirrored version,
  re-run differential tests)
- Batch/recursive CLI mode, in-place editing flag, JSON `info` output,
  if upstream adds them or the Go port adds them as clearly marked extensions
- Homebrew tap / Scoop manifest
- WASM build for in-browser use
