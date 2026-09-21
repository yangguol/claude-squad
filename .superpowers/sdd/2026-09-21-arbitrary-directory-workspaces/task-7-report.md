# Task 7 Report

## Status

Implemented transactional directory apply with durable manifests, final touched-path revalidation, transaction-local payloads and backups, atomic replacement, rollback, incomplete transaction diagnostics, and successful apply metadata. Session worktrees and non-included policy paths remain intact.

## Evidence

### TDD red

`go test ./session/workspace -run 'TestApplyTransaction|TestApplyRollback|TestIncompleteTransaction' -v`

FAIL: `TestApplyTransactionCleanMerge` proved that the Task 6 implementation returned changed paths without modifying the source tree.

### Focused transactional tests

`go test ./session/workspace -run 'TestApplyTransaction|TestApplyRollback|TestIncompleteTransaction' -v`

PASS (`ok claude-squad/session/workspace 19.446s`). Coverage includes create/update/delete, executable mode, symlink creation, ignored/generated preservation, final OURS revalidation, rollback after updates and deletion, joined apply/rollback failures, retained recovery metadata, and startup discovery without deletion.

### Workspace suite

`go test ./session/workspace -v`

PASS (`ok claude-squad/session/workspace 80.113s`).

### Uncached full suite

`go test -count=1 ./...`

PASS. All repository packages passed; `session/workspace` completed in 81.737s.

### Static patch check

`git diff --check`

PASS with no output.

## Implementation

- Updated `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/apply.go` with transaction manifest states, materialization, backup, revalidation, deletion-first execution, atomic sibling replacement, reverse-order rollback, recovery discovery, and apply metadata.
- Updated `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/apply_test.go` with successful apply, policy preservation, race rejection, rollback, rollback failure, and discovery coverage.
- Updated `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/shadow.go` to report incomplete transactions while retaining them.
- Updated `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/shadow_test.go` with restore-time incomplete transaction diagnostics.

## Self-review

- Every changed relative path is revalidated against the planned OURS entry immediately before filesystem writes; mismatches return a path-specific `ConflictError`.
- Payload and backup paths are derived from validated relative paths and remain under the validated per-project transaction directory.
- Existing entries are backed up before any source mutation. Deletes execute before creates/updates, and every successful mutation is rolled back in reverse execution order on failure.
- Regular file and symlink replacements use temporary siblings followed by rename; symlink targets are stored and recreated without dereferencing.
- Apply and rollback errors are joined. A failed rollback leaves an `applying` manifest and payloads for diagnosis/recovery; successful rollback records `rolled_back` before removing its transaction directory.
- Successful transactions record `committed`, update `LastAppliedTree` and an RFC3339Nano UTC timestamp, remove transaction payloads, and do not alter or remove the session worktree.
- Unchanged apply returns no changed paths and performs no source writes.
- Restore scans transaction manifests, reports prepared/applying transactions, and never auto-deletes them.

## Concerns

- Apply metadata is persisted through the existing workspace serialization lifecycle; `Apply` updates the in-memory `ShadowData`, and the session owner must perform its normal save to durably store that metadata.
- Filesystem durability uses atomic rename but does not call directory/file `fsync`; a machine-level power loss can therefore leave the durable manifest and filesystem at different storage-flush points, which startup diagnostics expose for nonterminal manifests.

## Commit

`63b7acd feat: apply directory changes transactionally`

## Fix Round 1

### Status

Resolved all six review findings: source and manifest temporaries now use collision-safe exclusive siblings and only transaction-created names are cleaned; every source mutation is preceded by authoritative entry and symlink-safe ancestor revalidation after backup; rollback tracks and safely removes transaction-created parents; directory-to-file and file-to-tree changes are ordered correctly; regular-file rollback publishes through an exclusive sibling and atomic rename; and committed cleanup failures are returned while the committed manifest and payload remain discoverable.

### TDD evidence

The new focused tests initially failed as expected:

`go test ./session/workspace -run 'TestApplyPreservesPredictableSiblingCollision|TestApplyRejectsPostBackupSourceAndAncestorChanges|TestApplyRollbackRemovesCreatedParents|TestApplyDirectoryToFileAndFileToTree|TestApplyReportsCommittedCleanupFailure' -v`

FAIL: the predictable sibling was deleted, post-backup source/ancestor changes were overwritten, created parent directories survived rollback, both directory/file transition directions failed, and committed cleanup errors were ignored.

### Final focused transactional tests

`go test ./session/workspace -run 'TestApplyTransaction|TestApplyRollback|TestIncompleteTransaction|TestApplyPreservesPredictableSiblingCollision|TestApplyRejectsPostBackupSourceAndAncestorChanges|TestApplyDirectoryToFileAndFileToTree|TestApplyReportsCommittedCleanupFailure' -v`

PASS (`ok claude-squad/session/workspace 43.229s`).

### Final workspace suite

`go test ./session/workspace -v`

PASS (`ok claude-squad/session/workspace 111.491s`).

### Final uncached full suite

`go test -count=1 ./...`

PASS. All repository packages passed; `session/workspace` completed in 115.252s.

### Final static patch check

`git diff --check`

PASS with no output.

### Files

- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/apply.go`
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/apply_test.go`
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/.superpowers/sdd/2026-09-21-arbitrary-directory-workspaces/task-7-report.md`

### Concerns

- As before, filesystem durability is atomic at rename boundaries but does not fsync files or directories against machine-level power loss.
- The remaining unavoidable OS race is between the final userspace revalidation syscall and the immediately following mutation syscall; deterministic hooks verify that all changes introduced before final revalidation are rejected without overwrite.
