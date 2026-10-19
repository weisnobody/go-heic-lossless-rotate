# Plan: Phase 0–1: Scaffolding, synth_heic Port, and Python Differential Harness

- **Date:** 2026-10-19
- **Status:** Approved
- **Covers:** [PLAN.md](../PLAN.md) Phase 0 (Scaffolding) and Phase 1 (Test foundation)
- **Upstream pin:** heic-lossless-rotate v1.7.1, commit `70d6c73b5827aabf958c0649b3ac1c9e0dd33d05`

## Requirements

- [ ] The module builds and tests on Go 1.23+ with **zero dependencies**, using the Makefile/`./bin/` convention
- [ ] `version.go` holds `Version = "1.7.1"`, `GoRevision = 1`, `GoVersion`, and `GoVersionString()`
- [ ] `docs/UPSTREAM.md` and `docs/COMPAT.md` exist. The pinned upstream commit lives in **one place**.
- [ ] `internal/synth`: a test-only port of `tests/synth_heic.py`
- [ ] `difftest/` harness that finds the pinned upstream checkout, runs its CLI, and drives its Python functions directly
- [ ] **Gate:** Go `synth` output is byte-identical to Python's `synth_heic.py` for every parameter combination
- [ ] GitHub Actions on the mirror: unit tests on Linux/macOS/Windows and a difftest job on Linux

## Resolved Review Points

| # | Question | Decision |
|---|---|---|
| 1 | Add the upstream copyright line to LICENSE? | **Yes** |
| 2 | Fail hard (not skip) when `-tags difftest` is set but Python or the upstream checkout is missing? | **Yes** |
| 3 | Who creates the GitHub repo and the arena → GitHub push mirror? | **User does it.** Not part of this implementation. |

## Pre-Implementation Verification (done 2026-10-19)

- The pinned upstream passes its own suite on local Python 3.14.6: `Ran 68 tests … OK (skipped=2)`, where the skips are the real-file tests with an empty `testdata/`. It takes about 2.2 s.
- `PYTHONPATH=<upstream>/src python3 -m heic_rotate -V` prints
  `heic-lossless-rotate 1.7.1` and exits 0. This is the invocation the harness uses.
- Go toolchain available: go1.26.5 darwin/arm64.

## Technical Approach

1. **The pinned commit lives in a single file, `difftest/upstream.commit`.** The Makefile
   (`$(shell cat …)`), CI, and the Go harness all read it.
   *Why:* three copies of a SHA will eventually drift apart.
2. **The upstream checkout is found through `HEIC_ROTATE_PY_SRC`**, which defaults to `.upstream/`
   (gitignored). `make upstream` clones it at the pinned commit. The harness
   verifies `git rev-parse HEAD` == `upstream.commit` and upstream's
   `_version.py` `VERSION` == `heicrotate.Version`. A mismatch fails unless
   `HEIC_ROTATE_PY_ALLOW_UNPINNED=1`.
   *Why:* comparing against the wrong upstream version would make the differential tests meaningless.
3. **Batch Python driver.** `difftest/pydriver.py` takes a JSON list of
   requests and writes each result to a file in a temp dir, so one Python process serves a
   whole test. Phase 1 has only the `synth` op. Phase 2 adds `apply_rotation`,
   `reverse_rotation`, and so on.
   *Why:* about 200 synth combinations shouldn't cost about 200 interpreter startups.
