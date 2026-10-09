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

## Fix Round 3

### Status

Fixed the remaining created-parent ownership race. Created-parent directories now receive a preallocated private transaction-stage path. Forward execution creates and opens the directory there, obtains ownership from that descriptor, and only then atomically publishes that same inode to the source with descriptor-relative no-replace rename. A source entry installed before publication is rejected and is never claimed or removed.

### RED

Added `TestCreatedParentReplacementBeforeOwnershipOpenIsNotClaimed` with a deterministic hook immediately after private `mkdirat` and before ownership acquisition. The initial implementation still created directly at the source pathname, so the replacement was opened and accepted; the focused test failed because `ExecuteTransaction` returned nil.

Exact command and result:

- `/Users/yangguoliang/.local/bin/go test ./session/workspace -run '^TestCreatedParentReplacementBeforeOwnershipOpenIsNotClaimed$' -count=1` — FAIL: `An error is expected but got nil.`

### GREEN

The created inode is opened no-follow under the bound transaction directory before publication. Publication uses descriptor-relative no-replace rename into the bound source parent, syncs both affected directories, and verifies the published binding still matches the opened descriptor. The regression installs an external non-empty source directory in the former mkdir/open gap, proves execution fails into `rolling-back` without durable ownership, and confirms the external marker remains.

Exact commands and results:

- `/Users/yangguoliang/.local/bin/go test ./session/workspace -run '^TestCreatedParentReplacementBeforeOwnershipOpenIsNotClaimed$' -count=1` — PASS
- `/Users/yangguoliang/.local/bin/go test ./session/workspace -run '^(TestCreatedParent|TestForward|TestFinalValidation|TestOpenDescriptor|TestCommitted|TestApplyPreserves|TestApplyDirectory)' -count=1` — PASS
- `/Users/yangguoliang/.local/bin/go test ./session/workspace -count=1` — PASS
- `/Users/yangguoliang/.local/bin/go test ./... -count=1` — PASS
- `git diff --check` — PASS

### Concerns

Created-parent stage paths are now required v2 WAL fields and remain as transaction materials when publication fails; their later removal belongs to Task 7. Reverse rollback and cleanup remain intentionally unimplemented here. The implementation relies on the existing cross-directory same-filesystem atomic no-replace preflight and platform primitive.

## Fix Round 4

### Status

Verified both reviewer findings. Created-parent publication previously occurred inside `mkdirRelative` before the caller persisted the opened inode identity, and validation allowed identity-less `publish-intent`. Forward execution now creates, opens, and syncs the private staged directory, durably persists its device/inode together with `publish-intent`, and only then invokes atomic no-replace publication. Recovery-safe reconciliation accepts either the exact owned inode at the stage path before publication or at the source path after publication, and rejects ambiguous, missing, duplicated, or mismatched ownership.

### RED

Added deterministic coverage asserting that the publication checkpoint observes a durable nonzero identity while the exact directory remains privately staged and the source is absent, that an injected identity-manifest write failure cannot publish the source directory, and that identity-less created-parent `publish-intent` is invalid.

- `/Users/yangguoliang/.local/bin/go test ./session/workspace -run '^(TestForwardCreatedParentUsesWALState|TestCreatedParentIdentityWriteFailurePreventsPublication|TestValidateManifestValidatesAppliedEntriesAndCreatedOwnership)$' -count=1` — FAIL: publication checkpoint was not reached before rename; identity-write failure left the source published; validator accepted missing ownership.

### GREEN

- `/Users/yangguoliang/.local/bin/go test ./session/workspace -run '^(TestCreatedParent|TestForward|TestFinalValidation|TestOpenDescriptor|TestCommitted|TestApplyPreserves|TestApplyDirectory|TestValidateManifestRequiresOperationSpecificRecoveryFields|TestValidateManifestValidatesAppliedEntriesAndCreatedOwnership)' -count=1` — PASS
- `/Users/yangguoliang/.local/bin/go test ./session/workspace -count=1` — PASS
- `/Users/yangguoliang/.local/bin/go test ./... -count=1` — PASS
- `git diff --check` — PASS

### v2 compatibility decision

No version bump, migration, or optional-field derivation was added. The approved binding spec fixes `CurrentTransactionVersion = 2` and requires every recovery-critical name to be durable before source mutation. Task 5 is not released, and the coordinator confirms older manifests exist only as artifacts of this in-progress branch/test sequence, not as supported deployed WALs. The smallest fail-closed design is therefore to keep v2, require `StagePath` for every reachable current created-parent manifest, and reject obsolete intermediate v2 shapes rather than weaken recovery or invent an unsafe stage path. The operation-specific validator test now explicitly covers the created-parent stage requirement.

### Concerns

Reverse rollback and cleanup remain intentionally deferred to Tasks 6 and 7. A failed prepublication identity write may subsequently persist `rolling-back` with the in-memory identity; this is safe and useful because the owned inode remains private at the staged path. Transaction-private staged directories remain until those later lifecycle tasks process them.
