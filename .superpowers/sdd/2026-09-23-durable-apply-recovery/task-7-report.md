# Task 7 Report

## Status

Implemented committed cleanup, private quarantine, typed cleanup-blocked warnings, complete receipts, and explicit acknowledgement.

## RED

Command:

```bash
go test ./session/workspace -run '^(TestApplyCleanupBlocked|TestCleanup|TestCleaningRecovery|TestCompleteTransaction|TestAcknowledgeApply)' -count=1
```

Result: failed to compile as expected because `ApplyResult.Warning`, `ApplyResult.TransactionID`, `AcknowledgeApply`, `CleanCommittedTransaction`, and `QuarantineSourceTemporary` did not exist.

## GREEN

Commands and results:

```bash
go test ./session/workspace -run '^(TestApplyCleanupBlocked|TestCleanup|TestCleaningRecovery|TestCompleteTransaction|TestAcknowledgeApply)' -count=1
```

PASS (`ok claude-squad/session/workspace`, 22.365s final run).

```bash
go test ./session/workspace -count=1
```

PASS (`ok claude-squad/session/workspace`, 312.558s).

```bash
go test ./... -count=1
```

PASS for all repository packages; workspace completed in 322.150s.

```bash
git diff --check
```

PASS, no output.

## Cleanup ordering and durability

Cleanup durably writes `cleaning`, then for each source capture writes `quarantine-intent`, atomically renames the identity-bound source temporary with no replacement into its predeclared transaction-private quarantine, validates the captured identity, writes `quarantined`, removes recursively only through the private descriptor-bound location, and writes `removed`. It finally writes `complete` and retains the manifest receipt until acknowledgement. Recovery resumes `committed` or `cleaning` cleanup idempotently.

## Source safety

No recursive deletion is performed against source namespace paths. Source temporaries are validated against WAL identity/tree metadata and atomically captured first. Destination occupants and source replacements are preserved; ambiguity fails closed as `cleanup-blocked`. Recursive removal operates only below the bound transaction descriptor. Acknowledgement accepts only a strict `complete` manifest and descriptor-relatively removes that transaction directory.

## Warning behavior

A cleanup ownership mismatch does not change the successful logical Apply result. Apply returns `TransactionID` plus `*CleanupBlockedWarning` containing affected and retained paths, and persists the exact condition as `cleanup-blocked`. Infrastructure cleanup errors are likewise converted to a typed warning if the blocked manifest can be persisted; failure to persist remains an error.

## Self-review

- Verified committed cleanup never calls rollback or mutates published result paths.
- Verified source namespace paths are never passed to recursive `RemoveAll`.
- Verified quarantine identity is checked both after interrupted intent and before resumed private removal.
- Verified complete metadata remains until explicit acknowledgement.
- Project-lock lifecycle integration intentionally remains Task 8.

## Commit

Implementation commit: `feat(workspace): quarantine committed apply cleanup` (SHA recorded after commit).

## Concerns

The descriptor-relative implementation is supported on the repository's current Darwin/Linux build targets, consistent with the existing v2 transaction executor. Task 8 still needs to consume recovery/cleanup-blocked lifecycle state under the project lock.

## Fix Round 1

### RED

```bash
go test ./session/workspace -run '^(TestCleanupQuarantineReplacementAfterValidationIsPreserved|TestCleanupBlockedReportsActualRetainedLocation|TestAcknowledgeApplyPreservesReplacementAfterValidation|TestAcknowledgeApplyRejectsExistingNonCompleteTransaction)$' -count=1
```

Failed deterministically: regular, symlink, and directory replacements at a validated quarantine name were deleted; a replacement transaction directory was acknowledged; and quarantine-occupant warnings omitted the private retained path.

### GREEN

```bash
go test ./session/workspace -run '^(TestApplyCleanupBlocked|TestCleanup|TestCommittedCleanup|TestCleaningRecovery|TestCompleteTransaction|TestAcknowledgeApply)' -count=1
```

