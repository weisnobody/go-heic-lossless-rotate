# Compatibility Notes

The Go port is a drop-in replacement for heic-lossless-rotate
**1.7.1** (see [SPEC.md § Compatibility Contract](SPEC.md#compatibility-contract)).
Output files are byte-identical, and operational messages and exit codes
match. This page lists **every** deliberate difference.

Each entry has an **allow-list ID**. The differential suite (`difftest/`)
skips Python-vs-Go comparisons only for cases tagged with one of these
IDs. An unexplained difference is a bug, either in the port or in this
page.

> **Status:** skeleton. The Go behaviors below are specified but not yet
> implemented. Each entry's *Implemented in* field says which phase lands it.

---

## D1: Default output path naming

| | |
|---|---|
| **ID** | `D1` |
| **Python** | `rotate`/`reverse` derive the default output path from `input.rsplit('.', 1)[0]` over the **whole path**, so a dot in a directory name, or a leading dot in the file name, counts as the extension. |
| **Go** | The extension is stripped from the **base name only**, and a base name that *starts* with its only dot (a dot-file) has no extension. `.heic` is always appended, and the `_restored`-suffix stripping in `rotate` works as in Python. |
| **Same for** | Every ordinary `dir/name.ext` input, which covers almost all real use. |
| **Implemented in** | Phase 3 (CLI), Phase 4 (`DefaultRotateOutput`/`DefaultReverseOutput`) |

| Input | Python `rotate` default | Go `rotate` default |
|---|---|---|
| `photo.heic` | `photo_rotated.heic` | `photo_rotated.heic` |
| `dir.v2/photo` | `dir_rotated.heic` | `dir.v2/photo_rotated.heic` |
| `.hidden` | `_rotated.heic` | `.hidden_rotated.heic` |
| `photo_restored.heic` | `photo.heic` | `photo.heic` |

## D2: Malformed input never crashes

| | |
|---|---|
| **ID** | `D2` |
| **Python** | Truncated or malformed boxes that the parsers don't explicitly check raise uncaught `struct.error`/`IndexError`, which prints a traceback to stderr and exits **1**. |
| **Go** | Every read is bounds-checked. The library returns `*BoxParseError` or `*ProvenanceError` and never panics on malformed input. The CLI prints `error: <message>` and exits **10** (`ErrorExitCode`). |
| **Same for** | Every malformed-input case Python already rejects with its own `BoxParseError`/`ProvenanceError`: same message, same exit code 10. |
| **Implemented in** | Phase 2 |

Example: a file whose `iloc` box is cut off mid-entry. Python prints
`struct.error: unpack_from requires a buffer of at least …` and exits 1.
Go prints `error: …` and exits 10.

## D3: Atomic output writes

| | |
|---|---|
| **ID** | `D3` |
| **Python** | Opens the output with `open(path, 'wb')` and writes in place, so a crash mid-write leaves a truncated file. |
| **Go** | Writes to a temp file in the output's directory, fsyncs it, then renames it over the target. When `-f` overwrites an existing file, that file's mode is kept. New files get `0666 &^ umask`, as in Python. |
| **Visible differences** | Overwriting replaces the inode: hard links to the old output aren't updated, and xattrs/ACLs on it aren't carried over. A stray `.heic-rotate-*` temp file can be left behind only if the process is killed between create and rename. |
| **Same for** | Output bytes, messages, exit codes, the `-f` refusal, and in-place editing (`input == output`). |
| **Implemented in** | Phase 4 |

## D4: Negative `iloc` offset after patching

| | |
|---|---|
| **ID** | `D4` |
| **Python** | If shrinking the provenance box (the v1→v2 record migration) would push an `iloc` extent offset below zero, `int.to_bytes` raises an uncaught `OverflowError` (traceback, exit 1). |
| **Go** | `*BoxParseError` (exit 10). |
| **Implemented in** | Phase 2 |

## D5: `iloc` field widths over 8 bytes

| | |
|---|---|
| **ID** | `D5` |
| **Python** | `iloc` offset/length/base-offset/index fields are read as arbitrary-precision ints, so any 4-bit width (0–15 bytes) is accepted. |
| **Go** | Widths other than those ISO/IEC 14496-12 defines (0, 4, 8) are accepted up to 8 bytes. Wider ones are refused with `*BoxParseError`. |
| **Implemented in** | Phase 2 |

## OSError message text (best effort)

| | |
|---|---|
| **ID** | `OSERR` |
| **Python** | File errors print as `error: [Errno 2] No such file or directory: 'x.heic'`, using the platform's `strerror` text. |
| **Go** | Formats the same way for ENOENT, EACCES, EISDIR, ENOTDIR, EEXIST, ENOSPC, and EROFS. Other errnos fall back to Go's error text in the same `[Errno N] …: '<path>'` shape. |
| **Same for** | Exit code (10) in every case. The custom `refusing to overwrite existing file …` message is exact. |
| **Implemented in** | Phase 2 (`pyfmt.go`), Phase 3 (CLI) |

---

## Go-only extensions

## E1: `-G` / `--goversion`

| | |
|---|---|
| **ID** | `E1` |
| **Python** | No such option, so it's a usage error (exit 2). |
| **Go** | Prints `GoVersionString()` and exits 0. It's a top-level option only, like `-V`, so `rotate -G …` is a usage error. |
| **Output** | `heic-lossless-rotate (Go port) 1.7.1+go.1` / `implements upstream heic-lossless-rotate 1.7.1` / `built with go1.x.y` |
| **Unaffected** | `-V`, the TVER provenance entry, and `info` still report only the upstream version (`1.7.1`). |
| **Implemented in** | Phase 3 |

---

## Not differences

These may look like differences, but they're covered by the contract:

- **`-h` help text and argparse usage-error wording** are allowed to differ
  (SPEC C6). Exit codes for them match: `-h` exits 0 and usage errors exit 2.
- **Files rotated by Go are indistinguishable from files rotated by Python.**
  TVER records `1.7.1` either way (SPEC C3). Use `-G` to identify a Go build.
