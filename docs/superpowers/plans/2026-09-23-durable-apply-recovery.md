# Durable Apply Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the in-process directory Apply transaction with a durable write-ahead state machine that automatically rolls back pre-commit crashes, safely completes committed cleanup, and never deletes concurrent source replacements.

**Architecture:** Keep merge planning in `apply.go`, but move the versioned WAL, durable I/O, project locking, and recovery engine into focused `session/workspace` files. Every source mutation is preceded by a synced intent and uses atomic no-replace rename; startup reconciles the durable phase with observed identities and either converges safely or exposes a read-only blocked workspace.

**Tech Stack:** Go 1.25, `golang.org/x/sys/unix`, Git plumbing, JSON WAL, Darwin `renameatx_np`, Linux `renameat2`, Bubble Tea/Lip Gloss.

**Spec:** `docs/superpowers/specs/2026-09-21-arbitrary-directory-workspaces-design.md`

## Global Constraints

- Git workspaces retain their existing behavior.
- The source directory never receives `.git` metadata.
- Merge conflicts never mutate the source.
- A complete versioned manifest and all recovery-critical names must be durable before the first source mutation.
- Every source mutation has a preceding durable intent; only durable `committed` is success.
- Source entries and source-side temporaries are never removed by pathname after validation. They are atomically captured with no-replace semantics first.
- Pre-commit recovery always rolls back; committed recovery only cleans.
- Concurrent external content is preserved in place and produces `recovery-blocked` or `cleanup-blocked`.
- Apply requires source and transaction root on the same filesystem.
- Apply is supported only where Darwin/Linux atomic no-replace rename is available; unsupported systems fail before source mutation.
- Apply, recovery, baseline scan, Resume, and reset transaction preflight share one non-blocking project OS lock.
- Blocked sessions remain viewable but cannot create sessions, Resume, or Apply.
- Cleanup-blocked Apply is successful with a typed warning; metadata is still persisted and the session paused.
- WAL applied tree/time are authoritative if session storage was not saved before a crash.
- Legacy incomplete manifests are retained without reinterpretation or rewriting.
- Do not address unrelated deferred UI or Git porcelain issues.

---

### Task 1: Versioned WAL Schema and Durable Manifest I/O

**Files:**
- Create: `session/workspace/transaction.go`
- Create: `session/workspace/transaction_test.go`
- Modify: `session/workspace/apply.go`

**Interfaces:**
- Consumes: `Entry`, `EntryType`, current `fileOps`, and transaction planning data.
- Produces:

```go
const CurrentTransactionVersion = 2

type TransactionState string
type EntryOperation string
type EntryPhase string
type CleanupPhase string

type TransactionEntry struct {
    Path, StagePath, CaptureName, RollbackCaptureName, QuarantinePath string
    Operation EntryOperation
    Phase EntryPhase
    CleanupPhase CleanupPhase
    Expected, Result *Entry
    ExpectedTree map[string]Entry
}

type TransactionManifest struct {
    Version int
    TransactionID, ProjectID, SessionID, SourcePath string
    State TransactionState
    AppliedTree, AppliedAt string
    Entries []TransactionEntry
}

func WriteManifestDurable(manifest TransactionManifest, transactionDir string) error
func SyncDirectory(path string) error
```

- [ ] **Step 1: Add RED schema and durability tests**

Add:

```go
func TestWriteManifestDurableSyncsFileBeforeRenameAndDirectoryAfterRename(t *testing.T)
func TestWriteManifestDurableRetainsPreviousManifestWhenReplacementFails(t *testing.T)
func TestWriteManifestDurableRejectsPartialWrite(t *testing.T)
func TestTransactionStateJSONUsesApprovedHyphenatedNames(t *testing.T)
```

Use an instrumented `fileOps` to assert `write -> file sync -> close -> rename -> directory sync`. Assert version `2` and exact state strings from the spec.

- [ ] **Step 2: Verify RED**

Run:

```bash
go test ./session/workspace -run '^(TestWriteManifestDurable|TestTransactionStateJSON)' -count=1
```

