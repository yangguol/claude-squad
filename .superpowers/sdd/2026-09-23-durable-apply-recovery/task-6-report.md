# Task 6 Report

## Status

Implemented automatic durable reverse rollback and crash recovery for prepared, applying, and rolling-back v2 transactions. Recovery is idempotent, descriptor-relative, no-follow, identity-bound, and fail-closed when external or ambiguous content is observed.

## RED evidence

Added the required recovery tests covering prepared/applying/rolling-back states, interrupted phase writes, operation/type transitions, repeated invocation, blocked restoration, retained content, and pathname-temporary safety.

Initial command:

- `go test ./session/workspace -run '^(TestRecovery|TestRollbackNever)' -count=1`

Expected result: build failed with undefined `RecoverTransaction`, `RollbackTransaction`, and recovery dispositions, proving no recovery executor existed.

## Implementation

- Added `RecoveryDisposition`, `RecoveryIssue`, `RecoveryReport`, `ApplyMetadataRepair`, and `ObservedEntryState` interfaces.
- Added `RecoverTransaction`, `RollbackTransaction`, and `ObserveEntryState`.
- Apply failures now invoke rollback automatically after durably entering `rolling-back`.
- Existing bound roots are reused when forward execution fails, preventing pathname rebound to a substituted transaction directory.
- Every source and transaction operation uses descriptor-relative no-follow traversal.
- Published results are atomically moved to their predeclared rollback captures with no-replace semantics.
- Captured originals are restored only into absent targets.
- Retained published results are removed only after identity revalidation immediately before descriptor-relative unlink/rmdir.
- Created-parent directories are removed only when their durable device/inode identity still matches and the directory is empty.

## Phase reconciliation

- `capture-intent`: original at capture plus absent target is recognized as completed capture; original still at target is recognized as not yet captured.
- `publish-intent`: result at source plus absent stage is recognized as completed publication; result at stage is retained as unpublished.
- `rollback-capture-intent`: result at rollback capture plus absent target is recognized as completed rollback capture.
- `rollback-captured`: an already restored original at target with absent capture is recognized as completed restoration.
- `restored`: treated as idempotently complete.
- Created-parent `publish-intent` reconciles the durable inode at either private `StagePath` or source `Path`.
- Rollback processes entries in reverse manifest order and persists every transition; final success persists `rolled-back` before any later private cleanup task.

## Blocked behavior

If a restoration target is occupied, a rollback capture name is occupied, an expected capture changed/disappeared, a created directory contains external content, or ownership is otherwise ambiguous, recovery leaves all observed content untouched and durably writes `recovery-blocked`. The report contains the exact affected and retained relative paths. No source temporary is deleted merely because its pathname was previously validated.

## GREEN evidence

Passing commands:

- `go test ./session/workspace -run '^(TestRecovery|TestRollbackNever|TestApplyRollback)' -count=1` — pass, 33.152s
- `go test ./session/workspace -count=1` — pass, 278.013s
- `go test ./... -count=1` — pass; all repository packages
- `git diff --check` — pass

## Self-review

Reviewed reverse ordering, intent-before-filesystem and filesystem-before-completion persistence, interrupted-write inference, created-parent stage/source inode reconciliation, no-replace restoration, identity-before-delete behavior, root substitution handling, external occupant preservation, retained-path reporting, repeated invocation, and Task 7/8 boundaries. Committed cleanup and project-lock lifecycle integration were not added.

## Commit

Pending at report creation; recorded after commit below.

## Concerns

- Successful rollback intentionally retains the transaction directory and manifest for Task 7 cleanup.
- `ApplyMetadataRepair` is included as the stable report field required by the interface; metadata repair remains Task 9.
- Recovery-blocked lifecycle and project-lock integration remain Task 8.
