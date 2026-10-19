# Compatibility Notes

The Go port is a drop-in replacement for heic-lossless-rotate
**1.7.1** (see [SPEC.md § Compatibility Contract](SPEC.md#compatibility-contract)).
Output files are byte-identical, and operational messages and exit codes
match. This page lists **every** deliberate difference.

Each entry has an **allow-list ID**. The differential suite (`difftest/`)
skips Python-vs-Go comparisons only for cases tagged with one of these
IDs. An unexplained difference is a bug, either in the port or in this
page.

> **Status:** D1, D2, D4–D7, OSERR, E1, and PYARGS are implemented and
> checked by the difftest gates (core: Phase 2; CLI: Phase 3). D3 is
> specified but lands with the Phase 4 file API.

---

## D1: Default output path naming

| | |
|---|---|
| **ID** | `D1` |
| **Python** | `rotate`/`reverse` derive the default output path from `input.rsplit('.', 1)[0]` over the **whole path**, so a dot in a directory name, or a leading dot in the file name, counts as the extension. |
| **Go** | The extension is stripped from the **base name only**, and a base name that *starts* with its only dot (a dot-file) has no extension. `.heic` is always appended, and the `_restored`-suffix stripping in `rotate` works as in Python. |
| **Same for** | Every ordinary `dir/name.ext` input, which covers almost all real use. |
| **Implemented in** | Phase 3 (CLI). Phase 4 exports it as `DefaultRotateOutput`/`DefaultReverseOutput`. |

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
| **Python** | Malformed content that the parsers don't explicitly check raises an uncaught `struct.error`, `IndexError`, `KeyError` (a missing provenance entry), or `TypeError`, which prints a traceback to stderr and exits **1**. |
| **Go** | Every read is bounds-checked and every lookup checked. The library returns `*BoxParseError` (structure) or `*ProvenanceError` (provenance entries) and never panics on malformed input. The CLI prints `error: <message>` and exits **10** (`ErrorExitCode`). |
| **Same for** | Every case Python rejects with its own `BoxParseError`/`ProvenanceError`: same type, same message, same exit code 10. Truncating a file never crashes Python - a cut-off top-level box is always caught as a typed error - so truncated files behave identically. |
| **Implemented in** | Phase 2 |

Example: a provenance record whose `PCRC` entry is 3 bytes long. Python
prints `struct.error: unpack requires a buffer of 4 bytes` (traceback,
exit 1); Go returns `ProvenanceError: provenance entry 'PCRC' is 3 bytes,
expected 4` (exit 10). In the difftest gate's single-byte corruption sweep,
about 2.3% of cases are D2.

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

## D5: `iloc` values over 64 bits

| | |
|---|---|
| **ID** | `D5` |
| **Python** | `iloc` offset/length/base-offset/index fields are arbitrary-precision ints, so any 4-bit width (0–15 bytes) and any value is accepted. |
| **Go** | Every width 0–15 is accepted too. Only a *value* that doesn't fit in 64 bits is refused (`*BoxParseError`) - whether read from a 9+-byte field or produced by patching one. Such an offset points far beyond any real file. |
| **Ordering** | The refusal happens only after the point where Python's own parse would succeed. Where Python reports an error first (e.g. `iloc parse mismatch`), Go reports that same error. |
| **Implemented in** | Phase 2 |

## D6: Zero-size 64-bit box

| | |
|---|---|
| **ID** | `D6` |
| **Python** | A box whose size field is 1 (64-bit size follows) and whose 64-bit size is 0 never advances, so `iter_boxes` and `verify_structure` loop forever. |
| **Go** | `*BoxParseError`: `box b'free' at N has a 64-bit size of 0 and cannot be stepped over`. A lookup that stops at that very box (as Python's `find_child` would) still returns it. |
| **Implemented in** | Phase 2 |

## D7: Runaway zero-width `iloc` extents

| | |
|---|---|
| **ID** | `D7` |
| **Python** | With offset, length, and index sizes all 0, extents occupy no bytes, so a file can declare up to 65535 per item across many items; Python materializes every one (gigabytes for a crafted file). |
| **Go** | Parses up to 2^20 (1,048,576) zero-width extents - matching Python exactly - and refuses beyond that with `*BoxParseError`. Exceeding the cap takes 17+ items of 65535 empty extents each. |
| **Ordering** | As with D5, only after the point where Python's own parse would succeed. |
| **Implemented in** | Phase 2 |

## OSError message text (best effort)

| | |
|---|---|
| **ID** | `OSERR` |
| **Python** | File errors print as `error: [Errno 2] No such file or directory: 'x.heic'`, using the platform's `strerror` text. |
| **Go** | Formats the same way for ENOENT, EACCES, EISDIR, ENOTDIR, EEXIST, ENOSPC, and EROFS. Other errnos fall back to Go's error text in the same `[Errno N] …: '<path>'` shape. |
| **Same for** | Exit code (10) in every case. The custom `refusing to overwrite existing file …` message is exact. |
| **Implemented in** | Phase 3 (`pyfmt.OSError`, used by the CLI) |

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

## PYARGS: argparse version

| | |
|---|---|
| **ID** | `PYARGS` |
| **Python** | argparse's behavior varies by version. For example, Python 3.11 rejects `rotate 90 x -q y` (an optional positional after a flag) and `-- rotate 90 x`, both of which 3.14 accepts. |
| **Go** | Follows **Python 3.14**, including its processing order: positionals are converted as they're consumed, `-h`/`-V` act as soon as they're reached, unknown options are deferred to the end, an unknown character in a combined short-flag group is deferred (`-qz -h` shows help), and only the first `--` is consumed. The CLI difftest runs on 3.14 and skips itself on older interpreters. |
| **Rotation parsing** | `rotation` is parsed like Python's `int()`: Unicode whitespace, sign, `_` between digits, and any Unicode decimal digit (`'９０'` is 90). Go's Unicode tables are version 15.0, Python 3.14's are 16.0, so digits from scripts added in Unicode 16 (for example Gurung Khema, Kirat Rai, Ol Onal) are accepted by Python but rejected by Go (exit 2). |
| **Implemented in** | Phase 3 |

---

## Not differences

These may look like differences, but they're covered by the contract:

- **`-h` help text and argparse usage-error wording** are allowed to differ
  (SPEC C6). Exit codes for them match: `-h` exits 0 and usage errors exit 2.
- **Files rotated by Go are indistinguishable from files rotated by Python.**
  TVER records `1.7.1` either way (SPEC C3). Use `-G` to identify a Go build.