Expected: compile failures for the new types/functions or ordering assertions showing no sync.

- [ ] **Step 3: Implement the minimal schema and durable writer**

`WriteManifestDurable` must marshal, create an exclusive private temporary, write all bytes, `Sync` it, close it, atomically replace `manifest.json`, then sync the transaction directory. A failed replacement leaves the prior manifest parseable. Direct removal is permitted only for transaction-private temporary files.

- [ ] **Step 4: Verify GREEN and regression suite**

```bash
go test ./session/workspace -run '^(TestWriteManifestDurable|TestTransactionStateJSON)' -count=1
go test ./session/workspace -count=1
```

- [ ] **Step 5: Commit**

```bash
git add session/workspace/transaction.go session/workspace/transaction_test.go session/workspace/apply.go
git commit -m "feat(workspace): add durable apply WAL schema"
```

---

### Task 2: Project Lock and Atomic Apply Preflight

**Files:**
- Create: `session/workspace/project_lock.go`
- Create: `session/workspace/project_lock_unix.go`
- Create: `session/workspace/project_lock_unsupported.go`
- Create: `session/workspace/project_lock_test.go`
- Create: `session/workspace/platform_apply.go`
- Create: `session/workspace/platform_apply_unix.go`
- Create: `session/workspace/platform_apply_unsupported.go`
- Modify: `session/workspace/apply.go`

**Interfaces:**

```go
type OperationInProgressError struct { ProjectID, Operation string }
type ProjectLock struct { /* private descriptor */ }
func AcquireProjectLock(projectPath, operation string) (*ProjectLock, error)
func (l *ProjectLock) Close() error
func VerifyAtomicApplyPlatform() error
func VerifySameFilesystem(sourcePath, transactionRoot string) error
func ProbeAtomicNoReplace(transactionRoot string) error
```

- [ ] **Step 1: Add RED locking/preflight tests**

```go
func TestProjectLockIsNonBlockingAndReleasedByClose(t *testing.T)
func TestProjectLockSerializesSessionsForOneProject(t *testing.T)
func TestProjectLockNeverTreatsContentsAsStale(t *testing.T)
func TestApplyRejectsDifferentFilesystemsBeforeSourceMutation(t *testing.T)
func TestAtomicNoReplacePreflightRunsBeforeManifestAndSourceMutation(t *testing.T)
```

Inject `filesystemDeviceID = platformFilesystemDeviceID` for the cross-device test.

- [ ] **Step 2: Verify RED**

```bash
go test ./session/workspace -run '^(TestProjectLock|TestApplyRejectsDifferentFilesystems|TestAtomicNoReplacePreflight)' -count=1
```

Expected: lock APIs absent and Apply reaches transaction creation without same-filesystem rejection.

- [ ] **Step 3: Implement non-blocking OS lock and preflight**

Store locks under `shadow-projects/.locks/<project-id>.lock`. Unix uses `unix.Flock(fd, LOCK_EX|LOCK_NB)`; contention maps to `OperationInProgressError`. Never infer stale ownership from lock-file contents. Verify OS support, device identity, and no-replace probe before manifest publication or source mutation.

- [ ] **Step 4: Verify**

```bash
go test ./session/workspace -run '^(TestProjectLock|TestApplyRejectsDifferentFilesystems|TestAtomicNoReplacePreflight)' -count=1
```

- [ ] **Step 5: Commit**

```bash
git add session/workspace/project_lock*.go session/workspace/platform_apply*.go session/workspace/apply.go
git commit -m "feat(workspace): serialize and preflight atomic apply"
```

---

### Task 3: Strict Manifest Discovery and Legacy Classification

**Files:**
- Modify: `session/workspace/transaction.go`
- Create: `session/workspace/recovery.go`
- Create: `session/workspace/recovery_test.go`
- Modify: `session/workspace/apply.go`

**Interfaces:**

