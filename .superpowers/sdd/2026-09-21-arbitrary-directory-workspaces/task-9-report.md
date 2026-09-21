# Task 9 Report

## Status

Implemented reference-aware project-level shadow cleanup and integrated it into `cs reset` after stored-instance, tmux, and native Git worktree cleanup. Cleanup validates all persisted and on-disk paths, preserves active projects and incomplete apply transactions, never follows metadata source paths, aggregates rejected candidates, and fails closed when persisted session state cannot be trusted.

## Evidence

### TDD red

`go test ./session/workspace -run 'TestCleanupShadowProjects' -v`

FAIL at compile time with `undefined: CleanupShadowProjects`, proving the cleanup API and behavior were absent.

### Focused cleanup/reset tests

`go test ./session/workspace . -run 'TestCleanupShadowProjects|TestReset' -v`

PASS. Covers validated unreferenced project deletion, source/external path preservation, symlink and malformed metadata rejection, stored-reference retention, malformed-state fail-closed behavior, active-worktree retention, incomplete transaction reporting/preservation, native-worktree-before-shadow reset ordering, and reset error propagation.

### Uncached full suite

`go test -count=1 ./...`

PASS. All repository packages passed; `session/workspace` completed in 133.342s.

### Static checks

- `go vet ./...` — PASS with no output.
- `git diff --check` — PASS with no output.

## Implementation

- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/shadow.go`: adds `CleanupShadowProjects() error`, validates the canonical shadow root and direct project descendants, validates persisted workspace references and project metadata, detects active Git worktrees, reports incomplete transactions, aggregates rejected candidates, and removes only validated unreferenced projects.
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/shadow_test.go`: adds cleanup safety, retained-reference, malformed-state, active-session, and incomplete-transaction coverage.
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/main.go`: keeps the existing storage, tmux, native worktree, and daemon reset behavior while inserting shadow cleanup after native worktree cleanup and exposing step failures.
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/main_test.go`: verifies reset ordering and shadow-cleanup failure propagation.

## Self-review

- Every deletion candidate is derived from a direct shadow-root directory entry, checked as a 64-character hexadecimal project ID, rejected if symlinked/non-directory, and canonically verified beneath the configured shadow root immediately before `os.RemoveAll`.
- Project metadata is read only from the validated project directory. `SourcePath` is treated only as identity data and is never traversed or deleted; metadata must be absolute, clean, and hash to the directory's project ID.
- Persisted shadow workspace paths are validated against the configured root and matching project metadata. Invalid or unreadable persisted state causes cleanup to fail closed and preserves every project while still reporting malformed on-disk candidates.
- A project is retained when a valid stored session references it or its validated repository reports a registered non-main worktree. Missing, linked, or externally redirected repository metadata is rejected rather than deleted.
- Incomplete prepared/applying transactions and committed-cleanup markers are reported and preserved before reference/activity checks, so recoverable state is not silently discarded even after reset clears stored instances.
- Reset preserves the prior Git cleanup call and ordering semantics; shadow cleanup runs only after storage, tmux, and native worktree cleanup succeed. A shadow cleanup error prevents a false completion message and daemon-stop continuation.

## Concerns

- `CleanupShadowProjects` intentionally refuses all project deletion if `state.json` exists but is malformed, symlinked, or unreadable. This favors data preservation and requires the user to repair/remove the bad state before retrying cleanup.
- Active-session detection relies on validated Git worktree registrations. Stale registrations that point to missing paths are reported and preserve the project until Git metadata is repaired/pruned.

## Commit

`eab8370 feat: clean shadow workspaces on reset`

## Fix Round 1

### Changes

- Reset now validates raw persisted shadow references before `DeleteAllInstances`; malformed state aborts reset without rewriting `state.json` or deleting shadow projects.
- Persisted-state parsing now rejects missing/null `instances`, malformed instance/workspace objects, and directory workspaces with missing/null shadow data while preserving valid legacy Git records.
- Project cleanup now revalidates root/project identity and atomically renames a direct project child to a randomized quarantine entry under the canonical shadow root before recursive removal. Replacement races fail closed and retain/report the replacement.
- Transaction directories without `manifest.json` are reported as incomplete and preserve their project; committed cleanup markers remain discoverable.
- Native Git cleanup now returns aggregated branch-deletion and `RemoveAll` failures instead of logging or discarding them, while retaining the existing cleanup scope.

### TDD and verification

- New regressions cover reset-path malformed-state preservation, strict stored-state shapes and legacy Git compatibility, project replacement before quarantine, missing transaction manifests, and injected native branch/removal failures.
- Focused: `go test -count=1 ./session/workspace . -run 'TestCleanupShadowProjects|TestStoredShadowProjectReferences|TestReset' -v` — PASS.
- Focused: `go test -count=1 ./session/git -run 'TestCleanupWorktree' -v` — PASS.
- Full: `go test -count=1 ./...` — PASS.
- Static: `go vet ./...` — PASS.
- Diff: `git diff --check` — PASS.

### Remaining concern

- Portable path APIs cannot eliminate every post-rename mutation race inside a directory controlled by a concurrent local actor. Quarantining the validated direct child under the same validated root removes the dangerous parent/symlink substitution window before recursive traversal; recursive removal remains best-effort and reports failures.

## Fix Round 2

### Changes

- Legacy state detection now decodes `worktree` and applies the same required Git fields used by workspace restoration instead of accepting the key's mere presence.
- Explicit null, empty, non-object, and incomplete legacy worktree data now fail reset preflight before storage or project cleanup; structurally valid legacy Git records remain accepted.
- Actual reset-path regressions verify malformed legacy state bytes and shadow project directories are preserved, and valid legacy state completes reset.

### TDD and verification

- Red: `go test -count=1 . -run 'TestReset(RejectsMalformedLegacyWorktree|AcceptsStructurallyValidLegacyGitRecord)' -v` — malformed legacy cases reached storage deletion/project cleanup instead of failing preflight.
- Green focused: `go test -count=1 ./session/workspace . -run 'TestCleanupShadowProjects|TestStoredShadowProjectReferences|TestReset' -v` — PASS.
- Full: `go test -count=1 ./...` — PASS; `session/workspace` completed in 117.385s.
- Static: `go vet ./...` — PASS with no output.
- Diff: `git diff --check` — PASS with no output.

### Concerns

- Descriptor-relative platform-specific recursive deletion remains intentionally out of scope; validated same-root quarantine rename containment is retained per ruling.
- Quoted porcelain paths remain deferred Minor per ruling.
