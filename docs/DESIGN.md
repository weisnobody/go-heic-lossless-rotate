# go-heic-lossless-rotate Technical Design

Upstream baseline: heic-lossless-rotate **v1.7.1**, commit `70d6c73`.
See [SPEC.md](SPEC.md) for the compatibility contract (C1–C6) and the
deliberate divergences (D1–D5).

## Architecture Overview

```
                ┌───────────────────────────────────────────────┐
 cmd/heic-rotate│                                               │
 cmd/heic-      │  internal/cli  (← cli.py)                     │
 lossless-rotate│   argparse-compatible parser, _run, messages  │
     main.go ──►│   Main(args, stdout, stderr) int              │
                └──────────────┬────────────────────────────────┘
                               │ uses
                ┌──────────────▼────────────────────────────────┐
                │  pkg/files   RotateFile / ReverseFile /       │  Go-only
                │     │        InfoFile (naming, overwrite      │
                │     │        check, atomic write)             │
                │     ▼                                         │
                │  pkg/stream  ReaderAt engine  ─┐              │  Go-only
                │                                │ shares       │
                │  pkg/core    (← core.py, 1:1) ◄┘ orchestration│
                │     []byte in → []byte out       parsers, TLV │
                │  pkg/version (← _version.py)                  │
                │  internal/pybytes  Python slice/index semantics│
                └───────────────────────────────────────────────┘
```

- **`pkg/core` is the reference implementation.** `core.go` is a line-by-line
  port of `core.py` working on `[]byte`, kept in the same order with the same
  function names and comments, so the two files can be diffed by eye.
- **`pkg/stream` is a second engine** over `io.ReaderAt`. It runs core's
  orchestration on an in-memory copy of the `meta`+provenance span, through
  the Go-only `core.FileEnv` hooks, and streams everything else. It must be
  byte-identical to the `[]byte` engine, which a dedicated cross-check test
  enforces (the same way upstream cross-checks `verify_reversible` against
  `reverse_rotation`).
- **`pkg/files`** is the convenience layer that the CLI calls.
- Each package depends only on the ones below it in the diagram:
  `files` → `stream` → `core` → `version`, `internal/pybytes`, `internal/pyfmt`.

## Technology Choices

| Concern | Choice | Reasoning |
|---|---|---|
| Language | Go, `go 1.23` minimum in `go.mod` | Range-over-func iterators (`iter.Seq2`) give a faithful port of Python's lazy `iter_boxes` generator. 1.23 is old enough for library consumers. Developed on 1.26. |
| Dependencies | **stdlib only** (library *and* CLI) | Matches upstream. Importers get no transitive deps. |
| CRC32 | `hash/crc32` IEEE | Same polynomial and init as `zlib.crc32`, which is required for C1. |
| CLI parsing | Hand-written argparse-compatible parser (`internal/cli/args.go`) | `flag` can't do flags after positionals, flags on both sides of the subcommand, or argparse prefix abbreviations. |
| Release | goreleaser | Two builds (`heic-rotate`, `heic-lossless-rotate`), darwin/linux/windows × amd64/arm64, checksums, GitHub release. |
| Module path | `github.com/pjg/go-heic-lossless-rotate` | `go install …@latest` works for anyone. |
| Hosting | **Primary:** `arena.the42.net/pjg/go-heic-lossless-rotate` (origin). **Mirror:** GitHub, via push mirror of all branches **and tags** | All development and pushes go to arena. The Go module proxy, `go install` users, and goreleaser all read from GitHub, so the mirror must never lag a tag. |
| CI / release | GitHub Actions on the mirror | It runs tests on push and goreleaser on a tag push, triggered when the mirror sync delivers the tag. No arena CI is assumed. |

## Repository Layout