```go
type DiscoveredTransaction struct {
    Directory string
    Manifest *TransactionManifest
    Legacy, Orphan bool
    Invalid error
}
func DiscoverTransactions(projectPath string) ([]DiscoveredTransaction, error)
func ReadManifestStrict(projectPath, transactionDir string) (TransactionManifest, error)
func ValidateManifest(projectPath, transactionDir string, manifest TransactionManifest) error
```

- [ ] **Step 1: Add RED validation tests**

Cover exact tests:

```go
TestReadManifestStrictRejectsUnknownFieldsAndTrailingJSON
TestValidateManifestRejectsUnknownVersionStateOperationAndPhase
TestValidateManifestRejectsDuplicateTargetAndTemporaryPaths
TestValidateManifestRejectsAbsoluteTraversalAndOutOfRootPaths
TestValidateManifestRejectsSymlinkedTransactionComponents
TestInvalidManifestIsReportedWithoutRewritingBytes
TestLegacyIncompleteManifestIsRetainedForManualRecovery
TestOrphanPreManifestDirectoryIsQuarantinedAndRemoved
TestOrphanCleanupRejectsSymlinkAndNonDirectChild
TestDiscoverySortsTransactionsAndIssuesDeterministically
```

Reject unknown fields/trailing JSON, invalid phase/operation combinations, duplicate names, absolute/unclean/escaping paths, source/project mismatch, and symlinked directories/manifests. Version 1 is manual-only.

- [ ] **Step 2: Verify RED**

```bash
go test ./session/workspace -run '^(TestReadManifestStrict|TestValidateManifest|TestInvalidManifest|TestLegacyIncomplete|TestOrphan|TestDiscoverySorts)' -count=1
```

Expected: current permissive decoding accepts malformed data and cannot classify legacy/orphan state.

- [ ] **Step 3: Implement strict discovery**

Use `json.Decoder.DisallowUnknownFields`, require EOF, reconstruct paths from trusted roots, and never rewrite invalid bytes. Orphans are removed only after validated same-root quarantine; source mutation is impossible before a v2 manifest exists.

- [ ] **Step 4: Verify and commit**

```bash
go test ./session/workspace -run '^(TestReadManifestStrict|TestValidateManifest|TestInvalidManifest|TestLegacyIncomplete|TestOrphan|TestDiscoverySorts)' -count=1
git add session/workspace/transaction.go session/workspace/recovery.go session/workspace/recovery_test.go session/workspace/apply.go
git commit -m "feat(workspace): validate durable apply transactions"
```

---

### Task 4: Complete Preallocated WAL

**Files:**
- Modify: `session/workspace/apply.go`
- Modify: `session/workspace/transaction.go`
- Modify: `session/workspace/apply_test.go`
- Modify: `session/workspace/transaction_test.go`

**Interfaces:**

```go
func (w *shadowWorkspace) prepareTransaction(plan applyPlan, now time.Time) (TransactionManifest, error)
func preallocateTransactionEntries(plan applyPlan) ([]TransactionEntry, error)
func stageTransactionResults(transactionDir string, entries []TransactionEntry) error
```

- [ ] **Step 1: Add RED preparation tests**

```go
TestPrepareTransactionPreallocatesEveryOperationAndCreatedParent
TestPrepareTransactionPreallocatesUniqueSourceAndPrivatePaths
TestPrepareTransactionDurablyStagesRegularFilesAndSymlinks
TestPreparedManifestContainsCompleteRollbackInformation
TestPrepareTransactionDoesNotMutateSource
```

Assert all source captures, rollback captures, stages, quarantine paths, expected/result identities, and parent actions exist before mutation. Atomic source capture—not copied backup—is rollback material.

- [ ] **Step 2: Verify RED**

```bash
go test ./session/workspace -run '^(TestPrepareTransaction|TestPreparedManifest)' -count=1
```

Expected: phases/quarantine/rollback names and parent entries are missing.

- [ ] **Step 3: Implement preparation**

Sync payload/stage data before durable `prepared`. Represent created parents explicitly, shallow-first for creation and deep-first for rollback. Do not create recovery-critical names at runtime.

