# Task 5 Report

## Status

Implemented durable v2 forward execution and the committed boundary. Apply now executes the authoritative v2 manifest directly and retains transaction materials for later rollback/cleanup tasks.

## RED

Added the ten required deterministic state-machine tests. Initial focused run failed to compile because `ExecuteTransaction` and checkpoint hooks did not exist, demonstrating the legacy coarse executor could not expose or satisfy the required phase ordering.

## GREEN

The focused Task 5 suite, full workspace package, and complete repository suite pass.

## Phase ordering

- The transaction durably enters `applying` before forward operations.
- Capture and publication intent phases are durably persisted before their filesystem operation.
- Rename directory entries are synced before captured/published completion is persisted.
- Source capture and stage publication use atomic no-replace rename; source capture is descriptor-relative within the bound parent directory.
- Created parents are represented by and advanced through their own WAL entries.
- `AppliedTree` and `AppliedAt` remain present from preparation through commit.
- All final results are revalidated immediately before the durable `committed` transition, which is the only successful commit point.

## Identity reconciliation

Capture-intent and publish-intent states reconcile by checking both the moved identity and source absence. Final validation reopens and hashes regular files, validates symlink targets, validates directory identity attributes, and verifies deletes remain absent except where a created-parent entry intentionally republishes the same path. Open-descriptor writes before commit fail final validation; writes after durable commit are treated as external.

## Failure behavior

Any pre-commit failure durably transitions the manifest to `rolling-back`. No automatic reverse rollback or cleanup is attempted; preallocated captures, staged results, and published results are preserved for Task 6. Source-side entries are never pathname-deleted by the forward executor.

## Verification

- `go test ./session/workspace -run '^(TestForward|TestFinalValidation|TestOpenDescriptor|TestCommitted|TestApplyPreserves|TestApplyDirectory)' -count=1`
- `go test ./session/workspace -count=1`
- `go test ./... -count=1`
- `git diff --check`

All passed.

## Self-review

Checked intent-before-operation and sync-before-completion ordering, no-replace behavior, source capture preservation, commit placement, Apply lock scope, v2 manifest authority, and Task 6/7 scope boundaries. Compatibility tests that asserted legacy immediate rollback or cleanup were updated to assert retained `rolling-back`/`committed` v2 state instead.

## Commit

`6f113ed feat(workspace): persist forward apply phases`

## Concerns

Rollback and committed cleanup are intentionally deferred to Tasks 6 and 7, so transaction directories and source-side capture names remain after this task's successful or failed execution as appropriate.

## Fix Round 1

### Status

Addressed all six review findings. Forward execution now binds source and transaction roots once, resolves source/stage/capture/result paths descriptor-relatively with no-follow traversal, uses descriptor-relative rename/mkdir/stat/read operations, and verifies root bindings before commit. Created-parent ownership is persisted as device/inode and ambiguous publish-intent recovery is rejected.

Commit success is tracked separately from in-memory state. Precommit errors transition durably to `rolling-back`; a post-rename commit-sync error is accepted only when descriptor-relative manifest readback proves the committed transaction. The before-commit hook now precedes the final validation with no mutation seam afterward.

The prepared manifest now carries the authoritative applied entry set. Final validation scans complete created-parent subtrees and rejects unexpected children, while applied-tree construction removes stale descendants for directory replacements and deletes.

### RED

The first focused run failed to compile because the created-parent ownership fields did not exist. After adding only those schema fields, the focused tests failed for hook ordering, prepared revalidation state, unexpected created-parent children, and ambiguous created-parent recovery, confirming the reported behaviors.

Command:

- `go test ./session/workspace -run '^(TestFinalValidationRunsAfterBeforeCommitHook|TestPreparedRevalidationFailureDurablyEntersRollingBack|TestFinalValidationRejectsUnexpectedCreatedParentChild|TestCreatedParentPublishIntentWithoutIdentityIsAmbiguous|TestCreatedParentConcurrentCreatorIsNotClaimed)$' -count=1`

### GREEN