4. **Hermetic Python environment:**
   - `PYTHONPATH=<src>:<tests>`, so the driver imports both `heic_rotate` and `synth_heic`
   - `PYTHONDONTWRITEBYTECODE=1`, so nothing is written into the checkout
   - `PYTHONIOENCODING=utf-8` (Phase 2's U+FFFD output depends on it)
   - `PYTHONHASHSEED=0`
   - `-P` (Python ≥ 3.11), so the script's directory isn't prepended to `sys.path`
5. **Differential tests are opt-in via build tag** (`//go:build difftest`), so plain
   `go test ./...` never needs Python. Once opted in, a missing Python or checkout
   is a **hard failure** (review point 2).
6. **The synth port mirrors `synth_heic.py` function by function** (`box`, `fullbox`,
   `BuildSyntheticHEIC`, plus `buildMeta` for the nested `build_meta`). Options are a struct, and
   `DefaultOptions()` returns Python's defaults, because zero values can't express them
   (payload `FAKE_PIXEL_DATA`×20, `ExifOrientation` 1).

## Files

### `go.mod` (NEW)
`module github.com/pjg/go-heic-lossless-rotate`, `go 1.23`, no `require`.

### `version.go` + `version_test.go` (NEW)
```go
package heicrotate

// Version is the upstream heic-lossless-rotate version this port implements
// (← _version.py). Written into TVER, shown by -V and info.
const Version = "1.7.1"

// GoRevision counts Go-port releases for this Version; reset to 1 on each
// upstream bump. See docs/DESIGN.md § Go port version and release tags.
const GoRevision = 1

var GoVersion = Version + "+go." + strconv.Itoa(GoRevision)

func GoVersionString() string // 3 lines, per DESIGN.md
```
Tests:
- `GoVersion` matches `^\d+\.\d+\.\d+\+go\.[1-9]\d*$`.
- `GoVersionString()` has the exact 3-line shape, with `runtime.Version()` on the last line.

`VersionString()` is deferred to Phase 2 because it needs `FormatVersion`.

### `doc.go` (NEW)
The `package heicrotate` doc comment: what the package is, a link to upstream with credit to
its author, and a pointer to `docs/`.

### `internal/synth/synth.go` + `synth_test.go` (NEW)
Port of `tests/synth_heic.py`.
- `type Layout string`: `MdatFirst = "mdat_first"`, `MetaFirst = "meta_first"`
  (the same strings as Python, so they pass straight to the driver)
- `type ExifPosition string`: `ExifEnd = "end"`, `ExifMiddle = "middle"`
- `type Options struct{ Layout; MdatPayload []byte; IncludeIrot bool; IrotValue int; IncludeExif bool; ExifOrientation uint16; ExifPosition; ExtraMdat bool }`
- `DefaultOptions() Options`
- `BuildSyntheticHEIC(o Options) ([]byte, error)`. An unknown layout or position returns an error (like Python's `ValueError`), and so does a broken meta-size invariant (Python uses `assert`).
- Unexported `box()`, `fullbox()`, `buildMeta()`

Standalone tests (no Python):
- the top-level box order for each layout, with and without `ExtraMdat`
- the `iloc` extent offsets for the main item and the Exif item point at the actual payload and Exif blob, for both `end` and `middle`. This guards against the bug upstream's README says once made Exif sync unreachable in every synthetic test.
- with `middle`, the Exif blob has bytes on both sides
- invalid options return errors

### `difftest/upstream.commit` (NEW)
`70d6c73b5827aabf958c0649b3ac1c9e0dd33d05`

### `difftest/harness.go` (NEW, `//go:build difftest`)
- `Upstream(t) *Py` resolves the env vars, verifies the commit and version (approach step 2), and is cached per test binary
- `(*Py).CLI(t, dir string, args ...string) Result{Code int; Stdout, Stderr []byte}`
- `(*Py).Drive(t, reqs []Request) []Response` runs a single driver process per batch
- `DiffBytes(t, name string, want, got []byte)` reports the first differing offset, both lengths, and ±16 bytes of hex context on each side

### `difftest/pydriver.py` (NEW)
Stdlib-only. Arguments: `argv[1]` is the request JSON file and `argv[2]` is the output dir. Each request writes
`<id>.bin` on success or `<id>.err` (`type(e).__name__: msg`) on failure. Phase 1 has only the op
`synth`: its parameters go straight through as kwargs, with `mdat_payload` base64-encoded in the JSON.

### `difftest/harness_test.go` + `difftest/synth_test.go` (NEW, `//go:build difftest`)
- `TestUpstreamPinned` checks that the commit and version match.
- `TestPythonCLISmoke` checks that Python `-V` exits 0 and its first line is `heic-lossless-rotate ` + `Version`.
- `TestSynthMatchesPython` is **the gate**:
  - 180 combinations in one batch: layout{2} × irot{off, 0..3} × exif{off, orientation{1,3,6,8} × position{end, middle}} × extra_mdat{2}
  - plus payload variants: empty, 1 byte, all 256 byte values, 4 MiB
  - plus error cases: an invalid layout or exif_position must fail in both implementations

### `Makefile` (NEW)
```make
UPSTREAM_COMMIT := $(shell cat difftest/upstream.commit)
UPSTREAM_DIR    ?= .upstream
build:    ; go build -o bin/ ./...    # cmd/* binaries land here from Phase 3
test:     ; go test -race ./...
difftest: upstream ; HEIC_ROTATE_PY_SRC=$(abspath $(UPSTREAM_DIR)) go test -tags difftest ./difftest/...
upstream: # clone if missing, then fetch and check out $(UPSTREAM_COMMIT) (detached)
fmt / lint (go vet; staticcheck if installed) / clean (rm -rf bin dist)
```
The `run` target is deferred to Phase 3. There's nothing runnable before then, and an empty target would be misleading.

### `.gitignore` (APPEND)
Add `/bin/`, `/dist/`, `/rotated/`, `/.upstream/`, `/testdata/*`, and `!/testdata/.keep`, plus a new
`testdata/.keep`.

### `docs/UPSTREAM.md` (NEW)
- the pinned commit (referencing `difftest/upstream.commit`) and the upstream version
- how to bump the pin
- the Python→Go function map, moved here from DESIGN.md (DESIGN.md links to it)
- a porting log table: upstream version, upstream commit, Go version, date, notes

### `docs/COMPAT.md` (NEW)
D1–D5 and E1, each with Python behavior, Go behavior, an example, and an allow-list ID for the
Phase 3 harness to match on.

### `.github/workflows/ci.yml` (NEW)
- `test`: {ubuntu, macos, windows} × Go {1.23, stable}, running `go vet`,
  `staticcheck`, and `go test -race ./...`
- `difftest`: ubuntu with Python 3.12. It reads `upstream.commit`, uses `actions/checkout`
  with `repository: olegoz/heic-lossless-rotate` and `ref: <sha>` to check out into
  `.upstream`, then runs `make difftest`.

### `README.md` (MODIFY)
Expand the stub: status (pre-release), dev setup (`make test`, `make difftest`), and
links to docs. Install and usage instructions wait for Phase 6.

### `LICENSE` (MODIFY, approved)
Add `Portions Copyright (c) 2026 Oleg Ostrozhansky (heic-lossless-rotate)`
above the existing copyright line. Upstream is MIT, and ported code (including
`synth_heic.py`) carries its notice.

## Dependencies

None. Go stdlib only. `python3` ≥ 3.11 (for `-P`) is needed for difftest only, and CI pins 3.12.

## Testing Strategy

- `go test ./...`: version tests and the standalone synth tests. No Python needed, so they run everywhere, including Windows.
- `make difftest`: the pin and CLI smoke tests, plus the synth gate of about 190 byte-exact comparisons run in a single Python process.
- **Harness self-check:** temporarily break one byte in Go synth, confirm `DiffBytes`
  reports the right offset, then revert it. This follows upstream's own lesson behind its Exif-sync direction test:
  a check that can't fail proves nothing.

## Alternatives Considered

- **Committed golden fixture files** instead of live Python. This option wasn't selected in discovery, and the fixtures would go stale whenever the pin moves.
- **Env-var gating instead of a build tag.** The build tag keeps `os/exec` and the Python plumbing
  out of normal test binaries entirely.
- **A `git submodule` for upstream.** It would couple every clone to an external repo. A Makefile clone plus
  the pin file is lighter and just as reproducible in CI.

## Risks

| Risk | Mitigation |
|---|---|
| The GitHub mirror doesn't exist yet, and CI only runs there | The user is setting up the repo and the arena push mirror (review point 3). Until then everything runs locally via `make`. |
| Python version differences (local 3.14 vs CI 3.12) | Upstream passes its own suite on 3.14, and its code is plain struct/bytes. The harness reports `python3 --version` on failure. |
| Line endings or encoding on Windows | The difftest job is Linux-only. Unit tests compare bytes only. |

## Complexity

**Medium-low.** About 150 lines of synth port, about 250 lines of harness and driver, plus config. The only
real logic is the synth port, and the gate checks it exhaustively.

## Out of Scope (later phases)

- `core.go` port, `pyfmt.go`, `VersionString()` (Phase 2)
- CLI, `-G`, `cmd/` binaries, Makefile `run` target, COMPAT allow-list matching (Phase 3)
- GitHub repo creation and push mirror configuration (user)