- [ ] **Step 4: Verify and commit**

```bash
go test ./session/workspace -run '^(TestPrepareTransaction|TestPreparedManifest)' -count=1
go test ./session/workspace -count=1
git add session/workspace/apply.go session/workspace/apply_test.go session/workspace/transaction.go session/workspace/transaction_test.go
git commit -m "feat(workspace): preallocate complete apply WAL"
```

---

### Task 5: Durable Forward Execution and Commit Boundary

**Files:**
- Create: `session/workspace/transaction_execute.go`
- Modify: `session/workspace/apply.go`
- Modify: `session/workspace/transaction.go`
- Modify: `session/workspace/apply_test.go`

**Interfaces:**

```go
func ExecuteTransaction(manifest *TransactionManifest, transactionDir string) error
func ApplyEntryForward(manifest *TransactionManifest, entryIndex int, transactionDir string) error
func ValidatePublishedResults(manifest TransactionManifest, transactionDir string) error
```

- [ ] **Step 1: Add RED state-machine tests**

```go
TestForwardCreatePersistsIntentBeforePublication
TestForwardUpdatePersistsCaptureAndPublishPhases
TestForwardDeletePersistsCaptureBeforeTargetAbsence
TestForwardCreatedParentUsesWALState
TestForwardCaptureValidationFailureRollsBack
TestForwardPublicationRefusesConcurrentTarget
TestFinalValidationDetectsOpenDescriptorWriteBeforeCommit
TestOpenDescriptorWriteAfterDurableCommitIsExternal
TestCommittedManifestContainsAppliedTreeAndTime
TestCommittedIsTheOnlySuccessfulApplyCommitPoint
```

Use ordered durability events and existing race hooks to prove intent is durable before rename and completion after synced rename.

- [ ] **Step 2: Verify RED**

```bash
go test ./session/workspace -run '^(TestForward|TestFinalValidation|TestOpenDescriptor|TestCommitted)' -count=1
```

Expected: current coarse transaction state cannot satisfy ordering and final validation assertions.

- [ ] **Step 3: Implement forward phases**

Persist `applying`; for each operation persist intent, perform no-replace rename, sync both affected directories, validate identity, then persist completion. Set applied tree/time, validate all published results immediately, then durably persist `committed` as the sole success point.

- [ ] **Step 4: Verify and commit**

```bash
go test ./session/workspace -run '^(TestForward|TestFinalValidation|TestOpenDescriptor|TestCommitted|TestApplyPreserves|TestApplyDirectory)' -count=1
git add session/workspace/transaction_execute.go session/workspace/transaction.go session/workspace/apply.go session/workspace/apply_test.go
git commit -m "feat(workspace): persist forward apply phases"
```

---

### Task 6: Automatic Reverse Rollback and Recovery

**Files:**
- Modify: `session/workspace/recovery.go`
- Modify: `session/workspace/transaction_execute.go`
- Modify: `session/workspace/recovery_test.go`
- Modify: `session/workspace/apply_test.go`

**Interfaces:**

```go
type RecoveryDisposition string
type RecoveryIssue struct { TransactionID string; State TransactionState; Message string; Paths, RetainedPaths []string }
type RecoveryReport struct { Disposition RecoveryDisposition; Issues []RecoveryIssue; Repairs []ApplyMetadataRepair; Orphans []string }
type ObservedEntryState struct { /* target/capture/rollback/stage/quarantine identities */ }
func RecoverTransaction(manifest *TransactionManifest, transactionDir string) (RecoveryReport, error)
func RollbackTransaction(manifest *TransactionManifest, transactionDir string) (RecoveryReport, error)
func ObserveEntryState(manifest TransactionManifest, entry TransactionEntry, transactionDir string) (ObservedEntryState, error)
```

- [ ] **Step 1: Add RED recovery tests**

