# go-heic-lossless-rotate

Rotate HEIC/HEIF photos **losslessly** and **reversibly**, by changing the
small "which way is up" tag in the file's metadata instead of decoding and
re-encoding the image.

This is a Go port of
[heic-lossless-rotate](https://github.com/olegoz/heic-lossless-rotate) by Oleg
Ostrozhansky. Claude did the coding. It is both:

- a **drop-in replacement** for the Python command-line tool: the same
  commands, flags, messages, and exit codes, and byte-identical output files,
  so a file rotated by either tool can be reversed by the other; and
- a **Go library**, whose functions mirror the Python source one to one,
  plus a file API and a streaming API that holds only the file's metadata in
  memory.

It has no dependencies outside the Go standard library.

> **Why "lossless" matters:** HEIC, like JPEG, is a lossy format. Every
> decode and re-encode discards some image quality, and the loss compounds
> with each re-save. This tool only ever edits the rotation tag, so no
> re-encoding, and no quality loss, happens no matter how many times you
> rotate.

Why in Go?  Was building a tool (in Go) to be able to fix rotation on images and was trying to make the tool easy to install / use.  The Python requirement was not something I felt the non-technical people that might use the tool would have on hand.


## Contents

- [What it does](#what-it-does)
- [Install](#install)
- [Usage](#usage)
  - [Examples](#examples)
- [Before you start](#before-you-start)
- [Options](#options)
- [Exit codes](#exit-codes)
- [How it works](#how-it-works)
- [Known limitations](#known-limitations)
- [Differences from the Python tool](#differences-from-the-python-tool)
- [Using it as a Go library](#using-it-as-a-go-library)
- [Versions](#versions)
- [Development](#development)
- [Documentation](#documentation)
- [License](#license)

## What it does

- **Rotates HEIC/HEIF photos losslessly.** The image data is never touched or
  recompressed; only the rotation tag inside the file changes.
- **Is fully reversible.** `reverse` restores a file to be byte-for-byte
  identical to the original, even after several rotations.
- **Checks itself before writing.** `rotate` reverses its own output in
  memory and refuses to write anything unless that reconstructs the original
  exactly.
- **Writes atomically.** Output goes to a temporary file that is renamed into
  place, so a crash or full disk can never leave a half-written photo.
- **Uses little memory.** Files are streamed: only the metadata (typically a
  few KB) is held in memory, so a 64 MiB photo is rotated with under 1 MB.

## Install

Both ways install two commands, `heic-rotate` and `heic-lossless-rotate`;
see [Usage](#usage).

**Download a release.** Each
[release](https://github.com/weisnobody/go-heic-lossless-rotate/releases) has
an archive for macOS, Linux, and Windows (Intel/AMD and ARM), plus
`checksums.txt`. Unpack it and put both commands somewhere on your `PATH`.
They're single files with nothing else to install.

On macOS, a downloaded binary is quarantined until you allow it (System
Settings → Privacy & Security), or clear the flag yourself:
`xattr -d com.apple.quarantine heic-rotate heic-lossless-rotate`.

**With Go 1.23 or later:**

```bash
go install github.com/weisnobody/go-heic-lossless-rotate/cmd/heic-rotate@latest
go install github.com/weisnobody/go-heic-lossless-rotate/cmd/heic-lossless-rotate@latest
```

`@latest` installs the newest upstream version's first Go release; a later
Go-only fix needs its tag spelled out (see [Versions](#versions)).

**As a library:**

```bash
go get github.com/weisnobody/go-heic-lossless-rotate@latest
```

See [Using it as a Go library](#using-it-as-a-go-library).

**From source:**

```bash
git clone https://github.com/weisnobody/go-heic-lossless-rotate.git
cd go-heic-lossless-rotate
make build      # ./bin/heic-rotate and ./bin/heic-lossless-rotate
```

## Usage

```
heic-rotate rotate <0|90|180|270> <input.heic> [output.heic] [-f]
heic-rotate reverse <input.heic> [output.heic] [-f] [--ignore-tamper-check]
heic-rotate info <input.heic>
heic-rotate -V | --version
heic-rotate -G | --goversion
```

The tool is built under two names, `heic-rotate` and `heic-lossless-rotate`,
which are identical, as with the Python tool's `pip` install. Whichever you
run, it identifies itself as **heic-lossless-rotate** in its output.

The `rotate` subcommand name may be omitted: if the first non-option argument
is exactly `0`, `90`, `180`, or `270`, `rotate` is assumed.

```bash
heic-rotate 90 photo.heic photo_rotated.heic
# equivalent to:
heic-rotate rotate 90 photo.heic photo_rotated.heic
```

**Default output names.** Without an output path, `rotate` writes to
`<input>_rotated.heic`, unless the input name already ends in `_restored`
(it looks like `reverse`'s output), in which case that suffix is stripped
instead (`photo_restored.heic` → `photo.heic`). `reverse` writes to
`<input>_restored.heic`. Both refuse to overwrite an existing file unless you
pass `-f`/`--force`.

**Rotation direction.** Positive angles rotate **counter-clockwise**:
`rotate 90` turns the image 90° counter-clockwise, and `rotate 270` turns it
90° clockwise. Rotation is **added** to whatever the file already has, rather
than setting an absolute angle: rotating 90° twice lands at 180°, the way
you'd turn a printed photo in your hands.

Run with no arguments, or with `-h`, for full help; each subcommand also takes
`-h` (`heic-rotate rotate -h`).

### Examples

```bash
# Rotate 90° counter-clockwise, writing to a new file
heic-rotate rotate 90 IMG_0001.heic IMG_0001_rotated.heic

# Same thing, using the implicit-rotate shorthand
heic-rotate 90 IMG_0001.heic IMG_0001_rotated.heic

# Rotate 90° clockwise instead (270° counter-clockwise == 90° clockwise)
heic-rotate 270 IMG_0001.heic IMG_0001_rotated.heic

# Preview what would happen without writing anything
heic-rotate --dry-run 180 IMG_0001.heic

# Check whether and how this tool has previously touched a file
heic-rotate info IMG_0001_rotated.heic

# Bring an old file's metadata up to date (for example, add version
# tracking to a file rotated by an older release) without changing how it
# displays
heic-rotate 0 IMG_0001_rotated.heic IMG_0001_updated.heic

# Undo every edit this tool has made, restoring the exact original bytes
heic-rotate reverse IMG_0001_rotated.heic IMG_0001_original.heic

# Overwrite an existing output file
heic-rotate 90 IMG_0001.heic IMG_0001_rotated.heic --force

# Show which Go build you're running
heic-rotate -G
```

## Before you start

This tool edits container metadata directly rather than going through a
HEIC/HEIF library. It verifies every output before writing it and writes
atomically, but keep a backup of anything irreplaceable, and check that the
result displays correctly in your own image viewers before rotating files in
place.

## Options

| Flag | Applies to | Meaning |
|---|---|---|
| `-h`, `--help` | any | Show help and exit |
| `-V`, `--version` | top level | Show the upstream version implemented, the current metadata format version, and the format versions `reverse` supports, then exit |
| `-G`, `--goversion` | top level | Show the Go port's own version, the upstream version it implements, and the Go toolchain it was built with, then exit (Go only) |
| `-q`, `--quiet` | all | Suppress informational messages on stdout (errors still go to stderr) |
| `--dry-run` | `rotate`, `reverse` | Do everything except write the output file |
| `-f`, `--force` | `rotate`, `reverse` | Overwrite the output file if it already exists |
| `--no-exif-sync` | `rotate` | Don't sync the legacy embedded Exif `Orientation` tag |
| `--ignore-tamper-check` | `reverse` | Reverse even if the file seems to have been modified by something else since this tool last edited it |

`-q`, `--dry-run`, and `-f` may be given either before or after the
subcommand name.

`-f` and `--ignore-tamper-check` control different things. `-f` is only
about not overwriting an existing *output* file. `--ignore-tamper-check`
overrides a checksum mismatch showing that the *input* was changed by
something else since this tool's last edit; reversing then could silently
discard those other changes, so it's refused unless you ask for it. Even
then, `reverse` still checks the reconstructed file against the original's
checksum and refuses to write a wrong result.

## Exit codes

`rotate` and `reverse` exit `0` on success. `info` reports its result in the
exit code, so a script can branch without parsing text:

| Exit code | Meaning |
|---|---|
| `0` | No provenance record: this tool has never touched the file |
| `1` / `2` / `3` | This tool has added `90°` / `180°` / `270°` in total |
| `4` | The file has a provenance record, but this tool's net rotation is `0°` (for example after `rotate 0`, or rotations that cancel out) |
| `10` | An error: an unreadable or malformed file, a refused overwrite, a failed check |

Bad arguments are a usage error, which exits `2` for every command, as with
any `argparse` tool. For `info` that is the same code as "180°", so a script
should make sure its arguments are right before relying on it.

## How it works

Rotating a HEIC file without re-encoding means changing the 2-bit angle in
the `irot` property inside its ISOBMFF container (ISO/IEC 23008-12). Tools
like ExifTool can do that only when an `irot` box already exists, and many
phones leave it out when no rotation is needed. This tool handles both cases:

- **Fast path:** an `irot` property already belongs to the primary image, so
  its one byte is overwritten in place.
- **Slow path:** there is no `irot` yet, so one is inserted into `ipco`, an
  association is added to `ipma`, the containing boxes grow, and any `iloc`
  file offsets the size change affects are patched.

It also keeps a legacy embedded Exif `Orientation` tag, if there is one, in
sync, since some readers fall back to it.

The first edit stores a small **provenance record** in a private top-level
`uuid` box, which compliant readers skip. It holds the file's pristine state
and CRC32 checksums. Later edits update only the live rotation, so `reverse`
can always undo every edit since the first, and can tell when something else
has changed the file. `info` reads the record without changing anything.

Only container bytes are ever touched; the compressed image data (`mdat`) is
never rewritten. For the full details, see upstream's
[README](https://github.com/olegoz/heic-lossless-rotate#why-this-exists).

## Known limitations

These are deliberate. Each is detected and refused with an error rather than
risking a wrong edit:

- A `meta` box with a 64-bit size header, or a size of `0` ("extends to end
  of file"), is refused. Both are legal, but real encoders don't use them for
  a box as small as `meta`.
- More than one `ipma` or `ipco` box inside a single `iprp` is refused.
- The Exif `Orientation` sync only handles an Exif item stored at an absolute
  file offset. If it's stored any other way, the sync is skipped: safe, just
  incomplete.
- Two more Go-only refusals guard against crafted files: `iloc` values that
  don't fit in 64 bits, and more than about a million zero-width `iloc`
  extents (see [COMPAT.md](docs/COMPAT.md) D5 and D7).

## Differences from the Python tool

Output files are byte-identical. The differences are all where the Python
tool would crash or behave unsafely; [docs/COMPAT.md](docs/COMPAT.md) lists
each one with examples.

- **Malformed files give an error, not a traceback.** Where Python crashes
  with a traceback and exit code 1, this prints `error: <message>` and exits
  `10`.
- **Writes are atomic.** Python writes the output in place, so a crash
  mid-write leaves a truncated file. Here a crash leaves either the old file
  or the complete new one. Everything else about writing matches Python:
  writes go through symlinks, overwritten files keep their permissions, and
  hard-linked files stay linked.
- **Default output names** strip the extension from the file name only, so a
  dot in a directory name doesn't count as one.
- **`-G`/`--goversion`** is new.
- **Arguments** are parsed the way Python 3.14's `argparse` parses them.
  Older Pythons reject a few unusual orderings that 3.14 accepts.
- **File-error messages** match Python's for the common errors (missing file,
  permission denied, and so on) and use Go's wording for rare ones.

## Using it as a Go library

| Package | Use it for |
|---|---|
| [`pkg/files`](pkg/files) | Files on disk, exactly as the command does: checks, default names, overwrite refusal, atomic writes |
| [`pkg/stream`](pkg/stream) | Any `io.ReaderAt` (large files, remote objects), holding only the metadata in memory |
| [`pkg/core`](pkg/core) | `[]byte` in, `[]byte` out: the 1:1 port of upstream's `core.py` |
| [`pkg/version`](pkg/version) | The upstream version implemented and the Go port's own version |

All three give identical results.

```go
import (
	"github.com/weisnobody/go-heic-lossless-rotate/pkg/core"
	"github.com/weisnobody/go-heic-lossless-rotate/pkg/files"
	"github.com/weisnobody/go-heic-lossless-rotate/pkg/stream"
)

// Files: everything the command does. Log: os.Stdout prints its messages.
res, err := files.RotateFile("photo.heic", "", 1, files.FileOptions{}) // -> photo_rotated.heic
info, err := files.InfoFile(res.Output)                                // *info.DeltaQuarterTurns == 1
_, err = files.ReverseFile(res.Output, "", files.FileOptions{})

// Streams: any io.ReaderAt; the result is a View to write out.
view, err := stream.ApplyRotationReaderAt(f, size, 1, core.RotateOptions{})
_, err = view.WriteTo(w)

// Bytes in memory.
rotated, err := core.ApplyRotation(data, 3, core.RotateOptions{}) // 270° CCW == 90° CW
original, err := core.ReverseRotation(rotated, core.ReverseOptions{})
```

A few things to know:

- **Rotation counts quarter turns counter-clockwise**, added to the file's
  current rotation and taken modulo 4, so `3` or `-1` is 90° clockwise.
- **Only `pkg/files` checks its output.** `core.ApplyRotation` and
  `stream.ApplyRotationReaderAt` don't, as with upstream's library; run
  `VerifyStructure` and `VerifyReversible` on the result to get the same
  guarantee.
- **A `View` reads from its input** until you've written it out, so keep the
  input open and unchanged until then. Malformed or unusual files fall back
  to reading the whole input into memory, with the same result, so cap the
  size of untrusted uploads.
- **Errors** are `*core.BoxParseError` (malformed structure),
  `*core.ProvenanceError` (missing or inconsistent provenance record, or a
  failed check), `*files.OutputExistsError`, or file-system errors.
  `errors.Is(err, core.ErrNoProvenance)` means the file was never rotated by
  this tool. Nothing panics on malformed input.
- **Everything is safe for concurrent use**, including several goroutines on
  the same input.

Each package's documentation (`go doc ./pkg/stream`, or the package
directories above) covers these in detail, with runnable examples.

## Versions

The port implements upstream **v1.7.1** (see
[docs/UPSTREAM.md](docs/UPSTREAM.md)). Files record that version, and `-V`
shows it, exactly as the Python tool does. The Go port's own releases add
build metadata, as in `1.7.1+go.1`; `-G` shows that version and the Go
toolchain it was built with.

Release tags follow the same scheme:

| Release | Tag | `-G` shows |
|---|---|---|
| First Go release of upstream 1.7.1 | `v1.7.1` | `1.7.1+go.1` |
| A later Go-only fix | `v1.7.1+go.2`, `v1.7.1+go.3`, ... | `1.7.1+go.2`, ... |
| First Go release of upstream 1.8.0 | `v1.8.0` | `1.8.0+go.1` |

Release downloads exist for every tag. Go's module tools, though, ignore
tags with build metadata, so `go install …@latest` and `go get …@latest`
stop at the plain `vX.Y.Z` tag. To get a Go-only fix, name its tag:

```bash
go install github.com/weisnobody/go-heic-lossless-rotate/cmd/heic-rotate@v1.7.1+go.2
```

Go records it as a pseudo-version (such as `v1.7.2-0.20261020…-abcdef123456`)
rather than as `v1.7.1+go.2`; `heic-rotate -G` confirms what you have.

## Development

Requires Go 1.23+. The differential suite also needs Python 3.14 and git (the
core tests run on 3.11+, but the CLI tests follow 3.14's `argparse`).

```bash
make test       # unit tests (no Python needed)
make lint       # go vet (+ staticcheck if installed)
make build      # compile; binaries land in ./bin/
make run ARGS="info photo.heic"
make difftest   # fetch the pinned upstream into .upstream/ and compare Go vs Python
```

`make difftest` compares this port against the real Python implementation at
the exact upstream commit in [`difftest/upstream.commit`](difftest/upstream.commit),
and requires their output to be byte-identical. See
[`difftest/doc.go`](difftest/doc.go) for its environment variables.

Drop real `.heic` files into `testdata/` to include them in round-trip tests.
The directory's contents are gitignored.

## Documentation

- [docs/SPEC.md](docs/SPEC.md): what "drop-in" guarantees, and the scope
- [docs/DESIGN.md](docs/DESIGN.md): architecture and key decisions
- [docs/COMPAT.md](docs/COMPAT.md): every deliberate difference from the Python tool
- [docs/UPSTREAM.md](docs/UPSTREAM.md): pinned upstream version and the Python → Go function map
- [docs/PLAN.md](docs/PLAN.md): implementation roadmap

## License

MIT. See [LICENSE](LICENSE). Portions are derived from [heic-lossless-rotate](https://github.com/olegoz/heic-lossless-rotate),
Copyright (c) 2026 Oleg Ostrozhansky.
