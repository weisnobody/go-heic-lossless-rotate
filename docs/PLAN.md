# go-heic-lossless-rotate Implementation Plan

## MVP Scope

See [SPEC.md § MVP Scope](SPEC.md#mvp-scope): the 1:1 core, the CLI, both binaries,
the file API, the streaming engine, test parity plus difftest, and goreleaser. The
architecture is in [DESIGN.md](DESIGN.md).

Ordering principle: **correctness against Python comes first, before any
Go-only layers.** The difftest harness is built in Phase 1, so every later phase is
measured against the real upstream tool from day one.

## Implementation Order

### Phase 0: Scaffolding
- [x] `go mod init github.com/pjg/go-heic-lossless-rotate` (`go 1.23`), with `Makefile`
      targets `test`, `difftest`, `lint`, `build`
- [x] `version.go` (`Version = "1.7.1"`, `GoRevision = 1`, `GoVersion`,
      `GoVersionString()`), `doc.go`
- [ ] Create the GitHub repo and configure the arena → GitHub push mirror (branches + tags)
      *(owner: user; not done in the Phase 0–1 implementation)*
- [x] `docs/UPSTREAM.md`: pinned commit `70d6c73`, the Python↔Go function map
      (from DESIGN.md), and a porting log
- [x] `docs/COMPAT.md` skeleton with D1–D5
- [x] `.gitignore`: `testdata/*`, `!testdata/.keep`, `rotated/`, `bin/`, `dist/`
- [x] CI (GitHub Actions on the mirror): `go vet`, `staticcheck`,
      `go test -race ./...` on linux/macos/windows

### Phase 1: Test foundation
- [x] `internal/synth`: port of `synth_heic.py` (`BuildSyntheticHEIC(Options)`
      with layout, include_irot, irot_value, include_exif, exif_orientation,
      exif_position, extra_mdat)
- [x] `difftest/` harness: locate `$HEIC_ROTATE_PY_SRC`, run the Python CLI and
      generator, compare bytes, stdout, stderr, and exit codes. (The COMPAT
      allow-list moved to Phase 3, where the first divergences appear.)
- [x] **Gate:** the Go synth output is byte-identical to Python's `synth_heic.py` for
      every parameter combination

### Phase 2: Core port (`core.go`), in `core.py` order
- [x] Errors, constants, `ProvenanceUUID` (plus the UUIDv5 derivation test),
      `versionTuple`, `VersionString`
- [x] `pyfmt.go` helpers, unit-tested against literal Python strings
- [x] Box primitives: `ReadBoxHeader`, `IterBoxes` (lazy), `FindChild`,
      `FindUniqueChild`
- [x] `ParsePitm`, `ParseIpma`/`BuildIpma`, `ParseIpcoChildren`
- [x] `ParseIloc`/`BuildIlocContent`/`PatchIlocOffsetsAtThreshold`
- [x] `ParseIinfItems`, Exif orientation maps, `FindExifItemLocation`,
      `FindExifOrientationField`
- [x] Provenance: `Crc32ExcludingRange`, `FindProvenanceBox`,
      `ParseProvenance`(+`Lenient`), `BuildProvenanceBox`, `LocateStructures`,
      the rotation helpers
- [x] `ApplyRotation` (fast path, slow path, Exif sync, provenance
      create/update/v1→v2 migrate/resize)
- [x] `restoreMetaProvenanceSpan`, `prepareReversal`,
      `iterReconstructedChunks`, `ReverseRotation`, `VerifyReversible`
- [x] `GatherInfo`, `FormatInfo`, `VerifyStructure`
- [x] Bounds-check audit for D2/D4/D5: every Python crash path returns a typed error
- [x] Port the test groups 1, 2, and 6 listed below (plus the core-only
      group 4 and 5 tests; group 2's CLI self-check method moves to Phase 3)
- [x] **Gate:** the library-level difftest (Go `ApplyRotation`/`ReverseRotation` vs
      Python `apply_rotation`/`reverse_rotation`, via a small Python driver)
      shows zero diffs, including verbose log text
- [x] Gate result (2026-10-19): 103,396 cases over 18,043 inputs, zero
      unexplained differences (allowed: D2 x2037, D5 x16, D6 x15); 794
      formatting cases vs the live interpreter. See
      [plans/2026-10-19-implementation-phase-2.md](plans/2026-10-19-implementation-phase-2.md).

### Phase 3: CLI port
- [ ] `cli/args.go`: the argparse-compatible parser (DESIGN.md § argparse-compatible
      CLI parser)
- [ ] `cli/cli.go`: `buildParser`, `insertImplicitRotate`, `Main`, `run`.
      Messages and exit codes are 1:1 with `cli.py`. Default output naming uses
      the D1 rule.
- [ ] `cmd/heic-rotate`, `cmd/heic-lossless-rotate`
- [ ] `-G`/`--goversion` (E1). Tests: output format, exit 0, top-level only
      (`rotate -G` is a usage error), and `-V` output unchanged
- [ ] Port upstream's `test_cli_self_check_aborts_and_writes_nothing_on_failure`
      (deferred from Phase 2)
- [ ] Port `TestCLI` plus group 5's CLI tests (in-process via `cli.Main`, plus a
      few subprocess tests against the built binaries)
- [ ] Difftest COMPAT allow-list (match cases by COMPAT.md ID)
- [ ] Difftest: the argv edge-case table vs argparse (exit codes), and the full CLI
      corpus (bytes, stdout, stderr, exit codes)
- [ ] **Gate:** zero unexplained CLI diffs, and every allowed diff is listed in COMPAT.md

### Phase 4: File API (`file.go`)
- [ ] `RotateFile(in, out string, deltaTurns int, opts FileOptions) (Result, error)`,
      `ReverseFile(...)`, `InfoFile(path) (*Info, error)`, with
      `FileOptions{Force, DryRun, NoExifSync, IgnoreTamper bool; Log io.Writer}`.
      `Result` reports the output path, size, and delta vs input (for the
      `[ok] wrote …` and `[dry-run] …` lines).
- [ ] `DefaultRotateOutput(in)` / `DefaultReverseOutput(in)` (D1), exported
- [ ] Atomic temp-and-rename write (D3), overwrite refusal, mode preservation
- [ ] Refactor `cli.run` onto the file API. The difftest must stay green.
- [ ] Port `TestSameFileInputOutput` and `TestDryRunSizeReport` at the file-API level

### Phase 5: Streaming engine (`stream.go`)
- [ ] Spliced view (`io.ReaderAt` over input ranges plus literal segments) and
      a streaming CRC32 over views
- [ ] `ApplyRotationReaderAt(r io.ReaderAt, size int64, deltaTurns int, opts)
      (*View, error)`, `ReverseRotationReaderAt(...)`, `VerifyReversibleReaderAt`,
      `GatherInfoReaderAt`. `View` exposes `Size()`, `ReadAt`, and `WriteTo(io.Writer)`.
- [ ] Cross-check test: the stream engine equals the `[]byte` engine over the whole
      corpus (all synth variants, multi-edit chains, v1 records, tamper
      cases, mid-stream Exif, extra mdat, testdata/)
- [ ] Heap-bound test: rotating and reversing a 64 MiB synthetic stays under
      2 MiB of heap growth
- [ ] **Go/No-Go gate:** the cross-check is green and the heap bound is met, so
      `file.go` (and therefore the CLI) switches to the streaming engine. If not, ship
      with the `[]byte` engine behind the file API and keep stream.go
      unexported until it passes.

### Phase 6: Release
- [ ] Verify the mirror delivers tags: push a throwaway tag to arena, confirm it appears
      on GitHub and triggers the workflow, then delete it from both
- [ ] `.goreleaser.yaml`: two builds × {darwin, linux, windows} × {amd64, arm64},
      `CGO_ENABLED=0`, checksums, archives. Confirm goreleaser accepts a
      `+go.N` tag (do a dry run with `--snapshot` against a local `v1.7.1+go.2` tag).
- [ ] Release workflow: tag-vs-`GoVersion` consistency check (DESIGN.md § Go port
      version and release tags)
- [ ] README: install (`go install`, release download), usage (point to
      upstream for semantics), library examples, compat notes
- [ ] Package docs with `Example*` tests for `ApplyRotation`,
      `RotateFile`, and the streaming API
- [ ] README: document `-G`, the `+go.N` scheme, and that Go-only fixes need
      `go install …@vX.Y.Z+go.N`, because `@latest` ignores metadata tags
- [ ] Tag `v1.7.1` on arena (= GoVersion `1.7.1+go.1`) and confirm the GitHub
      release and `go install …@latest` work

## Upstream Test → Go Test Mapping

| Group | Upstream class | Go test | Notes |
|---|---|---|---|
| 1 | `TestIlocPatchingStraddlingExtent` | `TestIlocPatchingStraddlingExtent` | includes the "old buggy algorithm fails" replay |
| 1 | `TestIlocByteFidelity` | `TestIlocByteFidelity` | |
| 1 | `TestDuplicateBoxDetection` | `TestDuplicateBoxDetection` | |
| 2 | `TestSyntheticRoundTrips` | `TestSyntheticRoundTrips` (table of 8 combos + subtests) | |
| 2 | `TestProvenanceVersionAndSelfCheck` | same | the self-check failure uses a test hook in place of `mock.patch` |
| 2 | `TestProvenanceLeanFormat` | same | port the `_make_v1_style_record` helper |
| 3 | `TestCLI` | `cli.TestCLI` | in-process `Main`, plus subprocess smoke tests |
| 4 | `TestRealFiles` | `TestRealFiles` | `t.Skip` when testdata is empty, and writes `rotated/` |
| 5 | `TestMdatContentIndependence` | same | |
| 5 | `TestLargeMdatFixtures` | same | also run against the stream engine |
| 5 | `TestMdatMidStreamExifPatch` | same | |
| 5 | `TestMultipleTopLevelMdatBoxes` | same | |
| 5 | `TestSameFileInputOutput` | same (file API + CLI) | |
| 5 | `TestDryRunSizeReport` | same | |
| 6 | `TestVerifyReversibleMatchesReverseRotation` | same | tracemalloc test becomes a MemStats bound |
| 6 | `TestSharedReconstructionCode` | same intent | bytecode check becomes a chunk-sequence hook check |
| 6 | `TestReturnTypeAvoidsExtraCopy` | `TestApplyRotationSingleAllocation` | Go-equivalent intent |

## Go/No-Go Criteria

### Continue if:
- [ ] The Phase 1 gate passes: the synth port is byte-identical, so the test foundation is trustworthy
- [ ] The Phase 2 gate passes: the library is byte-identical to Python, verbose text included
- [ ] The Phase 3 gate passes: every CLI diff vs Python is explained in COMPAT.md

### Pivot if:
- [ ] Byte-identical output turns out to be infeasible for some class of input (unexpected,
      since all the logic is deterministic integer and byte work). Then relax C1 to
      "format-compatible + cross-reversible" for that class only, and
      document it.
- [ ] The streaming engine can't match the `[]byte` engine without duplicating
      core logic wholesale. Then ship the MVP on the `[]byte` engine and revisit when
      upstream lands its own streaming refactor, aligning with its structure.

## Resolved Decisions
1. **Versioning (2026-10-19).** Match the Python version and track Go releases with
   build metadata: `1.7.1+go.N`, shown by the new `-G`/`--goversion`. Tags are
   `vX.Y.Z` for `+go.1` and `vX.Y.Z+go.N` after that. See DESIGN.md.
2. **Hosting (2026-10-19).** arena.the42.net is primary and GitHub is the push mirror.

## Post-MVP Phases
- Go native fuzzing of the parsers and the rotate→reverse round trip
- Port upstream releases after 1.7.1 as they land (follow UPSTREAM.md's porting log)
- Homebrew tap / Scoop manifest
- Optional Go-only extensions (JSON `info`, batch mode), clearly flagged as
  extensions in `--help` and COMPAT.md