```go
TestRecoveryPreparedTransactionRollsBack
TestRecoveryApplyingTransactionRollsBackInReverseOrder
TestRecoveryRollingBackTransactionResumesIdempotently
TestRecoveryInfersCaptureCompletedBeforePhaseWrite
TestRecoveryInfersPublicationCompletedBeforePhaseWrite
TestRecoveryInfersRestorationCompletedBeforePhaseWrite
TestRecoveryCreateUpdateDeleteAndTypeTransitions
TestRecoveryRepeatedInvocationIsIdempotent
TestRecoveryExternalRestoreTargetBecomesRecoveryBlocked
TestRecoveryBlockedPreservesExternalAndRetainedContent
TestRollbackNeverRemovesSourceTemporaryByPathname
```

- [ ] **Step 2: Verify RED**

```bash
go test ./session/workspace -run '^(TestRecovery|TestRollbackNever)' -count=1
```

Expected: no recovery executor exists; rollback depends on in-memory slices.

- [ ] **Step 3: Implement idempotent rollback**

Persist `rolling-back`; reconcile phase with observed identities; reverse operations. Capture published results using predeclared no-replace names, restore originals only into absent targets, and persist each transition. A conflicting target remains untouched and yields durable `recovery-blocked` with exact paths. A completed rollback becomes durable `rolled-back` before private metadata deletion.

- [ ] **Step 4: Verify and commit**

```bash
go test ./session/workspace -run '^(TestRecovery|TestRollbackNever|TestApplyRollback)' -count=1
git add session/workspace/recovery.go session/workspace/transaction_execute.go session/workspace/recovery_test.go session/workspace/apply_test.go
git commit -m "feat(workspace): recover applies by durable rollback"
```

---

### Task 7: Committed Cleanup, Private Quarantine, and Warning

**Files:**
- Modify: `session/workspace/recovery.go`
- Modify: `session/workspace/transaction.go`
- Modify: `session/workspace/apply.go`
- Modify: `session/workspace/workspace.go`
- Modify: `session/workspace/recovery_test.go`
- Modify: `session/workspace/apply_test.go`

**Interfaces:**

```go
type CleanupBlockedWarning struct { TransactionID string; Paths, RetainedPaths []string }
func (w *CleanupBlockedWarning) Error() string
func CleanCommittedTransaction(manifest *TransactionManifest, transactionDir string) (*CleanupBlockedWarning, error)
func QuarantineSourceTemporary(manifest *TransactionManifest, entryIndex int, transactionDir string) error
func (w *shadowWorkspace) AcknowledgeApply(transactionID string) error
```

Extend `ApplyResult` with `TransactionID string` and `Warning *CleanupBlockedWarning`.

- [ ] **Step 1: Add RED cleanup tests**

```go
TestApplyCleanupBlockedReturnsSuccessWithTypedWarning
TestCleanupCommittedTransactionNeverRollsBack
TestCleanupCapturesSourceTemporaryBeforeRecursiveRemoval
TestCleanupNeverRemoveAllsSourceNamespacePath
TestCleanupReplacementBecomesCleanupBlocked
TestCleaningRecoveryResumesIdempotently
TestCompleteTransactionRetainsMetadataUntilAcknowledged
TestAcknowledgeApplyRemovesOnlyCompleteTransaction
```

- [ ] **Step 2: Verify RED**

```bash
go test ./session/workspace -run '^(TestApplyCleanupBlocked|TestCleanup|TestCleaningRecovery|TestCompleteTransaction|TestAcknowledgeApply)' -count=1
```

Expected: current cleanup deletes source siblings directly and returns an Apply error.

- [ ] **Step 3: Implement committed cleanup**

Persist `cleaning`; atomically move each validated source temporary into its predeclared private quarantine; only there use recursive deletion. Replacement/mismatch produces `cleanup-blocked` and successful `ApplyResult.Warning`. Persist `complete`, retain a minimal receipt until metadata acknowledgement, and never roll back committed results.

- [ ] **Step 4: Verify and commit**

```bash
go test ./session/workspace -run '^(TestApplyCleanupBlocked|TestCleanup|TestCleaningRecovery|TestCompleteTransaction|TestAcknowledgeApply)' -count=1
git add session/workspace/recovery.go session/workspace/transaction.go session/workspace/apply.go session/workspace/workspace.go session/workspace/recovery_test.go session/workspace/apply_test.go
git commit -m "feat(workspace): quarantine committed apply cleanup"
```