Added deterministic coverage for source-root and transaction-directory substitution, commit failure before manifest rename, uncertain failure after committed manifest rename, hook ordering, prepared revalidation failure, complete-tree unexpected children, and created-parent EEXIST/crash ambiguity.

Passing commands:

- `go test ./session/workspace -run '^(TestForward|TestFinalValidation|TestOpenDescriptor|TestCommitted|TestApplyPreserves|TestApplyDirectory|TestPreparedRevalidationFailure|TestCreatedParent|TestCommit)' -count=1`
- `go test ./session/workspace -count=1`
- `go test ./... -count=1`
- `git diff --check`

### Regressions

All workspace-package and repository tests pass. Existing Task 5 phase-ordering, open-descriptor, commit-boundary, preservation, and directory tests remain green.

### Concerns

Full reverse rollback and committed cleanup remain intentionally owned by Tasks 6 and 7. The durable manifest schema now includes `applied_entries` and created-parent device/inode ownership fields; recovery code in subsequent tasks must preserve and honor them.

## Fix Round 2

### Status

Addressed all five open findings. Final validation now scans every authoritative applied directory tree through descriptor-bound root and ancestor chains, created-parent ownership comes from the descriptor opened immediately after `mkdirat`, uncertain committed readback uses the shared strict parser and exact canonical manifest comparison, and manifest validation covers applied entries plus operation/phase/ownership invariants. Failure to bind forward roots now attempts a durable `rolling-back` transition while retaining transaction materials.

### RED evidence

The interrupted worktree already contained the new regression tests and partial implementation when this round resumed. The preserved tests target each finding directly:

- `TestFinalValidationRejectsUnexpectedChildInExistingAppliedDirectory`
- `TestFinalDirectoryScanRejectsReboundTopLevelChild`
- `TestCreatedParentReplacementAfterMkdirIsNotClaimed`
- `TestCommitUncertainReadbackRequiresExactCanonicalManifest`
- `TestCommitUncertainReadbackRejectsUnknownManifestField`
- `TestPreparedSourceRootOpenFailureDurablyEntersRollingBack`
- `TestValidateManifestValidatesAppliedEntriesAndCreatedOwnership`

The prior recorded RED state was the Task 5 implementation before these interrupted changes: it scanned only created-parent roots, obtained ownership with a later pathname stat, accepted readback by state/transaction ID, ignored the new schema fields in validation, and returned directly on root-open failure. On takeover, the exact focused command below passed because the interrupted implementation had already advanced beyond RED; all uncommitted work was preserved and reviewed rather than reset.

### Verification

- `go test ./session/workspace -run '^(TestFinalValidationRejectsUnexpectedChildInExistingAppliedDirectory|TestFinalDirectoryScanRejectsReboundTopLevelChild|TestCreatedParentReplacementAfterMkdirIsNotClaimed|TestCommitUncertainReadbackRequiresExactCanonicalManifest|TestCommitUncertainReadbackRejectsUnknownManifestField|TestPreparedSourceRootOpenFailureDurablyEntersRollingBack|TestValidateManifestValidatesAppliedEntriesAndCreatedOwnership)$' -count=1` — PASS
- `go test ./session/workspace -count=1` — PASS
- `go test ./... -count=1` — PASS
- `git diff --check` — PASS

### Regression classification

No failures remained in either the workspace package or full repository suite. Existing tests that intentionally defer immediate reverse rollback and committed cleanup to Tasks 6 and 7 remain unchanged. The genuine operation/phase validation issue was corrected by permitting the durable created-parent `publish-intent` state both before ownership is known and after ownership is persisted, while requiring ownership for later created-parent phases and forbidding it on other operations.

### Concerns

Reverse rollback and committed cleanup remain intentionally deferred to Tasks 6 and 7. If opening the transaction directory itself fails, the best-effort pathname rebind used to persist `rolling-back` can also fail; that error is joined and materials are not removed. The descriptor-bound subtree scan detects replacement of every opened authoritative root/ancestor at the final verification point, but cannot prevent external mutation after the durable commit boundary.
