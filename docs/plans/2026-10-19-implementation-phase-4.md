# Plan: Phase 4 - File API with Atomic Writes

- **Date:** 2026-10-19
- **Status:** Approved
- **Covers:** [PLAN.md](../PLAN.md) Phase 4 (File API)
- **Upstream pin:** heic-lossless-rotate v1.7.1, commit `70d6c73`
- **Branch:** `feat/phase-4-file-api` (from `dev`)

## Requirements

- [ ] Exported: `RotateFile`, `ReverseFile`, `InfoFile`, `DefaultRotateOutput`, `DefaultReverseOutput`, `FileOptions`, `Result`, `OutputExistsError`
- [ ] The file API does everything `_run` does: reads the input, runs the structure check and the post-rotate self-check, picks the default output name, refuses to overwrite, handles dry-run, writes the output, and emits every `[ok]`/`[dry-run]` line (through `opts.Log`). The CLI's stdout stays byte-identical.
- [ ] Atomic writes (D3) that are indistinguishable from Python's in-place write in every probed case, except where noted below
- [ ] `cli.run` becomes a thin layer over the file API. The CLI gate stays green, and is extended with D3 cases.
- [ ] `TestSameFileInputOutput`, `TestDryRunSizeReport`, and the self-check test ported at the file-API level

## Resolved Review Points

| # | Question | Decision |
|---|---|---|
| 1 | Hard-linked outputs | Fall back to an in-place write, so the links survive (like Python), at the cost of atomicity in that case |
| 2 | Ownership on overwrite | Best-effort `Chown` to the previous owner (succeeds only as root) |
| 3 | Options type | One `FileOptions` for both functions; fields that don't apply are ignored and documented |

## Pre-Implementation Findings (Python write probe, 2026-10-19)

| Case | Python 3.14 (in-place `open('wb')`) | Naive temp + rename |
|---|---|---|
| Output is a symlink, `-f` | Writes through the link; the link stays | Replaces the link with a regular file |
| Output is a dangling symlink | Creates the target through the link | Replaces the link |
| Read-only existing output, `-f` | `[Errno 13] Permission denied: 'ro.heic'`, exit 10 | Succeeds |
| Existing output with mode 0640, `-f` | Mode stays 0640 | 0600 |
| Hard-linked output, `-f` | The other link sees the new content | The link is broken |
| New file (umask 022) | 0644 | 0600 |
| Output is a directory, `-f` | `[Errno 21] Is a directory: 'adir'` | Rename error with different text |

## API

```go
type FileOptions struct {
	Force        bool      // overwrite an existing output (-f)
	DryRun       bool      // do everything except write (--dry-run)
	NoExifSync   bool      // RotateFile only (--no-exif-sync)
	IgnoreTamper bool      // ReverseFile only (--ignore-tamper-check)
	Log          io.Writer // the CLI's stdout lines; nil = silent
}
type Result struct {
	Output    string
	Size      int
	InputSize int
	DryRun    bool
}
type OutputExistsError struct{ Path string } // upstream's message; errors.Is(err, fs.ErrExist)

func RotateFile(input, output string, deltaTurns int, opts FileOptions) (*Result, error)
func ReverseFile(input, output string, opts FileOptions) (*Result, error)
func InfoFile(path string) (*Info, error)
func DefaultRotateOutput(input string) string
func DefaultReverseOutput(input string) string
```

## Technical Approach

1. **Port `_run`'s rotate/reverse halves into `file.go`**, with the same order and messages. The self-check goes through an
   unexported `verifyReversibleHook`.
2. **`writeOutput`** follows Python's in-place write semantics as closely as possible:
   1. Resolve symlinks by hand (`Lstat`/`Readlink`, up to 40 hops, then `ELOOP`), including a dangling link's target.
   2. If the target exists:
      - If it's a directory, return `PathError{open, path, EISDIR}`.
      - If it isn't writable, return the error from an `O_WRONLY` (non-truncating) open.
      - Otherwise, record its mode, uid/gid, and link count.
   3. If the target has more than one hard link, write in place with `O_WRONLY|O_TRUNC` (Python's exact semantics).
   4. Otherwise write atomically:
      - Create the temp file with `OpenFile(dir/.heic-rotate-<random>, O_CREATE|O_EXCL|O_WRONLY, 0o666)` so the umask applies.
      - Write it and fsync.
      - On overwrite, restore the previous mode, plus the owner (best effort).
      - Close, rename over the target, then fsync the directory (Unix, best effort).
      - On any error, remove the temp file.
   5. If creating the temp file fails, re-wrap the error as `PathError{open, outputPath, errno}` so the message names the output path, as Python's does.
3. **`cli.run`** becomes dispatch-only. `errorText` learns `*OutputExistsError`. The CLI's own naming helpers and
   refuse error go away.

## Files

- `file.go`, `file_unix.go`, `file_other.go`, `file_test.go` (NEW)
- `cli/cli.go`, `cli/cli_test.go`: `run` uses the file API. The naming and self-check tests move to the root package.
- `difftest/cli_test.go`: `tree()` records mode and symlink targets. New D3 cases: symlink and dangling-symlink
  outputs, a read-only output with `-f`, a 0640 output with `-f`, a hard-linked output with `-f`, an output in a read-only directory.
- Docs: COMPAT D3, DESIGN, UPSTREAM, PLAN, README

## Commits

1. feat: file API with atomic writes + tests
2. refactor: move cli.run onto the file API (CLI gate green)
3. test: D3 cases in the CLI difftest
4. docs: Phase 4

## Risks

| Risk | Mitigation |
|---|---|
| Unprobed behavior (ACLs, xattrs, network filesystems) | Documented under D3. The rename stays in the same directory, so it's never a cross-device move. |
| The writability pre-check races with permission changes | Same window as Python's exists-check. Only the error shown can differ; the data can't. |
| Windows rename/mode semantics | Windows CI unit tests; mode tests are skipped on Windows |
| Self-check message coverage after moving the seam | A root test asserts the exact message; a CLI test checks that `errorText` passes it through |

## Dependencies

None (stdlib only; `syscall` on Unix).