---

### Task 8: Locked Recovery and Read-Only Shadow Lifecycle

**Files:**
- Modify: `session/workspace/shadow.go`
- Modify: `session/workspace/workspace.go`
- Modify: `session/workspace/factory.go`
- Modify: `session/instance.go`
- Modify: `session/workspace/shadow_test.go`
- Modify: `session/instance_test.go`

**Interfaces:**

```go
type AccessState struct { ReadOnly bool; Disposition RecoveryDisposition; Message string; Paths, RetainedPaths []string }
func (w *shadowWorkspace) AccessState() AccessState
func RecoverProject(projectPath string, sessions map[string]*ShadowData) (RecoveryReport, error)
```

Add `AccessState()` and `AcknowledgeApply(string)` to `Workspace`; Git returns zero state/no-op acknowledgement.

- [ ] **Step 1: Add RED lifecycle tests**

```go
TestNewShadowHoldsProjectLockAcrossBaselineScan
TestApplyAndResumeContendOnSameProjectLock
TestNewSessionRejectedWhileProjectRecoveryBlocked
TestRestoreRecoveryBlockedSessionReadOnly
TestRestoreCleanupBlockedSessionReadOnly
TestRestoreLegacyIncompleteSessionReadOnly
TestReadOnlySessionDisablesApplyAndResume
TestReadOnlyRestoreDoesNotStartTmuxOrRecreateWorktree
TestHealthyRecoveredSessionRetainsNormalCapabilities
```

- [ ] **Step 2: Verify RED**

```bash
go test ./session/workspace ./session -run '^(TestNewShadowHolds|TestApplyAndResume|TestNewSessionRejected|TestRestore|TestReadOnly|TestHealthyRecovered)' -count=1
```

- [ ] **Step 3: Integrate lock/recovery**

Use private locked helpers to avoid recursive lock acquisition. Lock before baseline scan, recovery, Resume setup, and Apply. Valid blocked/manual transactions return viewable workspaces with Apply/Resume capabilities disabled; no tmux/worktree recreation occurs.

- [ ] **Step 4: Verify and commit**

```bash
go test ./session/workspace ./session -run '^(TestNewShadowHolds|TestApplyAndResume|TestNewSessionRejected|TestRestore|TestReadOnly|TestHealthyRecovered)' -count=1
git add session/workspace/shadow.go session/workspace/workspace.go session/workspace/factory.go session/instance.go session/workspace/shadow_test.go session/instance_test.go
git commit -m "feat(workspace): integrate locked automatic recovery"
```

---

### Task 9: WAL Metadata Repair and App Warning Flow

**Files:**
- Modify: `session/workspace/workspace.go`
- Modify: `session/storage.go`
- Modify: `session/storage_test.go`
- Modify: `session/instance.go`
- Modify: `app/app.go`
- Modify: `app/app_test.go`

**Interfaces:**

```go
type ApplyMetadataRepair struct { SessionID, TransactionID, AppliedTree, AppliedAt string }
```

Add `LastAppliedTransactionID string` to `ShadowData`.

- [ ] **Step 1: Add RED storage/app tests**

```go
TestLoadInstancesRepairsLastAppliedMetadataFromWAL
TestLoadInstancesWALMetadataOverridesStaleSessionMetadata
TestLoadInstancesPersistsRepairBeforeAcknowledgingTransaction
TestLoadInstancesReportsOrphanCommittedTransaction
TestLoadInstancesRestoresBlockedSessionsReadOnly
TestApplyCleanupWarningStillSavesPausesAndAcknowledges
TestApplyCleanupWarningRendersRetainedPaths
TestApplyMetadataSaveFailureLeavesCompleteWALForRecovery
TestBlockedSessionMenuDisablesApplyAndResume
```

- [ ] **Step 2: Verify RED**

