# Upstream Tracking

This Go module ports [heic-lossless-rotate](https://github.com/olegoz/heic-lossless-rotate)
by Oleg Ostrozhansky (MIT). The Go port mirrors one pinned upstream release.

## Pinned Upstream

| | |
|---|---|
| Upstream version | **1.7.1** (`version.Version`) |
| Upstream commit | see [`difftest/upstream.commit`](../difftest/upstream.commit), currently `70d6c73b5827aabf958c0649b3ac1c9e0dd33d05` (2026-10-08) |
| Go port version | `version.GoVersion`, currently `1.7.1+go.1` |

`difftest/upstream.commit` is the **single source of truth** for the pin.
The Makefile (`make upstream`), CI, and the differential harness all read
it. The harness refuses to run against a checkout at any other commit,
or whose `_version.py` doesn't match `version.Version`. Set
`HEIC_ROTATE_PY_ALLOW_UNPINNED=1` to override this for local experiments.

## Bumping the Pin (porting a new upstream release)

1. Read upstream's CHANGELOG and diff `core.py`/`cli.py`/tests between the old
   and new commits.
2. Write the new commit SHA to `difftest/upstream.commit` and run `make upstream`.
3. Port the changes function by function, keeping the map below current.
   Changes to `apply_rotation`/`_prepare_reversal` go into the shared orchestration in `pkg/core/core.go`.
   If a change reads bytes outside the meta+provenance span in a new way, route it through `FileEnv`
   and implement it in both `memEnv` and `pkg/stream`'s `streamEnv`. If it reads new boxes at fixed
   offsets, extend `fixedReadsFit`. `pkg/stream/stream_test.go`'s cross-check catches what's missed.
   If `cli.py`'s docstring changed, regenerate the help text:
   `python3 -I tools/gen_docstring.py .upstream/src/heic_rotate/cli.py > internal/cli/docstring.go`.
   If its parser changed, re-capture `internal/cli/testdata/argparse_py314.json` from the real parser.
4. Set `Version` in `pkg/version/version.go` to the new upstream version and reset
   `GoRevision` to 1.
5. `make test difftest` must be green. Any new divergence goes in
   [COMPAT.md](COMPAT.md).
6. Add a row to the porting log below.

For a Go-only fix release, bump `GoRevision` and nothing else (see
[DESIGN.md § Go port version and release tags](DESIGN.md#go-port-version-and-release-tags)).

## Porting Log

| Upstream version | Upstream commit | Go version | Date | Notes |
|---|---|---|---|---|
| 1.7.1 | `70d6c73` | 1.7.1+go.1 | in progress | Initial port |

## Python → Go Function Map

Naming rules are in [DESIGN.md § Naming Rules](DESIGN.md#naming-rules-python--go).

### Function map: `core.py` → `pkg/core/core.go`

Names below are in package `core` unless marked.

| Python | Go |
|---|---|
| `class BoxParseError` / `class ProvenanceError` | `type BoxParseError struct{Msg string; Err error}` / `type ProvenanceError …` (both `error`, both `Unwrap`) |
| `FORMAT_VERSION` | `const FormatVersion = 2` |
| `SUPPORTED_REVERSE_FORMAT_VERSIONS` | `var SupportedReverseFormatVersions = []int{1, 2}` |
| `_version_tuple()` | `versionTuple() ([3]uint8, error)` |
| `PROVENANCE_UUID` | `var ProvenanceUUID = [16]byte{0xe8,0x50,0x77,0x7c,…}` (`e850777c-3416-50a4-9fa8-6493cfc13f31`). Hardcoded, with a test re-deriving it as UUIDv5(NAMESPACE_URL, `urn:heic_rotate.py:provenance:v1`). |
| `read_box_header(data, pos)` | `ReadBoxHeader(data []byte, pos int) (BoxHeader, bool, error)` (`ok=false` is Python's `None` at EOF) |
| `iter_boxes(data, start, end)` | `IterBoxes(data []byte, start, end int) iter.Seq2[BoxHeader, error]`. Stays **lazy**: Python's `find_child` stops before reaching a malformed later box, and that acceptance behavior must match. |
| `find_child` | `FindChild(data []byte, start, end int, boxtype string) (BoxHeader, bool, error)` |
| `find_unique_child` | `FindUniqueChild(data []byte, start, end int, boxtype, context string) (BoxHeader, bool, error)` |
| `parse_pitm` | `ParsePitm(data, cStart, cEnd int) (uint32, error)` |
| `parse_ipma` / `build_ipma` | `ParseIpma(...) (version uint8, flags uint32, entries []IpmaEntry, err error)` / `BuildIpma(version, flags, entries) ([]byte, error)` |
| `parse_ipco_children` | `ParseIpcoChildren(...) ([]BoxHeader, error)` |
| `parse_iloc` / `build_iloc_content` | `ParseIloc(...) (*Iloc, error)` / `BuildIlocContent(*Iloc) ([]byte, error)` |
| `patch_iloc_offsets_at_threshold` | `PatchIlocOffsetsAtThreshold(data, ilocHdr *BoxHeader, threshold, delta int) ([]byte, bool, error)` |
| `parse_iinf_items` | `ParseIinfItems(...) ([]IinfItem, error)` |
| `IROT_TO_EXIF_ORIENTATION` / `EXIF_ORIENTATION_TO_IROT` | `IrotToExifOrientation = [4]uint16{1, 8, 3, 6}` / `ExifOrientationToIrot map[uint16]int` |
| `find_exif_item_location` | `FindExifItemLocation(...) (ExifItemLocation, bool, error)` |
| `find_exif_orientation_field` | `FindExifOrientationField(...) (ExifOrientationField, bool, error)` (`ByteOrder binary.ByteOrder`) |
| `crc32_excluding_range` | `Crc32ExcludingRange(data, exclStart, exclEnd int) uint32` |
| `find_provenance_box` | `FindProvenanceBox(data) (BoxHeader, bool, error)` |
| `parse_provenance` / `parse_provenance_lenient` | `ParseProvenance(data, hdr) (uint8, ProvenanceEntries, error)` / `ParseProvenanceLenient(data, hdr) (uint8, ProvenanceEntries, bool, error)` |
| `build_provenance_box` | `BuildProvenanceBox(version uint8, entries ProvenanceEntries) ([]byte, error)` (keys in sorted order, as in Python; Python's asserts on tag length and entry count are errors) |
| `locate_structures` | `LocateStructures(data) (*Structures, error)` |
| `_rotation_from_iprp_range` / `_extract_irot_state` | `rotationFromIprpRange(...) (int, error)` / `extractIrotState(...) (hadIrot bool, irotByte byte, ipmaFlags uint32, err error)` |
| `get_current_rotation` / `get_rotation_from_iprp_snapshot` | `GetCurrentRotation` / `GetRotationFromIprpSnapshot` |
| `apply_rotation(data, delta_turns, sync_exif=True, verbose=True)` | `ApplyRotation(data []byte, deltaTurns int, opts RotateOptions) ([]byte, error)`; `RotateOptions{NoExifSync bool; Log io.Writer}` |
| `_restore_meta_provenance_span` | `restoreMetaProvenanceSpan(span []byte, entries ProvenanceEntries, log logger) ([]byte, error)` |
| `_prepare_reversal` | `prepareReversal(data []byte, ignoreTamper bool, log logger) (*Reversal, error)`, a wrapper over the Go-only `PrepareReversalEnv(env FileEnv, data, ignoreTamper, log io.Writer)` |
| (Go-only) | `FileEnv` / `memEnv`, `ApplyRotationEnv`, `Reversal`, `Chunk`, `ReconstructedPlan`, `CheckReconstructedCrc`, `ErrNoProvenance`, `ExifFieldInBlob`, `ListBoxes` (`pkg/core`): the hooks the streaming engine uses. `streamEnv`, `View`, `ViewOfBytes`, `ApplyRotationReaderAt`, `ReverseRotationReaderAt`, `VerifyReversibleReaderAt`, `GatherInfoReaderAt`, `VerifyStructureView` (`pkg/stream`): the streaming engine over the same orchestration |
| `_iter_reconstructed_chunks` | `iterReconstructedChunks(data []byte, r *Reversal) iter.Seq[[]byte]` (sub-slices of `data`, no copies), called through the package variable `reconstructedChunks` (the test seam upstream monkeypatches) |
| `reverse_rotation(data, ignore_tamper=False, verbose=True)` | `ReverseRotation(data, opts ReverseOptions) ([]byte, error)`; `ReverseOptions{IgnoreTamper bool; Log io.Writer}` |
| `verify_reversible` | `VerifyReversible(data, opts ReverseOptions) error` |
| `gather_info` | `GatherInfo(data) (*Info, error)` |
| `format_info(result, quiet)` | `FormatInfo(info *Info, quiet bool) (text string, exitCode int)` (`""` is Python's `None`) |
| `verify_structure` | `VerifyStructure(data) error` |
| `ERROR_EXIT_CODE` | `const ErrorExitCode = 10` |
| `_version_string()` | `VersionString() string` (exported so callers can print it) |
| `_version.py` `VERSION` | `version.Version` (`pkg/version`) |
| — (Go-only) | `version.GoRevision`, `version.GoVersion`, `version.GoVersionString()` (see Key Decisions) |

### Function map: `cli.py` → `internal/cli/cli.go`

| Python | Go |
|---|---|
| `build_parser()` | `buildParser(prog string) *parser` (declarative spec consumed by `args.go`; same help strings, plus Go-only `-G`) |
| `_BARE_ROTATION_VALUES` | `bareRotationValues` |
| `_insert_implicit_rotate(argv)` | `insertImplicitRotate(argv []string) []string` (same algorithm) |
| `main()` | `Main(argv []string, stdout, stderr io.Writer) int` (`argv[0]` is the program name; returns the exit code instead of calling `sys.exit`, so it's testable in-process) |
| `_run(args, data)` | `run(a *args, stdout io.Writer) (int, error)` maps flags onto the file API. `_run`'s rotate and reverse halves are `files.RotateFile`/`files.ReverseFile` (`pkg/files`). Its info half is `files.InfoFile` + `core.FormatInfo`. The self-check goes through `verifyReversibleHook` in `pkg/files` (the seam upstream patches as `cli.verify_reversible`). Default naming is `files.DefaultRotateOutput`/`files.DefaultReverseOutput`. |
| module docstring (the `-h` description) | `internal/cli/docstring.go`, generated by `tools/gen_docstring.py` |