```
go.mod                          module github.com/pjg/go-heic-lossless-rotate
pkg/core/doc.go                 ← heic_rotate/__init__.py + module docstrings
pkg/core/core.go                ← core.py       (1:1 port)
pkg/core/pyerrors.go            IndexError/struct.error as *BoxParseError (COMPAT D2)
pkg/core/*_test.go              ← tests/test_heic_rotate.py (ported; package core_test)
pkg/version/version.go          ← _version.py   Version = "1.7.1", GoRevision, GoVersion
pkg/stream/stream.go            streaming engine (Go-only)
pkg/files/file.go               file-level API (Go-only)
internal/cli/cli.go             ← cli.py        Main, buildParser, insertImplicitRotate, run
internal/cli/args.go            argparse-compatible parser (Go-only)
internal/pybytes/               Python slice/index/struct semantics
internal/pyfmt/                 Python repr/format helpers (shared with synth and the CLI)
internal/synth/synth.go         ← tests/synth_heic.py
internal/craft/                 crafted-structure builder for the corpus tests
internal/tests/                 test helpers shared across packages
cmd/heic-rotate/main.go         os.Exit(cli.Main(os.Args, os.Stdout, os.Stderr))
cmd/heic-lossless-rotate/main.go  (identical)
difftest/                       differential suite vs Python (build tag `difftest`)
testdata/                       real .heic files (contents gitignored, like upstream)
docs/COMPAT.md                  every divergence from Python, with examples
docs/UPSTREAM.md                pinned commit, porting log, Python↔Go function map
.goreleaser.yaml
```

The library is four importable packages under `pkg/`; the module root holds no package:

| Package | Import path | Contents |
|---|---|---|
| `core` | `github.com/pjg/go-heic-lossless-rotate/pkg/core` | The 1:1 `core.py` port over `[]byte`: `ApplyRotation`, `ReverseRotation`, `GatherInfo`, the parsers and builders, the error types |
| `stream` | `…/pkg/stream` | `ApplyRotationReaderAt` and friends, returning a `View` |
| `files` | `…/pkg/files` | `RotateFile`, `ReverseFile`, `InfoFile`, default output naming, atomic writes |
| `version` | `…/pkg/version` | `Version`, `GoRevision`, `GoVersion`, `GoVersionString` |

Tests sit next to the code they test. Helpers that more than one package's
tests use (`MustSynth`, `MustApply`, the upstream-ported `StripProvenanceTag`
and `MakeV1StyleRecord`, …) live in `internal/tests`. Core's tests are an
external test package (`core_test`) so they can import those helpers;
`pkg/core/export_test.go` exposes the few unexported names they need.

## Naming Rules (Python → Go)

