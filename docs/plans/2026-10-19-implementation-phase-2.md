# Plan: Phase 2: core.go Port and Python-Formatting Helpers

- **Date:** 2026-10-19
- **Status:** Implemented 2026-10-19. The gate passed: 103,396 cases, zero unexplained
  differences. Deviations from this plan:
  - Commits 3 and 4 were swapped (the errors come before pybytes, which uses them).
  - D7's cap is absolute (2^20 zero-width extents) rather than `len(data)`. The gate
    found a 65535-extent file that Python parses and then rejects with its own typed
    error.
  - D5/D7 refusals are deferred until after Python's own parse would succeed. The
    interim corruption sweep found early refusals displacing Python's
    `iloc parse mismatch`.
- **Covers:** [PLAN.md](../PLAN.md) Phase 2 (Core port), plus the core-only tests from upstream test groups 4 and 5
- **Upstream pin:** heic-lossless-rotate v1.7.1, commit `70d6c73`
- **Branch:** `feat/phase-2-core` (from `dev`)

## Requirements

- [ ] Every function, constant, and error type in `core.py` is ported to `core.go`, in the same order and with the same names (per `docs/UPSTREAM.md`)
- [ ] **Byte-identical to Python** on every input Python accepts: output bytes, verbose log text, `Info` contents, `FormatInfo` text and exit codes, and `BoxParseError`/`ProvenanceError` messages
- [ ] **No panics, hangs, or out-of-memory failures on any input.** Wherever Python crashes or hangs, Go returns a typed error (D2, D4–D7).
- [ ] Ported tests: groups 1, 2, and 6, plus the core-only tests from groups 4 and 5. Group 2's CLI self-check test is deferred to Phase 3.
- [ ] **Gate:** zero unexplained Go-vs-Python differences over the difftest corpus, including the roughly 65k-case corruption sweep

## Resolved Review Points

| # | Question | Decision |
|---|---|---|
| 1 | `internal/pyfmt` package instead of a root `pyfmt.go` | Approved |
| 2 | New divergences D6 and D7, and a narrower D5 | Approved |
| 3 | Move the core-only group 4 and 5 tests into Phase 2 | Approved |
| 4 | Error types for cases where Python crashes: `ProvenanceError` for bad provenance entries, `BoxParseError` otherwise | Approved |

## Pre-Implementation Findings (probed against upstream, 2026-10-19)

- **Truncation never crashes Python.** Every truncated prefix of a synthetic file
  fails cleanly with `BoxParseError`, because a cut-off top-level box is caught early.
- **Single-byte corruption does crash it.** The sweep covered 8 fixtures (layout × irot ×
  original/rotated, all with Exif), every byte, XOR masks {0xFF, 0x01, 0x80}, and
  ops apply/reverse/info/verify: **65,184 calls in 1.9 s**. About 1,400 of them escaped with
  untyped exceptions: `IndexError`, `struct.error`, and `KeyError: b'CCRC'`.
- **A 64-bit size of 0 makes `iter_boxes` loop forever.** This is a box with size field 1 (meaning the real size follows in 8 bytes) where those 8 bytes are 0. The probe confirmed the hang with a timeout.
- **The `[info]` property list repr renders bytes `is\x80\x00` as `'is�\x00'`**, which
  confirms the decode('ascii','replace') + str-repr rules.

## Technical Approach

1. **Emulate Python's byte-access semantics in a handful of helpers** (`pybytes.go`):

   | Python | Behavior past the end of `data` | Go helper |
   |---|---|---|
   | `data[a:b]`, `memoryview[a:b]` | clamps; `a > b` gives empty | `pySlice(data, a, b) []byte` |
   | `int.from_bytes(data[p:p+n])` | silently reads a *shorter* value | `intFromBytes(data, p, n) (uint64, error)` |
   | `data[i]` | `IndexError` | `pyIndex(data, i) (byte, error)`, D2 `BoxParseError` |
   | `struct.unpack_from` | `struct.error` | `unpackU16/U32/U64(data, p)`, D2 |
   | `buf[a:b] = new` | replaces the clamped range; the length may change | `splice(buf, a, b, new) []byte` |

   Parsers read across box boundaries into following bytes exactly as Python does,
   so bounds checks are against `len(data)`, never against the box end.
2. **`internal/pyfmt`** holds `BytesRepr`, `StrRepr`, `Hex08`, `PropList`, `AssocList`,
   `DecodeASCIIReplace`, and `Bool`. `internal/synth` switches over to it too.
3. **`IterBoxes` stays lazy** (`iter.Seq2[BoxHeader, error]`, which yields an error at most once and
   then stops). Python's eager walks stay eager in Go.
4. **Memory: one copy.**
   - `ApplyRotation` allocates its working copy with spare capacity
     (`len + 64 KiB`), so the slow path's resizes are memmoves rather than reallocations.
   - `ReverseRotation` sizes its output exactly once.
   - `VerifyReversible` makes no allocation proportional to the file.
   - **No allocation is ever sized from an untrusted count in the file.**