```bash
go test ./session ./app -run '^(TestLoadInstances|TestApplyCleanupWarning|TestApplyMetadataSaveFailure|TestBlockedSessionMenu)' -count=1
```

- [ ] **Step 3: Implement repair and warning flow**

Load raw records, group directory sessions by project, recover, apply WAL repairs, save repaired storage, construct instances, then acknowledge matching complete transactions. Orphan commits still clean and report. App order is Apply → save metadata → acknowledge → pause → save paused state; cleanup warning does not enter failure flow.

- [ ] **Step 4: Verify and commit**

```bash
go test ./session ./app -run '^(TestLoadInstances|TestApplyCleanupWarning|TestApplyMetadataSaveFailure|TestBlockedSessionMenu)' -count=1
go test ./session ./app -count=1
git add session/workspace/workspace.go session/storage.go session/storage_test.go session/instance.go app/app.go app/app_test.go
git commit -m "feat(session): repair apply metadata from WAL"
```

---

### Task 10: Locked Reset and Blocked Transaction Preservation

**Files:**
- Modify: `main.go`
- Modify: `main_test.go`
- Modify: `session/workspace/shadow.go`
- Modify: `session/workspace/recovery.go`
- Modify: `session/workspace/shadow_test.go`

**Interfaces:**

```go
func WithResetPreflight(run func() error) error
```

- [ ] **Step 1: Add RED reset tests**

```go
TestResetHoldsProjectLockAcrossStorageAndShadowCleanup
TestResetLockContentionPreservesStorageAndProjects
TestResetPreservesRecoveryBlockedTransaction
TestResetPreservesCleanupBlockedTransaction
TestResetPreservesLegacyIncompleteManifest
TestResetRecoversOrdinaryPreCommitTransactionBeforeCleanup
TestResetAcquiresMultipleProjectLocksInSortedOrder
```

- [ ] **Step 2: Verify RED**

```bash
go test . ./session/workspace -run '^TestReset' -count=1
```

- [ ] **Step 3: Implement reset preflight**

Validate references, enumerate projects by sorted ID, acquire all locks in that order, recover safe transactions, reject blocked/manual state, then hold locks across storage/tmux/worktree/project cleanup. Release in reverse order. Never delete storage before this decision.

- [ ] **Step 4: Verify and commit**

```bash
go test . ./session/workspace -run '^TestReset' -count=1
git add main.go main_test.go session/workspace/shadow.go session/workspace/recovery.go session/workspace/shadow_test.go
git commit -m "feat(reset): preserve blocked apply transactions"
```

---

### Task 11: Deterministic Subprocess Crash Matrix

**Files:**
- Create: `session/workspace/crash_recovery_test.go`
- Modify: `session/workspace/transaction.go`
- Modify: `session/workspace/recovery.go`

**Interfaces:**

```go
type DurabilityPoint string
var durabilityCheckpoint = func(DurabilityPoint) {}
```

Stable points cover before/after manifest persistence, capture rename, publication rename, restoration, quarantine, commit, and terminal state.

- [ ] **Step 1: Add RED subprocess tests**

```go
TestCrashRecoveryForwardMatrix
TestCrashRecoveryRollbackMatrix
TestCrashRecoveryCommittedCleanupMatrix
TestCrashRecoveryTypeTransitionMatrix
TestCrashRecoveryExternalRestoreBlocker
TestCrashRecoverySourceTemporaryReplacement
TestCrashRecoveryConvergesAfterRepeatedRecoveryCrashes
TestCrashHelperProcess
```

The helper process exits with `os.Exit(86)` at `CS_TEST_CRASH_AT`; parent restarts recovery against persistent fixtures. Do not simulate crashes with returned errors or panics.

- [ ] **Step 2: Verify RED**

```bash
go test ./session/workspace -run '^TestCrashRecovery' -count=1 -timeout=10m
```

Expected: absent checkpoints/restart reconciliation expose non-recoverable phases.

- [ ] **Step 3: Add checkpoints and complete matrix**

