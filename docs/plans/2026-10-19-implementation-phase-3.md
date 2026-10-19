# Plan: Phase 3 - CLI Port

- **Date:** 2026-10-19
- **Status:** Implemented 2026-10-19. The gate passed (2,556 CLI cases, zero unexplained differences).
  Deviations from this plan:
  - The generated argv sweep found that Python 3.12+ defers an unknown character in a
    combined short-flag group (`-qz -h` shows help), contradicting the plan's
    "immediate error" assumption. The parser follows the real `consume_optional`, and the
    captured table grew to 184 cases.
  - `parsePyInt` strips Unicode White_Space only. A full-Unicode probe showed `int()` doesn't
    strip `str.isspace()`'s extra `U+001C`-`U+001F`.
  - New COMPAT entry PYARGS (argparse version, plus Unicode 15-vs-16 digits in `rotation`).
  - The generated sweep grew from about 400 to 2,000 sequences (about 2 s).
- **Covers:** [PLAN.md](../PLAN.md) Phase 3 (CLI port), plus the COMPAT allow-list moved from Phase 1 and the CLI self-check test deferred from Phase 2
- **Upstream pin:** heic-lossless-rotate v1.7.1, commit `70d6c73`
- **Branch:** `feat/phase-3-cli` (from `dev`)

## Requirements

- [ ] `cli.Main(argv, stdout, stderr) int` is a 1:1 port of `cli.py` (`build_parser`, `_insert_implicit_rotate`, `main`, `_run`), stdlib only
- [ ] **SPEC C4/C5:** exit codes, every operational stdout line, and every `error: ...` message match Python exactly, including OSError text (`[Errno 2] No such file or directory: 'x.heic'`)
- [ ] **C6:** help and usage-error *text* may differ, but exit codes match: `-h` gives 0; no args gives 2 with help on stdout; a usage error gives 2 on stderr
- [ ] **D1:** default output naming uses the base-name rule. **E1:** `-G`/`--goversion`, top-level only
- [ ] `heic-rotate` and `heic-lossless-rotate` binaries; a Makefile `run` target
- [ ] Ported tests: `TestCLI` (17 methods), `test_cli_self_check_aborts_and_writes_nothing_on_failure`, `TestSameFileInputOutput`, `TestDryRunSizeReport`
- [ ] **Gate:** zero unexplained CLI differences against Python. Every allowed difference must be explained by the COMPAT allow-list.

## Resolved Review Points

| # | Question | Decision |
|---|---|---|
| 1 | Which argparse behavior to match? | **Python 3.14's.** CI difftest runs on 3.14. |
| 2 | Help text | **Includes upstream's full module docstring**, as Python's `-h` does |
| 3 | Writes in Phase 3 | **Non-atomic**, matching Python exactly. D3 lands with the Phase 4 file API. |

(Accepted as proposed.)

## Pre-Implementation Findings (argparse probe, 2026-10-19)

The probe ran 97 argv cases through upstream's real parser on Python 3.11 and 3.14.

- **The versions differ** on `rotate 90 x -q y` (3.11 rejects the output after a flag; 3.14
  accepts it) and on `-- rotate 90 x` (3.11 rejects; 3.14 strips `--`).
- **`int()` leniency:** `090`, `+90`, ` 90`, `9_0`, and `٩٠` all parse as 90. `90.0` and `0x5a`
  are rejected.
- **Order-dependent exits:**
  - `rotate 45 x -h` exits 2, because conversion happens before `-h` is reached. `rotate -h 45 x` exits 0.
  - `--bogus -h` exits 0 (unknown options are deferred). `bogus -h` exits 2 (the invalid subcommand is checked first).
  - `-qz` is an immediate error. `--quiet=1` is an immediate error.
- **Argument classification:** `-x.heic` is parsed as an option, while `-5`, `-1.5`, `-`, and `''` are positionals.
- **Subcommand boundary:** top-level flags work only before the subcommand. `info x -f` and `info x --dry-run` are errors, while `-f info x` works.

## Technical Approach

1. **Match Python 3.14's argparse; CI difftest on 3.14.** Difftest cases known to be
   version-sensitive are skipped (and logged) on older interpreters.
2. **`cli/args.go` reproduces argparse's processing order:**
   - Arguments are processed left to right.
   - Positionals are converted as they're consumed.
   - The first top-level positional selects the subcommand, and everything after it goes to the subparser.
   - Unknown options are deferred (reported at the end as "unrecognized arguments").
   - Help, version, and `-G` act immediately.
   - Unique long-option prefixes are accepted. `--opt=value` on a flag is an immediate error.
   - Short flags can be combined. An unknown character later in a combined group is an immediate error, but an unknown first character is deferred.
   - These count as positionals: negative-number-like arguments (`^-\d+$|^-\d*\.\d+$`), arguments containing a space, `-`, and `''`.
   - `--` ends option parsing.
   - `parsePyInt` reproduces Python's `int()`.
3. **Difftest runs Python's CLI in-process** through a pydriver `cli` op (patched `sys.argv`, chdir,
   captured streams, the `SystemExit` code, and an uncaught exception recorded as a traceback with exit 1), plus a few
   real-subprocess sanity cases.
4. **`pyfmt.OSError(err)`** produces `[Errno N] <Strerror>: '<path>'`. On Unix, Strerror is the errno with Go's text
   capitalized. On Windows, best-effort `errors.Is` mapping.
5. **Output writes stay `os.WriteFile(..., 0o666)`** (Python's truncate-and-write) until Phase 4.

## Files

- `internal/pyfmt/oserror.go` + tests (NEW)
- `cli/args.go` + `args_test.go` (NEW): the parser spec, `parse`, `parsePyInt`, and a 97-case table captured from Python 3.14
- `cli/cli.go`, `cli/help.go` (NEW): `buildParser`, `insertImplicitRotate`, `Main`, `run`,
  `verifyReversible` seam, `defaultRotateOutput`/`defaultReverseOutput` (D1), and help built from upstream's docstring
- `cli/cli_test.go` (NEW): the ported upstream CLI tests, plus `-G` and D1 tests
- `cmd/heic-rotate/main.go`, `cmd/heic-lossless-rotate/main.go`, `cmd/cmd_test.go` (NEW)
- `Makefile`: `run` target
- `difftest/pydriver.py` (`cli` op), `difftest/cli_test.go` (NEW): the gate. It covers the argv table plus about 400
  generated sequences, the operational corpus, and error cases, with a COMPAT allow-list for E1, D1, and D2.
- `.github/workflows/ci.yml`: difftest on Python 3.14
- Docs: COMPAT (D1/E1/OSERR implemented, argparse-version note), DESIGN, PLAN

## Commits

1. feat: pyfmt.OSError
2. feat: argparse-compatible parser
3. feat: cli.Main/run port + ported CLI tests
4. feat: binaries + Makefile run target
5. test: CLI difftest gate + CI Python 3.14
6. docs: Phase 3

## Risks

| Risk | Mitigation |
|---|---|
| argparse behaviors beyond the probed set | Generated argv sweep against the real parser |
| strerror text varies across platforms for rare errnos | Exact for the common ones (which the gate checks); otherwise best effort, under OSERR |
| In-process Python CLI differs from a real process | Real-subprocess sanity cases; `PYTHONIOENCODING=utf-8` |
| No Windows coverage of OSError text | Best effort, documented |

## Dependencies

None (stdlib only).