PASS (`ok claude-squad/session/workspace`, 50.134s).

```bash
go test ./session/workspace -count=1
```

PASS (`ok claude-squad/session/workspace`, 345.206s).

```bash
go test ./... -count=1
```

PASS for all repository packages; workspace completed in 346.912s.

```bash
git diff --check
```

PASS, no output.

Private cleanup now atomically recaptures the validated inode under a descriptor-owned removing name before recursive deletion and rechecks identity across every name-based mutation. Acknowledgement reads and validates the complete receipt through the retained transaction descriptor, verifies the original binding, atomically captures it under a private root name, and only removes that captured inode. Cleanup-blocked receipts record every actual retained capture and/or quarantine path and reproduce the same typed warning after strict reload. Crash tests cover intent before/after rename, quarantined before removal, removal before durable removed, and final complete persistence.

## Fix Round 2

### RED

`go test ./session/workspace -run '^(TestCleanupPrivateCaptureReplacementIsPreserved|TestCommittedCleanupCrashPointsRecoverIdempotently|TestApplyCleanupBlockedReturnsSuccessWithTypedWarning|TestCleanupNeverRemoveAllsSourceNamespacePath)$' -count=1 -v`

Failed deterministically for the directory replacement case: after private `.removing-*` capture, cleanup reopened the replacement directory by name and recursively deleted its contents because the opened inode was not compared with the captured identity. The acceptance assertions also exposed the missing genuine post-durable-`quarantined` seam and insufficient exact-path/source-safety coverage.

### GREEN

`go test ./session/workspace -run '^(TestApplyCleanupBlocked|TestCleanup|TestCommittedCleanup|TestCleaningRecovery|TestCompleteTransaction|TestAcknowledgeApply)' -count=1 -v`

PASS (`ok claude-squad/session/workspace`, 54.221s).

`go test ./session/workspace -count=1`

PASS (`ok claude-squad/session/workspace`, 348.686s).

`go test ./... -count=1`

PASS for all repository packages; workspace completed in 355.684s.

`git diff --check`

PASS, no output.

Private directory recursion now begins only after the opened directory descriptor's device/inode matches the expected captured identity. Deterministic post-private-capture replacement tests cover regular files, symlinks, and directories; replacements survive and Apply-level reconciliation durably records exact quarantine and `.removing-*` paths. Crash coverage now interrupts after the `quarantined` manifest rename has succeeded and after private capture, then strictly reloads and safely resumes. Source-safety coverage exercises descriptor-relative private cleanup and proves published results and unrelated source entries remain untouched.

## Fix Round 3

### RED

`go test ./session/workspace -run '^TestCommittedCleanupCrashPointsRecoverIdempotently$' -count=1`

Failed to compile after moving the quarantined crash injection to a post-sync hook because no post-transaction-directory-sync seam existed. Inspection also confirmed private capture invoked its checkpoint immediately after rename without first syncing the affected parent directory.

### GREEN

`go test ./session/workspace -run '^(TestApplyCleanupBlocked|TestCleanup|TestCommittedCleanup|TestCleaningRecovery|TestCompleteTransaction|TestAcknowledgeApply)' -count=1 -v`

PASS (`ok claude-squad/session/workspace`, 54.427s).

`go test ./session/workspace -count=1`

PASS (`ok claude-squad/session/workspace`, 349.022s).

`go test ./... -count=1`

PASS for all repository packages; workspace completed in 356.951s.

`git diff --check`

PASS, no output.

Durable manifest publication now completes the bound transaction-directory sync before exposing the post-publication checkpoint or allowing cleanup mutation. Private no-replace capture now syncs the affected parent directory before its crash checkpoint and before any recursive removal. Crash tests inject only at these true post-sync seams, strictly reload the manifest, and resume cleanup idempotently while retaining all existing identity and exact-path assertions.