5. **Difftest error classification.**
   - When Python returns a value, Go must return the exact same value and log.
   - When Python raises `BoxParseError`/`ProvenanceError`, Go must raise the same type with the same message and the same partial log.
   - When Python raises anything else (`IndexError`, `struct.error`, `KeyError`,
     `OverflowError`, or `TimeoutError` from a hang), Go must return some
     `*BoxParseError`/`*ProvenanceError`, attributed to a COMPAT ID.

## New Divergences

| ID | Python | Go |
|---|---|---|
| **D5** (revised) | `iloc` field widths up to 15 bytes are read as arbitrary-precision ints | Accepts every width 0–15. Refuses only a *value* that doesn't fit in 64 bits. |
| **D6** (new) | A box with a 64-bit size of 0 (size field 1, 8-byte size 0) makes `iter_boxes`/`verify_structure` loop forever | `BoxParseError` |
| **D7** (new) | `iloc` with zero-width fields and huge counts can build billions of extents (in effect, hang then OOM) | `BoxParseError` once the parsed extents exceed `len(data)` |

## Files

- `Makefile`: `lint` prints the staticcheck invocation
- `internal/pyfmt/pyfmt.go` + tests (NEW); `internal/synth/synth.go` switches to pyfmt
- `pybytes.go` + tests (NEW)
- `core.go` (NEW, about 1,900 lines), ported section by section in `core.py` order: errors and constants →
  box primitives → pitm/ipma/ipco → iloc → iinf/Exif → provenance and structure location →
  `ApplyRotation` → reverse (`reconstructedChunks` package var as the test seam) →
  `GatherInfo`/`FormatInfo` → `VerifyStructure`
- `core_test.go`, `helpers_test.go`, `realfiles_test.go` (NEW): upstream tests ported with
  the same names and order. Plus Go-only tests: the UUIDv5 derivation, D5/D6/D7 crafted cases, and
  a no-panic truncation/corruption sweep that runs without Python.
- `difftest/pydriver.py`: named inputs, ops (`apply_rotation`, `reverse_rotation`,
  `verify_reversible`, `gather_info`, `format_info`, `verify_structure`,
  `get_current_rotation`, `make_v1_style_record`, `strip_provenance_tag`, `repr`),
  captured stdout, and a 5 s per-request timer
- `difftest/harness.go`: named inputs, `Log`/`JSON` results, `ClassifyErr`
- `difftest/core_test.go` (NEW): **the gate**. Synth matrix × apply turns × sync_exif; reverse/
  verify/info/format/verify_structure on every output; chains; v1 and stripped-TVER records;
  tamper cases; large and mid-stream Exif files; crafted structures; the corruption sweep.
- `docs/COMPAT.md`, `docs/SPEC.md`, `docs/DESIGN.md`, `docs/UPSTREAM.md`: D5–D7, pyfmt/pybytes,
  final signatures

## Upstream Test Mapping (Phase 2)

| Upstream | Go |
|---|---|
| `TestIlocPatchingStraddlingExtent`, `TestIlocByteFidelity`, `TestDuplicateBoxDetection` | same names |
| `TestSyntheticRoundTrips` | same name |
| `TestVerifyReversibleMatchesReverseRotation` | same name; tracemalloc becomes a `MemStats.TotalAlloc` bound |
| `TestSharedReconstructionCode` | static check becomes a check that both functions use `reconstructedChunks`; the chunk-sequence spy test ports directly |
| `TestReturnTypeAvoidsExtraCopy` | `TestApplyRotationCopyBudget` |
| `TestProvenanceVersionAndSelfCheck` (3 of 4) | same name; the CLI method is deferred to Phase 3 |
| `TestProvenanceLeanFormat` | same name |
| `TestRealFiles` | same name |
| `TestMdatContentIndependence`, `TestLargeMdatFixtures`, `TestMdatMidStreamExifPatch`, `TestMultipleTopLevelMdatBoxes` | same names |

## Commits

1. build: show staticcheck invocation in make lint
2. feat: internal/pyfmt (and switch synth to it)
3. feat: pybytes helpers
4. feat: errors, constants, ProvenanceUUID, VersionString
5. feat: box primitives
6. feat: pitm/ipma/ipco
7. feat: iloc parse/build/patch
8. feat: iinf + Exif orientation lookup
9. feat: provenance box + structure location
10. feat: ApplyRotation
11. feat: ReverseRotation + VerifyReversible
12. feat: GatherInfo, FormatInfo, VerifyStructure
13. test: port upstream core tests (groups 1, 2, 4, 5, 6)
14. test: no-panic sweep (bounds-check audit)
15. test: difftest driver ops + core corpus (gate)
16. docs: COMPAT D5–D7, design/upstream updates, tick Phase 2

Every commit passes `make fmt lint test`.

## Risks

| Risk | Mitigation |
|---|---|
| Branches the corpus never reaches | Run the gate with coverage and inspect every uncovered `core.go` block before declaring the gate passed |
| D2 masking a real logic bug | D2 applies only when Python *crashes*. Every value and typed-error case must match exactly. |
| Memory-budget tests are noisy | Measure `TotalAlloc` after `runtime.GC()`, and skip under `-race` |
| A large single file is hard to review | 16 commits in `core.py` order |

## Dependencies

None: stdlib only (`iter`, `math/bits`, `math/big` on error paths only, `hash/crc32`,
and `crypto/sha1` in tests).
