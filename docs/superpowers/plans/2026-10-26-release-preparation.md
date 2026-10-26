# 0.9.0 Release Preparation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prepare the implemented 0.9.0 release for review, with a patched toolchain, complete release notes, and installation/upgrade evidence.

**Architecture:** Keep the existing shared engine, GUI, terminal, and release packaging. Update the toolchain and documentation, validate disposable release artifacts and databases, then submit the existing feature branch for CI and review.

**Tech Stack:** Go 1.26.10, staticcheck v0.8.1, govulncheck v1.8.0, Wails v2.16.0, GitHub Actions.

**Spec:** The owner's approved release-preparation priorities in this session and `docs/superpowers/specs/2026-10-18-lcars-gui-design.md`.

## Global Constraints

- Work on `feat/lcars-window`; preserve existing commits and untracked review artifacts.
- Go 1.26.10 fixes GO-2026-6809 and was released on 2026-10-19, meeting the one-week rule.
- Keep the `go 1.26.6` language directive and dependency versions unchanged.
- Do not alter the user's database, config, running radio, or installed shortcuts.
- Keep historical implementation plans as records of the toolchain used then.
- Publishing a tag/release and merging the PR are outside this preparation task.

## Review Focus

- A newer installed Go must not silently select an unpatched standard library for verification.
- The 0.9.0 release notes must cover everything since the published 0.6.1 release.
- Backup must work on the old schema before opening the new radio, without migrating its source.
- Upgrade must preserve loves, presets, learning, settings, and genuine history while removing stream-filename history.
- Windows archives must contain both executables; manifest hashes, extraction directories, command names, and shortcut targets must match.

---

### Task 1: Patch the build and complete the release notes

**Files:** `go.mod`, `AGENTS.md`, `README.md`, `CHANGELOG.md`, `docs/superpowers/specs/2026-10-18-lcars-gui-design.md`.

**Interfaces:** Go's toolchain directive; the release workflow's existing extraction of the single `## 0.9.0 — date` changelog section.

- [x] Change `toolchain go1.26.9` to `toolchain go1.26.10`. Update current build/verification instructions and the GUI design's current toolchain constraint.
- [x] Use `GOTOOLCHAIN=go1.26.10` in the README build examples and explain the patched toolchain requirement.
- [x] Consolidate the unpublished 0.8.0 notes into 0.9.0, dated 2026-10-26. State that 0.7.0 and 0.8.0 were not published. Combine duplicate section headings while preserving existing feature descriptions and recorded learning results.
- [x] Explain that the new terminal executable can export a backup before the first radio launch. Include the old-schema cleanup and the Go certificate-security fix.
- [x] The red check is already recorded: Windows and Linux `govulncheck` fail on GO-2026-6809 under Go 1.26.9. Rerun both under Go 1.26.10; require exit zero and no affected vulnerabilities.
- [x] Run formatting, tidy, vet, native/Linux staticcheck, and the race suite once with the updated toolchain. Validate the extracted release notes include both the GUI and previously unpublished features. Review the complete diff before committing.

### Task 2: Package, check upgrades, and submit for review

**Files:** Untracked evidence under `.review/release-0.9.0/`; existing `.github/workflows/release.yml` supplies the build/archive and Scoop-manifest recipes.

**Interfaces:** Release archive layout, `screech --version`, `screech export backup`, `core.OpenStore`, and the Scoop manifest.

- [x] Build all six release archives and the production Windows GUI using the existing workflow commands with version `0.9.0`; record each build result.
- [x] Inspect archive contents, hashes, version/build metadata, and manifest paths. Verify the Windows amd64 terminal runs from an extracted archive. Verify ARM64 through cross-build and executable metadata.
- [x] Check Scoop installation in an isolated environment if available. Otherwise record exactly which manifest, extraction, shim-target, and shortcut-target checks were possible; do not claim an actual installation.
- [x] Construct a disposable database from the schema in tag `v0.6.1`. Seed representative loves, presets, bandit/tag learning, volume, listens, genuine tracks, and filename-like tracks.
- [x] Use the new packaged terminal to export a backup before migration. Assert the source and backup retain schema version 1 and the seeded rows.
- [x] Upgrade a copy using the current `core.OpenStore` path, then reopen it. Assert the current schema, preserved listener data, expected history cleanup, integrity, and idempotence. Use only synthetic data.

**Submission:** Review and commit the preparation changes, push
`feat/lcars-window`, and open a PR against `main` describing the full release.
Inspect CI results and address concrete failures. The PR records CI status
and remaining checks before release publication.

## Local verification results

- Go 1.26.10: formatting, module tidy, vet, native/Linux staticcheck, the
  race suite, and native/Linux vulnerability scans passed.
- All six release archives built; archive layout, checksums, Windows
  executable metadata, version output, and manifest targets passed.
- A synthetic database built from the v0.6.1 schema exported a backup
  without migration, then upgraded to schema 5 with listener data intact.
  Only the seeded filename history was removed; reopening was idempotent.
- Scoop is absent on this machine. Manifest, extraction, and shortcut-target
  checks passed; an actual Scoop installation remains unchecked.
- Local evidence is in `.review/readiness-2026-10-26/patched-*.log` and
  `.review/release-0.9.0/RESULTS.md`. These scratch artifacts are not committed.