Cover create/update/delete, regular/directory/symlink transitions, crashes during recovery itself, external blockers, and replacement source temporaries. Pre-commit converges to exact pre-Apply snapshot; post-commit converges to merged snapshot.

- [ ] **Step 4: Verify stability and commit**

```bash
go test ./session/workspace -run '^TestCrashRecovery' -count=1 -timeout=15m
go test ./session/workspace -run '^TestCrashRecovery' -count=3 -timeout=30m
git add session/workspace/crash_recovery_test.go session/workspace/transaction.go session/workspace/recovery.go
git commit -m "test(workspace): cover durable apply crash boundaries"
```

---

### Task 12: Platform Contract and Final Readiness Review

**Files:**
- Modify: `session/workspace/rename_noreplace_darwin.go`
- Modify: `session/workspace/rename_noreplace_linux.go`
- Modify: `session/workspace/rename_noreplace_unsupported.go`
- Modify: `session/workspace/platform_apply.go`
- Modify: `session/workspace/platform_apply_unix.go`
- Modify: `session/workspace/platform_apply_unsupported.go`
- Modify: `README.md`
- Review: all files changed since `1aaf057`

**Interfaces:**
- Consumes: completed WAL, recovery, lifecycle, app, and reset integration.
- Produces: a reviewed branch with explicit supported-platform and recovery documentation.

- [ ] **Step 1: Add/verify platform contract tests**

```go
TestRenameNoReplaceContractMovesIntoAbsentDestination
TestRenameNoReplaceContractPreservesExistingDestination
TestRenameNoReplaceContractSupportsFileDirectoryAndSymlink
TestAtomicApplyPlatformContract
TestUnsupportedAtomicApplyDoesNotPublishManifestOrMutateSource
```

```bash
go test ./session/workspace -run '^(TestRenameNoReplaceContract|TestAtomicApplyPlatformContract|TestUnsupportedAtomicApply)' -count=1
```

- [ ] **Step 2: Cross-build**

```bash
GOOS=darwin GOARCH=amd64 go test ./session/workspace -run '^$'
GOOS=darwin GOARCH=arm64 go test ./session/workspace -run '^$'
GOOS=linux GOARCH=amd64 go test ./session/workspace -run '^$'
GOOS=linux GOARCH=arm64 go test ./session/workspace -run '^$'
GOOS=windows GOARCH=amd64 go test ./session/workspace -run '^$'
```

Expected: all compile; Windows exposes unsupported Apply without fallback to unsafe rename.

- [ ] **Step 3: Document operational behavior**

Update README with same-filesystem/Darwin/Linux Apply requirement, operation-in-progress errors, automatic rollback, read-only blocked sessions, cleanup warnings, and manual resolution expectations. Do not change unrelated usage text.

- [ ] **Step 4: Run final verification**

```bash
go test ./session/workspace -count=1 -timeout=15m
go test ./session ./app . -count=1
go test ./... -count=1 -timeout=20m
go test -race ./session/workspace ./session ./app -count=1 -timeout=30m
go test ./session/workspace -run '^TestCrashRecovery' -count=3 -timeout=30m
go vet ./...
$(go env GOROOT)/bin/gofmt -d main.go app session keys ui
go build -o /tmp/claude-squad-ygl .
git diff --check
git status --short --branch
```

Expected: all tests/build/static checks pass; `gofmt -d` and `git diff --check` have no output; only intended documentation changes remain before commit.

- [ ] **Step 5: Commit documentation**

```bash
git add README.md
git commit -m "docs: explain durable directory apply recovery"
```

- [ ] **Step 6: Request final independent review**

Review the full recovery range from `1aaf057` through HEAD. Explicitly audit:

- every intent/rename/completion ordering pair;
- every `Remove`/`RemoveAll` reachable from Apply/recovery;
- all project-lock acquisition paths;
- manifest trust boundaries;
- pre-commit rollback versus post-commit cleanup;
- blocked-state preservation and metadata acknowledgement;
- crash-matrix and platform evidence.

Resolve every Critical/Important finding before offering integration options.
