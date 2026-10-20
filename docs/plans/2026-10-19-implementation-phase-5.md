# Plan: Phase 5: Streaming / Low-Memory Engine

- **Date:** 2026-10-19
- **Status:** Implemented 2026-10-19 (Go/No-Go: **Go**). Deviations from this plan:
  - Instead of mirroring `ApplyRotation`'s orchestration in `stream.go`, `core.go`'s orchestration was
    routed through a six-operation `fileEnv` (review point 2's "small extractions", taken further), so
    both engines run one orchestration and can't drift. The core gate was unchanged by it.
  - The Exif blob cap is 1 MiB, not 64 KiB, because Exif items with thumbnails can exceed 64 KiB.
  - A boundary bug (an Exif blob ending exactly at `meta_start` counted as touching the span) made
    `mdat_first` files with Exif fall back. The smoke test found it, and it's fixed.
  - The cross-check counts only fallbacks on operations that *succeed*. Erroring operations fall back by
    design, to give the exact error.
- **Covers:** [PLAN.md](../PLAN.md) Phase 5 (Streaming engine)
- **Upstream pin:** heic-lossless-rotate v1.7.1, commit `70d6c73`
- **Branch:** `feat/phase-5-streaming` (from `dev`)

## Requirements

- [ ] Exported `ApplyRotationReaderAt`, `ReverseRotationReaderAt`, `VerifyReversibleReaderAt`, and `GatherInfoReaderAt`, plus `View` (`Size`, `ReadAt`, `WriteTo`, `Bytes`)
- [ ] For every input, the results are identical to the `[]byte` engine: output bytes, verbose log, `Info`, and error type and message
- [ ] For well-formed files, memory is O(meta). Rotating and reversing a 64 MiB file grows the heap by less than 2 MiB.
- [ ] The file API (and therefore the CLI) runs on the streaming engine, and both Python gates stay green
- [ ] `core.go` stays a 1:1 port of `core.py`; the only changes are small unexported helper extractions

## Resolved Review Points

| # | Question | Decision |
|---|---|---|
| 1 | Fallback model | Stream well-formed files. Anything unusual or erroneous goes through the `[]byte` engine, which keeps results exact at the current memory cost. |
| 2 | Changes to `core.go` | Small unexported extractions only, with no logic change. The core gate must stay unchanged. |
| 3 | Same file as input and output, with a hard-linked target | Materialize the output first |
| 4 | ReaderAt API | Exported, as proposed |

## Pre-Implementation Findings

1. **The core parsers read past box ends, just as upstream's do.** In a buffer that holds only `meta` and the provenance box, such a read
   hits the end of the buffer, where the full file would have had real `mdat` bytes. On malformed input that silently changes results.
2. **On well-formed files, the only bytes outside `[meta_start, prov_end)` the core reads are** the top-level headers,
   the Exif blob, and the whole-file CRCs. Every write falls inside that span, plus the 2-byte Exif patch.
3. **The Phase 4 hard-link in-place write could overwrite bytes the engine is still reading** when the input and the output
   are the same file.

## Technical Approach

1. **Stream the well-formed case and fall back for everything else.**
   - Prechecks, all of which must pass to stream:
     - the top-level walk via `ReadAt` is clean
     - `meta` has a 32-bit header
     - the `meta`, `iprp`, and `ipco` children tile their containers exactly
     - `pitm`, `infe`, and `irot` are big enough for the core's fixed reads
     - any provenance box comes immediately after `meta`
   - Any precheck failure, or any error at all, falls back: read the whole input and run the `[]byte` engine.
   - Two errors are returned directly instead, because the cross-check proves them exact: "no provenance record" (reverse) and overwrite refusal.
2. **Rotate flow:**
   - Walk the top-level boxes and load the span.
   - Run the core's parsers and builders on the span, following `ApplyRotation`'s orchestration step by step:
     - iloc thresholds and log offsets are translated by `meta_start`
     - the Exif blob is read via `ReadAt`, capped at 64 KiB; if the IFD needs more, fall back
     - CRCs are streamed
   - Build the output as a `View` of segments.
   - Reverse reuses `restoreMetaProvenanceSpan`, and its chunking reuses the segment logic of `iterReconstructedChunks`.
3. **Unexported extractions in `core.go`**, such as `exifFieldInBlob` and the shared pieces of `ApplyRotation`. Public bodies keep
   Python's order.
4. **The file API uses the streaming engine:**
   - It opens the input as an `*os.File` and runs the self-check on the `View`.
   - It streams the `View` into the atomic temp file.
   - If the input and output are the same file and the hard-link in-place path applies, it materializes the output first.
5. **Go/No-Go:** if the cross-check or the heap bound fails, the file API stays on the `[]byte` engine.

## API

```go
type View struct{ /* segments */ }
func (v *View) Size() int64
func (v *View) ReadAt(p []byte, off int64) (int, error)
func (v *View) WriteTo(w io.Writer) (int64, error)
func (v *View) Bytes() ([]byte, error)

func ApplyRotationReaderAt(r io.ReaderAt, size int64, deltaTurns int, opts RotateOptions) (*View, error)
func ReverseRotationReaderAt(r io.ReaderAt, size int64, opts ReverseOptions) (*View, error)
func VerifyReversibleReaderAt(r io.ReaderAt, size int64, opts ReverseOptions) error
func GatherInfoReaderAt(r io.ReaderAt, size int64) (*Info, error)
```

## Files

- `stream.go` (NEW): `View`, prechecks, span loading, orchestration, streaming CRC, fallback
- `core.go`: unexported extractions only
- `file.go`: input through the ReaderAt functions; `writeOutput` streams a `View`; same-file hard-link materialization
- `internal/craft` (NEW, moved from `difftest/craft.go`)
- `stream_test.go` (NEW):
  - cross-check of streaming against `[]byte` across the whole corpus, including the corruption sweep
  - fallback accounting
  - 64 MiB heap bound
  - same-file and hard-link cases
- Docs: DESIGN, SPEC, PLAN, README, UPSTREAM (the bump steps now include updating `stream.go`)

## Commits

1. refactor: core helper extractions (core gate unchanged)
2. test: move craft to internal/craft
3. feat: streaming engine + cross-check
4. feat: file API on the streaming engine + heap bound (Go/No-Go)
5. docs: Phase 5

## Risks

| Risk | Mitigation |
|---|---|
| Orchestration drift from `ApplyRotation` | Cross-check, both Python gates through the stream engine, extracted shared helpers, UPSTREAM bump step |
| Prechecks too strict (real files silently fall back) | Fallback accounting must show zero fallbacks on well-formed fixtures |
| 4–5 read passes per rotate | Page cache; measure a 64 MiB rotate |
| Same-file input/output | Temp + rename never overwrites the input mid-read; the hard-link path materializes first |

## Dependencies

None.
