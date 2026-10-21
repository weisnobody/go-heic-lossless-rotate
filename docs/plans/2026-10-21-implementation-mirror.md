# Plan: Mirroring (`imir`) support

- **Date:** 2026-10-21
- **Status:** Implemented 2026-10-21 on `feature/mirror`; `make test lint difftest pixeltest` green. Released as
  `1.7.1+go.2`. Deviations from this plan:
  - **v3 parsing:** `ParseProvenance` accepts a v3 record only if it carries `OIMR`. Any other v3 record still gets
    Python's exact "newer than this script understands (max 2)" error, so the existing difftest "version 3" cases
    keep matching.
  - **Reverse order of `OIPF`:** `restoreImir` puts the pristine `ipma` flags back only when `irot` wasn't inserted
    too (`OIRT` is 1 byte); otherwise the `irot` step does it, because the inserted `irot` may still need the wide
    flag. It also keeps `meta`'s size field consistent, since it re-locates boxes before `OMSZ` is restored.
  - **Extra `iprp` children:** upstream's SLOW PATH rebuilds `iprp` from `ipco`+`ipma` only, so a crafted extra child
    is dropped and the edit isn't reversible. Mirroring inherits this; the CLI's self-check refuses such files, as it
    does for rotate.
  - **Apple decoder:** `sips -s format png` writes stored pixels without applying `irot`/`imir`, so the pixel test
    decodes with ImageIO through a small Swift helper (`pixeltest/imageio.swift`, built with `swiftc`).
    `heif-dec` and ImageIO both agree with every edit; swapping the axis values fails 24 checks.
  - **`info` on a file's own `imir`:** reports the whole orientation even without a provenance record (D8).
  - The CLI's argparse clone keeps the axis as a validated string; `run` maps it to `core.MirrorAxis`.
- **Covers:** a Go-only feature beyond upstream: the `mirror` subcommand (E2), plus fixing how `rotate`
  treats files with an `imir` (D8). SPEC.md previously listed mirroring as excluded; this plan changes that.
- **Upstream pin:** heic-lossless-rotate v1.7.1, commit `70d6c73` (unchanged)
- **Branch:** `feature/mirror` (from `dev`)

## Background: how libheif handles `imir`

From libheif's source (`strukturag/libheif@364905f`, 2026-10-16):

- `imir` is a plain box (not a full box) with one byte of content. Only bit 0 is used
  (`box.cc` `Box_imir::parse`):
  - **1** ("horizontal"): a left↔right flip, Exif orientation 2 (`pixelimage.cc` swaps `x` with `w-1-x`).
  - **0** ("vertical"): a top↔bottom flip, Exif orientation 4.
- `imir` is essential (`is_essential()`), like `irot`.
- Transformative properties are applied in the primary item's `ipma` association order (`image_item.cc`).
- libheif's own writer (`file.cc` `add_orientation_properties`) emits `irot` then `imir`, which is also
  MIAF's required order.
- `heif_orientation_concat` (`heif_encoding.cc`) is an 8×8 table composing Exif orientations: the D8
  group. `mini.cc` uses it to fold any ordered `irot`/`imir` list into one orientation.

Consequences:

1. **No `imir` value means "no flip"**, so un-mirroring means removing the property.
2. **Python 1.7.1 mishandles files that already have an `imir`.** With `irot` before `imir`, rotating CCW
   on screen needs `irot -= d`, but Python adds `d`. Its Exif sync also writes a non-mirrored value.

## Requirements

- [ ] `heic-rotate mirror {horizontal,vertical} in.heic [out]` flips the image as currently displayed.
      Same flags as `rotate` (`--no-exif-sync`, `--dry-run`, `-f`, `-q`). Default output
      `<input>_mirrored.heic`, with `rotate`'s `_restored` stripping.
- [ ] Mirroring twice the same way returns to the start; `horizontal` then `vertical` equals 180°.
- [ ] Rotating a mirrored image still turns it CCW on screen (D8).
- [ ] The Exif Orientation sync writes the full 1–8 value.
- [ ] `reverse` restores the original byte for byte through any mix of rotate and mirror edits.
- [ ] `mirror` on a file with its own `imir` fails with a clear error; `rotate` still works on it.
- [ ] Files that never involve an `imir` produce exactly Python's bytes, stdout and exit codes
      (C1–C5 unchanged). `-V` output is unchanged.
- [ ] `info` reports flips; exit code `5` means the net change this tool made includes a flip.

## Resolved Review Points