1. `snake_case` becomes `CamelCase`, keeping upstream spelling (`Iloc`, `Ipma`, `Irot`,
   `Iprp`, `Ccrc`/`Pcrc` aren't re-cased as initialisms).
2. Public Python names are **exported**. `_leading_underscore` names are
   **unexported**.
3. Python constants map to Go `const`s or package-level vars with the same
   name in CamelCase: `FORMAT_VERSION` → `FormatVersion`.
4. Python keyword args with defaults become an options struct whose
   **zero value equals Python's default**, except `verbose`, which becomes
   `Log io.Writer` (nil means silent). Library callers get no stdout output, and
   the CLI passes `os.Stdout` to reproduce Python's output exactly.
5. Python `raise X(msg)` becomes `return …, &X{Msg: msg}`. `raise … from e`
   sets an `Err` field exposed via `Unwrap()`.

### Function map

The full Python → Go function map lives in [UPSTREAM.md](UPSTREAM.md#python--go-function-map),
next to the pinned upstream version it describes.

## Data Models

Python tuples and dicts become structs. Field names follow the Python
tuple positions or dict keys.

```go
// read_box_header's (start, header_len, content_start, content_end, boxtype)
type BoxHeader struct {
    Start, HeaderLen, ContentStart, ContentEnd int
    Type [4]byte
}

type IpmaAssoc struct { PropIndex int; Essential bool }
type IpmaEntry struct { ItemID uint32; Assocs []IpmaAssoc }

type Iloc struct {
    Version uint8; Flags uint32
    OffsetSize, LengthSize, BaseOffsetSize, IndexSize int
    Items []IlocItem
}
type IlocItem struct {
    ItemID uint32
    ConstrMethod int
    RawConstrField *uint16      // nil ⇔ Python None (iloc v0)
    DataRefIndex uint16
    BaseOffset uint64
    Extents []IlocExtent
}
type IlocExtent struct { Index *uint64; Offset, Length uint64 } // Index nil ⇔ None

type ProvenanceEntries map[string][]byte // 4-char tag → value

type Info struct {                       // gather_info's dict; pointer ⇔ key absent
    CurrentQuarterTurns  int
    HasProvenance        bool
    OriginalQuarterTurns *int
    DeltaQuarterTurns    *int
    Version              uint8
    VersionSupported     bool
    ToolVersion          *[3]uint8
    PCRC                 *uint32
    HasIlocSnapshot, HasExifSnapshot bool
}
```

Python ints are arbitrary-precision, while Go's are not. Arithmetic on `iloc`
offsets uses `int64`/`uint64` with explicit overflow and underflow checks that
return `BoxParseError` (D4, D5). Every slice access is bounds-checked
up front so that truncated input produces an error instead of a panic (D2).

### Python byte-access semantics (`internal/pybytes`)

`core.py` relies on three different behaviors when an access runs past the
end of the data. They decide what "byte-identical on every input Python
accepts" means in practice:

| Python | Past the end of `data` | Go helper |
|---|---|---|
| `data[a:b]`, `memoryview[a:b]` | clamps; `a > b` gives empty | `pybytes.Slice` (aliases), `SliceCopy`, `Slice64` |
| `int.from_bytes(data[p:p+n])` | silently reads a *shorter* value | `pybytes.IntFromBytes` (reports >64-bit values) |
| `data[i]`, `struct.unpack_from` | `IndexError` / `struct.error` | `pybytes.Index`, `UnpackU16/32/64` report `ok=false`; core's `pyIndex`, `unpackU16/32/64` (`pyerrors.go`) turn that into `*BoxParseError` (D2) |
| `buf[a:b] = new` | replaces the clamped range, the length may change | `pybytes.Splice` (in place when capacity allows) |

Parsers read across box boundaries exactly as Python does (bounds are
`len(data)`, never the box end). Go-only refusals (D5, D7) are deferred
until after the point where Python's own parse would succeed, so they never
replace an error Python reports.

## Python-compatible formatting (`internal/pyfmt`)

C5 requires reproducing Python's string formatting exactly. Verified
differences that make a helper mandatory:

| Python | Naive Go | Helper |
|---|---|---|
| `f"{x:#010x}"` → `0x000000ab` | `%#010x` → `0x00000000ab` ✗ | `pyfmt.Hex08(uint32)` = `fmt.Sprintf("0x%08x", x)` |
| `f"{boxtype}"` (bytes) → `b'irot'`, `b"a'b"`, `b'\x00'` | — | `pyfmt.BytesRepr([]byte)` (CPython quote selection + `\x`/`\t`/`\n`/`\r`/`\\` escapes) |
| `list(enumerate(prop_types, 1))` → `[(1, 'ispe'), (2, 'irot')]` with `decode('ascii','replace')` → U+FFFD | — | `pyfmt.EnumerateList`, `pyfmt.StrRepr`, `pyfmt.DecodeASCIIReplace` |
| `primary_entry[1]` → `[(1, True), (2, False)]` | — | `pyfmt.IntBoolList` |
| `f"{n:+d}"` | `%+d` ✓ | — |
| `OSError` → `[Errno 2] No such file or directory: 'x.heic'` | `open x.heic: no such file or directory` ✗ | `pyfmt.OSError(op, path, err)` (Phase 3) maps `syscall.Errno` to `[Errno N] <Strerror>: <repr(path)>`. Exact for the common errnos (ENOENT, EACCES, EISDIR, ENOTDIR, EEXIST, ENOSPC, EROFS), best-effort otherwise (listed in COMPAT.md). |

## Key Decisions

### Byte-identical output and mirrored version
- **Choice:** `Version = "1.7.1"` is written into TVER and shown by `-V` and `info`.
  Go output equals Python output byte for byte.
- **Reasoning:** This makes differential testing a simple `bytes.Equal`, and
  a file's provenance stays meaningful regardless of which implementation
  wrote it.
- **Trade-offs:** You can't tell from a file whether Go or Python touched it.
  `-G` (below) is how you find out which Go build you're running.

### Go port version and release tags
- **Choice:** `pkg/version` holds both versions:
  ```go
  const Version = "1.7.1"                  // ← _version.py; TVER, -V, info
  const GoRevision = 1                     // bump for each Go-only release
  var   GoVersion = Version + "+go." + strconv.Itoa(GoRevision) // "1.7.1+go.1"
  ```
  `-G`/`--goversion` (SPEC E1) prints:
  ```
  heic-lossless-rotate (Go port) 1.7.1+go.1
  implements upstream heic-lossless-rotate 1.7.1
  built with go1.26.5
  ```
  The third line comes from `runtime.Version()`. The function is `version.GoVersionString()`;
  `-V`'s text stays `core.VersionString()`, next to the rest of the core port.
- **Tags:**
  - The first Go release for an upstream version (`+go.1`) is tagged plain **`vX.Y.Z`**.
  - Later Go-only fixes are tagged **`vX.Y.Z+go.N`** (N ≥ 2).
  - When upstream bumps its version, `GoRevision` resets to 1.
  - A release-workflow check fails the build unless the tag equals `v`+`Version`
    (for revision 1) or `v`+`GoVersion` (for revision 2+).
- **Toolchain constraint (verified in `cmd/go/internal/modfetch/coderepo.go`):**
  the Go module system **ignores non-canonical tags**, so `+go.N` tags never
  show up in `go list -m -versions` or `@latest`.
  - `go install …@latest` resolves to the plain `vX.Y.Z` tag, i.e. the `+go.1` build.
  - Go-only fixes reach `go install` users only through
    `@vX.Y.Z+go.N` (resolved to a pseudo-version) or `@<commit>`.
  - GitHub release binaries (goreleaser) carry every `+go.N`.
  - We accept this: Go-only fixes should be rare, and the README documents the
    explicit `@vX.Y.Z+go.N` form.
- **Trade-offs:** `go install @latest` can lag behind Go-only fixes until
  the next upstream version bump.

### One orchestration, two engines (streaming)
- **Choice:** `core.go` keeps `core.py`'s functions and order. `ApplyRotation` and `prepareReversal`
  reach anything outside the buffer they edit through a small `core.FileEnv` interface with six
  operations: the Exif lookup on input and output, the Exif write, PCRC, CCRC on input and output, and the
  base file offset. `memEnv` (the `[]byte` engine) has the whole file in the buffer. `streamEnv`
  (`pkg/stream`) edits only the meta+provenance span and reads the rest through an `io.ReaderAt`.
  Both engines run the same orchestration, so they can't drift. Reverse shares
  `ReconstructedPlan`, the chunk sequence that `iterReconstructedChunks` walks and the streaming engine turns into a
  `View`.
- **Go-only hooks:** because the engines are separate packages, the pieces `pkg/stream` calls are exported
  from `pkg/core` and documented as Go-only: `FileEnv`, `ApplyRotationEnv`, `PrepareReversalEnv`,
  `Reversal` (opaque), `Chunk`, `ReconstructedPlan`, `CheckReconstructedCrc`, `ErrNoProvenance`,
  `ExifFieldInBlob`, and `ListBoxes`. They aren't part of upstream's API, and callers shouldn't need them.
- **Why the span is enough:** every edit falls inside `[meta_start, prov_end)` plus at most one 2-byte
  Exif patch, an argument upstream's `reverse_rotation` docstring makes itself.
- **Exactness by prechecks plus fallback:** the core's parsers, like upstream's, read past box ends.
  Inside the span that sees the same bytes as the whole file; past the span's end it wouldn't. So
  `loadStreamInput` streams only when:
  - the top level tiles the file exactly, with one `meta` box (32-bit header) and the first provenance box, if any, right after it
  - the span passes `VerifyStructure`
  - the boxes read at fixed offsets (`pitm`, `infe`, `irot`, provenance) are big enough
  - no absolute-offset iloc extent points into the span

  Under those conditions every read stays in the span or ends in an error. **Any failed precheck, and
  any error, falls back** to reading the whole input and running the `[]byte` engine, which gives the authoritative result.
  The one exception is `no provenance record` on reverse, which is exact without the full input. Output-side Exif patches are kept in
  input offsets, so they survive later span resizes. The Exif blob is read via `ReadAt`, capped at 1 MiB, and a larger blob falls back.
- **Memory:** O(meta) for well-formed files. Rotating a 64 MiB file allocates about 0.7 MB in about 170 ms,
  and reverse about 0.45 MB, against about 2× the file before. Malformed or unusual files (64-bit or to-EOF `meta`, over-64-bit
  iloc values, corruption) fall back and cost what they always did.
- **Trade-offs:** a rotate makes about 4–5 sequential passes over the input (PCRC, CCRC, the self-check's two CRCs,
  the write), which the page cache absorbs. `pkg/stream/stream_test.go` cross-checks both engines over 17,569 inputs,
  including a corruption sweep, and requires every successful operation on a well-formed file to stream.

### File API (`pkg/files`)
- **Choice:** `RotateFile`/`ReverseFile` are the rotate and reverse halves of `cli.py`'s `_run`. They
  open the input as an `*os.File` and run the streaming engine. A non-regular input is read into memory. Then come
  the structure check and (for rotate) the post-rotate self-check through `verifyReversibleHook`, both on the `View`,
  which is then streamed into the atomic temp file. The input is closed before the rename, and when the
  in-place hard-link path would overwrite the input itself, the `View` is materialized first. After that they apply default naming (D1) and the overwrite refusal
  (`*OutputExistsError`, `errors.Is(err, fs.ErrExist)`), then dry-run or write. Every `[ok]`/`[dry-run]`
  line goes through `FileOptions.Log`. `cli.run` only maps flags onto them. `InfoFile` reads the file and calls `GatherInfo`.
- **Reasoning:** library callers get the CLI's safety checks, and the write path has a single
  implementation, which the CLI difftest covers.

### Atomic writes (D3)
- **Choice:** `writeOutput` writes a temp file in the output's directory, fsyncs it, and renames it over the
  target. The Python probe (see the Phase 4 plan) showed seven ways a naive version would visibly
  differ from Python's in-place `open('wb')`. Each is handled:
  1. Symlinks are resolved first (`Lstat`/`Readlink`, 40 hops, then `ELOOP`), so writes go through a link.
     A dangling link's target gets created.
  2. An existing directory gets `EISDIR`, and an unwritable file gets the error from an `O_WRONLY`
     open (no truncation). Both match Python's message.
  3. A hard-linked target (`Nlink > 1`) is written in place with Python's exact semantics.
  4. The temp file is created with `OpenFile(…, O_EXCL, 0o666)` (`os.CreateTemp` would force 0600), so
     the umask applies as it does for Python's new files. On overwrite, the old mode is restored and the
     owner is restored best effort.
  5. Every error is re-labelled as an `open` error on the output path, so messages name it as
     Python's do.
- **Reasoning:** a crash or full disk can't truncate the output, and an in-place edit can't destroy the
  original.
- **Trade-offs:** see COMPAT D3. A non-hard-linked overwrite loses xattrs/ACLs, the owner is restored only
  when permitted, and the exists-check-to-rename window is the same small race as in Python.

### argparse-compatible CLI parser
- **Target:** Python 3.14's argparse (COMPAT PYARGS). Behavior captured from the
  real parser lives in `internal/cli/testdata/argparse_py314.json` (184 argv cases).
- **Processing order** decides the exit code when an argv has several problems,
  so it's reproduced too. `internal/cli/args.go` mirrors `_parse_optional`,
  `_get_option_tuples`, and `consume_optional`:
  - arguments are processed left to right
  - a positional is converted when it's consumed (`rotate 45 x -h` exits 2)
  - `-h`/`-V`/`-G` act as soon as they're reached
  - unknown options are deferred (`--bogus -h` exits 0)
  - an unknown character ends a combined short-flag group, leaving the earlier
    flags in effect and deferring the rest (`-qz -h` exits 0, `-hz` shows help)
  - an explicit argument after `=` or starting with `-` is an immediate error
    (`-q=1`, `-f-q`, `--quiet=1`)
  - only the first `--` is consumed; later ones are values
  - negative-number-like arguments, arguments containing spaces, `-`, and `''`
    are positionals
- **Choice:** A small declarative parser in `internal/cli/args.go` that reproduces:
  - top-level `-q/--quiet`, `--dry-run`, `-f/--force` accepted before **or**
    after the subcommand (Python's `default=SUPPRESS` layering)
  - interspersed flags and positionals, `--` terminator, and combined short
    flags (`-qf`)
  - unambiguous long-option prefix abbreviation (`--dry` → `--dry-run`),
    since argparse's `allow_abbrev` defaults to True
  - per-subcommand flag sets: `info` rejects `-f`/`--dry-run` *after* the
    subcommand but accepts them before it, exactly as Python does
  - `rotation` validated like `type=int, choices=[0,90,180,270]`, with
    `parsePyInt` mirroring `int()` (Unicode White_Space trimmed, an optional
    sign, `_` only between digits, any Unicode `Nd` digit)
  - `insertImplicitRotate` ported verbatim
  - no args → help on stdout, exit 2. `-h` → exit 0. Usage errors → stderr,
    exit 2. `-V` → `VersionString()` on stdout, exit 0. `-G`/`--goversion`
    → `GoVersionString()` on stdout, exit 0 (Go-only, top-level only, like `-V`).
    It joins the long-option abbreviation set, so `--gov` works and `--go` is
    unambiguous because no other long option starts with `go`.
- **Help text** (C6, may differ) uses argparse's layout. The top-level description is
  upstream's module docstring, generated into `internal/cli/docstring.go` by
  `tools/gen_docstring.py`.
- **Trade-offs:** About 450 lines of custom code. It's covered by the captured table, the
  ported CLI tests, and the CLI difftest (2,000 generated argv sequences against the
  real parser).

### Default output naming fix (D1)
- **Choice:** `stem = strings.TrimSuffix(path, filepath.Ext(base))`, where an
  extension only counts if it isn't the whole base name (`.hidden` keeps its name), and
  `.heic` is always appended. The `_restored`-suffix stripping logic is otherwise identical.
- **Trade-offs:** It differs from Python only for paths whose directory part
  contains a dot and whose file name has no extension, or for dot-files.

## Testing Architecture

| Layer | What | Runs |
|---|---|---|
| **Ported unit and integration tests** | Every upstream `TestXxx` class becomes a Go test (see PLAN.md for the mapping) | `go test ./...` always |
| **`internal/synth`** | Port of `synth_heic.py`. A difftest asserts it emits bytes identical to the Python generator for every parameter combination. | always + difftest |
| **Engine cross-check** | `ApplyRotation` vs the streaming engine, and `ReverseRotation` vs `VerifyReversible` vs the streaming engine, over the whole corpus | always |
| **Differential** (`-tags difftest`) | Runs `python3 -m heic_rotate` (PYTHONPATH = `$HEIC_ROTATE_PY_SRC`) and Go `cli.Main` on the same argv and fixtures, and compares output bytes, stdout, stderr, and exit codes. Also runs cross-reversal (Go rotate → Py reverse, Py rotate → Go reverse) and parser argv tables. COMPAT.md cases are excluded via an allow-list. | `make difftest`, CI job with Python 3.14 + upstream checkout at the pinned commit. The CLI is driven in-process through pydriver's `cli` op, plus a few real `python -m heic_rotate` processes. |
| **Real files** | `testdata/*.heic` round-trip, plus rotated copies to `rotated/` for visual checks | when testdata is present |

Python-specific upstream tests get Go equivalents:
- `TestSharedReconstructionCode` (bytecode inspection) becomes a test that both
  `ReverseRotation` and `VerifyReversible` consume the identical chunk
  sequence from `iterReconstructedChunks`, via a package-level test hook.
- `test_avoids_full_file_size_allocation` (tracemalloc) becomes a
  `runtime.MemStats`/`testing.AllocsPerRun` bound on `VerifyReversible`, plus
  a heap bound on the streaming engine.
- `TestReturnTypeAvoidsExtraCopy` becomes an assertion that `ApplyRotation`'s
  output doesn't alias the input and is allocated once (benchmark
  `-benchmem` check). It's partly N/A since Go has no bytes/bytearray split.

## Technical Risks

| Risk | Mitigation |
|---|---|
| Subtle formatting drift (reprs, hex widths, OSError text) breaks C5 | The `internal/pyfmt` helpers have unit tests against literal Python output plus a difftest against the live interpreter, and the difftest compares full log text |
| Lazy-vs-eager iteration changes which malformed files are accepted | `IterBoxes` stays a lazy `iter.Seq2`. Difftest fixtures include files with garbage after the needed box. |
| Python crash paths (D2) are hard to enumerate | Bounds-check every read with a shared `need(data, pos, n)` helper. Fuzzing comes post-MVP. The CLI also `recover()`s as a last-resort net (exit 10, "internal error"), and tests assert it's never hit. |
| The streaming engine diverges from core | The cross-check test is a release gate. The engine reuses core's parsers and builders, so only the I/O plumbing is new. |
| Upstream ships a breaking release (for example its planned streaming refactor) | Pinned commit in UPSTREAM.md. Each release is ported deliberately and difftested before the mirrored version is bumped. |
| The module path points at the GitHub mirror, so a sync failure means `go install` and releases silently miss a tag | Mirror tags as well as branches. The release workflow only runs from the mirror's tag push, so a missing tag shows up as a missing release. Before announcing, verify with `git ls-remote --tags github`. Never push directly to GitHub, which would cause the mirror to diverge. |
| Users expect `go install @latest` to include Go-only fixes | README documents `@vX.Y.Z+go.N`, and `-G` shows exactly which build is installed |
| CPython `int()` / argparse edge cases (`rotate ' 90'`, `--for` ambiguity) | An argv table in the difftest compares exit codes against real argparse |
