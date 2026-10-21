# Plan: Phase 6 - Release

- **Date:** 2026-10-20
- **Status:** In progress. Commit 1 (module path rename) is done.
- **Covers:** [PLAN.md](../PLAN.md) Phase 6 (Release): the items not already done on 2026-10-19
- **Upstream pin:** heic-lossless-rotate v1.7.1, commit `70d6c73`
- **Branch:** `feat/phase-6-release` (from `dev`)

## Requirements

- [ ] The module path matches the public GitHub mirror, so `go install` and `go get` work
- [ ] goreleaser release binaries for {darwin, linux, windows} × {amd64, arm64}, with checksums, built on the GitHub mirror
- [ ] The release fails unless the tag matches the code: `v`+`Version` for `GoRevision` 1, `v`+`GoVersion` for 2+ (DESIGN.md § Go port version and release tags)
- [ ] Proof that a tag pushed to arena reaches GitHub and starts the release workflow
- [ ] README install section: `go install`, release download, `go get`, and the `@vX.Y.Z+go.N` caveat
- [ ] Tag `v1.7.1` and confirm the GitHub release and `go install …@latest` both work

## Resolved Review Points

| # | Question | Decision |
|---|---|---|
| 1 | Module path vs mirror | The mirror is `github.com/weisnobody/go-heic-lossless-rotate`, so the module path is renamed to match. That's free before the first tag, and would break importers after it. |
| 2 | GitHub mirror status | It exists and is public, and the push mirror is configured. **Mirror syncs are triggered manually by the user**, so every step that needs GitHub waits on the user. |
| 3 | goreleaser locally | Installed (`/opt/homebrew/bin/goreleaser`); used for the local snapshot dry run |
| 4 | Draft or published release | **Publish directly** |

## Technical Approach

1. **Tag check as a Go test.** It reuses the version constants directly, nothing has to parse Go source with shell, and `go test` covers it.
2. **A separate `release.yml` workflow** that runs on `v*` tags and on manual runs. Its jobs are: tag check → the full CI suite (called from `ci.yml`) → goreleaser. A manual run builds a snapshot release and uploads it as a workflow artifact, without publishing anything, so the whole pipeline is proven before a real tag.
3. **Leave `+` out of release file names.** GitHub renames uploaded files with special characters in their names, which would make `checksums.txt` stop matching the files. Archives are named `…_1.7.1-go.2_…`; the release title still shows the real tag.
4. **Use a throwaway tag `vtest-mirror` for the mirror test.** It matches `v*`, so it starts the release workflow, and the tag check must fail it. That tests the mirror and the failure path at once. Go ignores tags that aren't valid versions, so the Go module proxy never caches it; the proxy keeps a version forever once fetched, so only the real `v1.7.1` may ever reach it.

## Files to Modify

### Commit 1 (done, 56932df): module path rename
- `go.mod`, every import, README and docs: `github.com/pjg/…` → `github.com/weisnobody/…`. The dated plan files under `docs/plans/` are historical records and keep the old path.
- `docs/DESIGN.md` Hosting row: name the mirror URL.

### `pkg/version/release_test.go` (NEW)
- `expectedTag(version string, revision int) string` returns `"v"+version` for revision 1, and `"v"+version+"+go."+N` for revision 2+.
- `TestExpectedTag` uses a table: `(1.7.1, 1)` → `v1.7.1`, `(1.7.1, 2)` → `v1.7.1+go.2`, and agreement with `GoVersion`.
- `TestReleaseTag` is skipped unless `RELEASE_TAG` is set. Otherwise it fails with `tag vX doesn't match GoVersion 1.7.1+go.1; want v1.7.1`.

### `.goreleaser.yaml` (NEW)
- Two builds, `heic-rotate` and `heic-lossless-rotate`: `CGO_ENABLED=0`, `-trimpath`, `-s -w`, and `mod_timestamp: "{{ .CommitTimestamp }}"` so rebuilding the same commit gives the same binaries.
- Archives: tar.gz, and zip for Windows. Each holds both binaries plus `README.md` and `LICENSE`. Name: `go-heic-lossless-rotate_{{ replace .Version "+" "-" }}_{{ .Os }}_{{ .Arch }}`.
- `checksums.txt`, and a changelog generated from git.

### `.github/workflows/release.yml` (NEW)
- `on: push: tags: ["v*"]` and `workflow_dispatch`.
- `check-tag`: `RELEASE_TAG=$GITHUB_REF_NAME go test -run TestReleaseTag ./pkg/version`. Skipped on manual runs.
- `ci`: `uses: ./.github/workflows/ci.yml`.
- `goreleaser`: needs both, has `permissions: contents: write`, checks out with `fetch-depth: 0`, and uses the current pinned `goreleaser/goreleaser-action`. A tag runs `release --clean`; a manual run runs `release --snapshot --clean` and then `upload-artifact` on `dist/`.

### `.github/workflows/ci.yml`
- Drop the `v*` tag trigger (`release.yml` runs CI for tags) and add `workflow_call`.

### `Makefile`
- `release-check TAG=…` runs the tag test locally. `snapshot` runs `goreleaser release --snapshot --clean`. `clean` already removes `dist/`.

### `README.md`
- Replace "Building" with **Install**: `go install github.com/weisnobody/go-heic-lossless-rotate/cmd/heic-rotate@latest`, the releases page, `go get …@latest`, and building from source.
- Expand "Versions": Go-only fixes are tagged `vX.Y.Z+go.N`, `@latest` doesn't see those tags, so install one with `@v1.7.1+go.N`, and `-G` shows the version you have.

### `docs/PLAN.md`, `docs/DESIGN.md`
- Tick items as they're verified. Record the file-name decision in DESIGN's release section.

## Execution Order

1. Commits on `feat/phase-6-release`: the module path rename (done); the tag test; the goreleaser config, workflows and Makefile; the README and docs. The user merges to dev and then main.
2. **Local snapshot dry run:**
   - Tag `v1.7.1+go.2` locally (never pushed) and run `goreleaser release --snapshot --clean`. This confirms goreleaser accepts a `+go.N` tag.
   - Check the archive names and contents, and run the darwin binary's `-G`.
   - Delete the local tag.
3. **Mirror test (after the merge to main):**
   - I push `vtest-mirror` to arena, and the user syncs the mirror.
   - The user confirms the tag is on GitHub and the release workflow fails at `check-tag`.
   - Delete the tag from arena, and from GitHub if the mirror doesn't remove it.
4. **Manual run of `release.yml` on GitHub (the user starts it):** it must finish green, with snapshot archives as artifacts.
5. **First release (only on the user's go):**
   - Tag `v1.7.1` on main, push it to arena, and the user syncs the mirror.
   - Check the GitHub release, run `go install …@latest` in a clean `GOPATH`, run `-G`, and tick PLAN.

## Testing Strategy

- **Tag test:** `TestExpectedTag`; `TestReleaseTag` with a matching and a mismatched `RELEASE_TAG`.
- **Pipeline:** the local snapshot, the failed `vtest-mirror` run, the green manual run, and then the real tag.

## Risks

- **Retagging is impossible:** the Go proxy keeps `v1.7.1` forever once fetched. Mitigation: tag only after step 4 is green.
- **Manual mirror sync:** a tag on arena isn't on GitHub until the user syncs. Mitigation: each GitHub step is a handoff to the user.
- **goreleaser-action major version:** it may have moved on. Mitigation: check the current release when writing the workflow, and pin it.

## Complexity

Medium. Little code, but several outward-facing steps that can't be undone and that depend on GitHub, so they're ordered to fail safely.