| # | Question | Decision |
|---|---|---|
| 1 | CLI surface | A new `mirror` subcommand, not flags on `rotate` |
| 2 | Python's rotate on `imir` files | Fix it, as divergence **D8** |
| 3 | Un-mirroring a file's own `imir` | Refuse with a clear error |
| 4 | `info` exit code for a flip | `5` |
| 5 | Provenance format | v3 only when the record carries `OIMR`; v2 otherwise |

## Technical Approach

1. **Orientation math** (`pkg/core/orient.go`, Go-only): Exif-style values 1–8, libheif's concat table,
   composition over the primary item's ordered associations, and a 4-candidate brute-force solve for the
   `irot` value that produces a target orientation.
2. **Refactor with no behavior change** (`pkg/core/core.go`): extract the `iprp` splice (meta size + iloc
   shift), the last-property removal used by reverse, and the provenance-record tail of `ApplyRotationEnv`,
   so `ApplyMirrorEnv` shares them.
3. **D8 for `rotate`:** only when the primary item has an `imir`. Fast path solves `irot` for the target;
   slow path (appended after the `imir`) is unchanged; Exif sync writes the target orientation.
4. **`ApplyMirror`:**

   | Primary item has | Action |
   |---|---|
   | no `imir`, no `irot` | Insert `irot`(0) and `imir`(axis) in one `iprp` splice, `irot` first |
   | `irot`, no `imir` | Append `imir`(axis); `irot` unchanged |
   | an `imir` this tool inserted (`OIMR` present; last `ipco` child; last association) | Remove it, then solve and patch `irot` |
   | its own `imir` | Refuse |

   The tool never patches an `imir` byte in place.
5. **Provenance:** `OIMR` (empty) is written on the first `imir` insertion and carried forward. `OIPF` is
   recorded at that point if missing (the current flags still equal the pristine ones). The record is
   format v3 when `OIMR` is present, else v2. `MirrorFormatVersion = 3` is accepted by reverse;
   `SupportedReverseFormatVersions` stays `{1, 2}` so `-V` is unchanged, and Python refuses v3 cleanly.
6. **Reverse:** before the `OPRP`/`OIRT` step, if `OIMR` is present and the last `ipco` child is an `imir`
   that the primary's last association points at, remove both and restore `OIPF`'s flags.
7. **Info:** Go-only fields for the full orientation. Text and exit codes are unchanged unless an `imir`
   is involved; then flips are reported and a net flip exits 5.

## Files to Modify

| File | Change |
|---|---|
| `pkg/core/core.go` | Refactors; Go-only D8 / `OIMR` / v3 hooks in rotate, reverse, info |
| `pkg/core/orient.go` (NEW) | D8 table, compose, solve, `findImirChild` |
| `pkg/core/mirror.go` (NEW) | `MirrorAxis`, `ApplyMirror`, `ApplyMirrorEnv` |
| `pkg/stream/stream.go` | `ApplyMirrorReaderAt` |
| `pkg/files/file.go` | `MirrorFile`, `DefaultMirrorOutput` |
| `internal/cli/*.go` | `mirror` subparser, dispatch, help for exit 5 |
| `internal/synth/synth.go` | `Imir`, `ImirFirst` options |
| `docs/*.md`, `README.md` | E2, D8, `OIMR`/v3, usage |
| `pkg/version/version.go` | `GoRevision` → 2 |

## Testing Strategy

- `orient_test.go`: libheif's `tests/mini_box.cc` composition cases; group properties of the table.
- `core_mirror_test.go`: synth layouts × op sequences; orientation, Exif value, `VerifyStructure`,
  byte-exact `reverse`, toggle-off structure, refusal on a file's own `imir`, D8 rotates in both orders.
- `pkg/stream`: byte-identical cross-check for mirror.
- `pkg/files`, `internal/cli`: naming, dry run, quiet, choice errors, info exit 5.
- `difftest`: Python refuses to reverse v3; rotate corpus has no `imir` inputs.
- `pixeltest` build tag: decode mirrored/rotated real files with `heif-dec` and `sips`, compare against a
  Go-side flip of the original decode.

## Risks

- **`imir` axis ambiguity** between spec editions: follow libheif; the pixel test checks libheif and
  Apple ImageIO.
- **Upstream may add mirroring later** with different tags: v3/`OIMR` are documented as Go-only.
- **A file's own `imir` plus an inserted `irot`** breaks MIAF's order; Python does the same, and
  decoders follow `ipma` order.
- **`TVER` says 1.7.1 on a v3 record**: noted in COMPAT.md.
