# Task 2 Report: Project Lock and Atomic Apply Preflight

## Status

Complete. Task 2 was implemented in the existing `arbitrary-directory-workspaces` worktree and committed locally. Nothing was pushed.

## RED

Command:

```bash
go test ./session/workspace -run '^(TestProjectLock|TestApplyRejectsDifferentFilesystems|TestAtomicNoReplacePreflight)' -count=1
```

Expected RED output:

```text
# claude-squad/session/workspace [claude-squad/session/workspace.test]
session/workspace/project_lock_test.go:14:16: undefined: AcquireProjectLock
session/workspace/project_lock_test.go:17:11: undefined: AcquireProjectLock
session/workspace/project_lock_test.go:18:18: undefined: OperationInProgressError
session/workspace/project_lock_test.go:24:17: undefined: AcquireProjectLock
session/workspace/project_lock_test.go:34:15: undefined: AcquireProjectLock
session/workspace/project_lock_test.go:38:11: undefined: AcquireProjectLock
session/workspace/project_lock_test.go:39:18: undefined: OperationInProgressError
session/workspace/project_lock_test.go:50:15: undefined: AcquireProjectLock
session/workspace/project_lock_test.go:64:14: undefined: filesystemDeviceID
session/workspace/project_lock_test.go:65:2: undefined: filesystemDeviceID
session/workspace/project_lock_test.go:65:2: too many errors
FAIL	claude-squad/session/workspace [build failed]
FAIL
```

This demonstrated that the required lock APIs and injectable filesystem-device preflight did not exist before implementation.

## GREEN

Command:

```bash
go test ./session/workspace -run '^(TestProjectLock|TestApplyRejectsDifferentFilesystems|TestAtomicNoReplacePreflight)' -count=1
```

Output:

```text
ok  	claude-squad/session/workspace	4.732s
```

## Implementation Details

- Added `OperationInProgressError`, `ProjectLock`, `AcquireProjectLock`, and idempotent `Close`.
- Project locks live at `shadow-projects/.locks/<project-id>.lock` and lock-file contents are never interpreted or removed as stale ownership data.
- Darwin/Linux use non-blocking `unix.Flock(fd, LOCK_EX|LOCK_NB)`; contention maps to `OperationInProgressError`.
- Unsupported platforms return explicit project-lock and atomic-apply support errors.
- Added `VerifyAtomicApplyPlatform`, `VerifySameFilesystem`, and `ProbeAtomicNoReplace`.
- Added injectable `filesystemDeviceID = platformFilesystemDeviceID` for focused device mismatch testing.
- Integrated Apply so validated sessions acquire the project lock, create/check the transaction root, verify source and transaction storage device identity, and probe no-replace rename before planning, manifest publication, or source mutation.
- Preserved the existing version 1 Apply state machine and Task 1 version 2 transaction semantics unchanged.
- Added the five focused tests prescribed by the brief.

Relevant files:

- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/project_lock.go`
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/project_lock_unix.go`
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/project_lock_unsupported.go`
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/platform_apply.go`
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/platform_apply_unix.go`
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/platform_apply_unsupported.go`
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/project_lock_test.go`
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/apply.go`

## Verification

Focused tests:

```bash
go test ./session/workspace -run '^(TestProjectLock|TestApplyRejectsDifferentFilesystems|TestAtomicNoReplacePreflight)' -count=1
```

Result: PASS (`ok claude-squad/session/workspace 4.732s`).

Workspace regression suite:

```bash
go test ./session/workspace -count=1
```

Result: PASS (`ok claude-squad/session/workspace 145.422s`).

Full repository suite:

```bash
go test ./... -count=1
```

Result: PASS for all packages, including `claude-squad/session/workspace` in 149.585s.

Race-focused verification:

```bash
go test -race ./session/workspace -run '^(TestProjectLock|TestApplyRejectsDifferentFilesystems|TestAtomicNoReplacePreflight)' -count=1
```

Result: PASS (`ok claude-squad/session/workspace 6.077s`).

Static analysis:

```bash
go vet ./session/workspace
```

Result: PASS with no output.

Cross-platform package builds:

```bash
GOOS=linux go build ./session/workspace
GOOS=windows go build ./session/workspace
```

Result: PASS with no output.

Formatting and whitespace:

```bash
git diff --check
```

Result: PASS with no output before commit.

Final status:

```bash
git status --short --branch
```

Result: clean branch (`## arbitrary-directory-workspaces`) immediately after commit.

## Self-review

- Confirmed lock contention is non-blocking and reports the attempted operation and project ID.
- Confirmed closing a lock permits immediate reacquisition and repeated/nil closes are safe.
- Confirmed existing lock-file contents are left untouched and do not affect ownership decisions.
- Confirmed preflight runs before `planApply`, so failures cannot publish a transaction manifest or mutate source content.
- Confirmed source and transaction roots are compared using platform device IDs.
- Confirmed the no-replace probe cleans up both source and destination probe paths on failure and cleans the destination on success.
- Confirmed malformed workspace data is validated before deriving or creating lock/preflight paths.
- Confirmed no Task 1 transaction schema/state changes or unrelated code changes were made.

## Commit

- SHA: `717d0bf5aeb52105b9d0996b0fd42b0f1c4d9e81`
- Message: `feat(workspace): serialize and preflight atomic apply`

## Concerns

None. The preflight intentionally creates the transaction-root directory and transient probe file before Apply planning; it does not create a transaction manifest or mutate the source. The permanent empty lock file is expected because ownership is represented solely by the OS advisory lock, never by file contents.

## Fix Round 1

### Status

Complete. Addressed both Important findings and the Minor test-coverage finding. Changes were committed locally and were not pushed.

### Covering tests

- `session/workspace/project_lock_test.go`
  - `TestProjectLockCloseIsNilSafeAndIdempotent`
  - `TestProjectLockRejectsSymlinkedLockDirectory`
  - `TestProjectLockRejectsSymlinkedLockFile`
  - `TestProbeAtomicNoReplaceExercisesCollisionAndPreservesDestination`
  - `TestApplyRejectsSymlinkedTransactionRootBeforeOutsideMutation`

### RED

Command:

```bash
/Users/yangguoliang/.local/go1.25.8/bin/gofmt -w /Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/project_lock_test.go && /Users/yangguoliang/.local/bin/go test ./session/workspace -run '^(TestProjectLockCloseIsNilSafeAndIdempotent|TestProjectLockRejectsSymlinkedLockDirectory|TestProjectLockRejectsSymlinkedLockFile|TestProbeAtomicNoReplaceExercisesCollisionAndPreservesDestination|TestApplyRejectsSymlinkedTransactionRootBeforeOutsideMutation)$' -count=1
```

Output:

```text
--- FAIL: TestProjectLockRejectsSymlinkedLockDirectory (0.00s)
    project_lock_test.go:74: Error: An error is expected but got nil.
--- FAIL: TestProjectLockRejectsSymlinkedLockFile (0.00s)
    project_lock_test.go:87: Error: An error is expected but got nil.
--- FAIL: TestProbeAtomicNoReplaceExercisesCollisionAndPreservesDestination (0.00s)
    project_lock_test.go:111: Error: Not equal: expected: 2 actual: 1
--- FAIL: TestApplyRejectsSymlinkedTransactionRootBeforeOutsideMutation (2.63s)
    project_lock_test.go:163: Error: Should be false
    Messages: preflight must reject the symlink before creating probe artifacts
FAIL
FAIL    claude-squad/session/workspace    3.325s
FAIL
```

The nil/repeated Close test already passed; the four defect tests failed for the reviewed reasons.

### GREEN

Command:

```bash
/Users/yangguoliang/.local/bin/go test ./session/workspace -run '^(TestProjectLockCloseIsNilSafeAndIdempotent|TestProjectLockRejectsSymlinkedLockDirectory|TestProjectLockRejectsSymlinkedLockFile|TestProbeAtomicNoReplaceExercisesCollisionAndPreservesDestination|TestApplyRejectsSymlinkedTransactionRootBeforeOutsideMutation)$' -count=1
```

Output:

```text
ok  claude-squad/session/workspace  2.635s
```

### Implementation

- The no-replace probe now first proves rename to an absent destination, then creates both collision operands exclusively, verifies collision refusal, verifies the destination bytes remain unchanged, and removes only paths it explicitly created.
- Lock acquisition validates `.locks` and the lock path under the canonical shadow-projects root. Unix opens the lock file with `O_NOFOLLOW`, closing the final-component substitution window while retaining non-blocking `flock` behavior.
- Apply validates the transaction root before `MkdirAll`, validates it again after creation, and only then performs filesystem and rename probes.
- Unsupported-platform lock behavior and existing Apply v1 logic remain unchanged.

### Regression verification

Focused Task 2 suite:

```bash
/Users/yangguoliang/.local/bin/go test ./session/workspace -run '^(TestProjectLock|TestProbeAtomicNoReplace|TestApplyRejectsDifferentFilesystems|TestApplyRejectsSymlinkedTransactionRoot|TestAtomicNoReplacePreflight)' -count=1
```

```text
ok  claude-squad/session/workspace  6.469s
```

Workspace suite:

```bash
/Users/yangguoliang/.local/bin/go test ./session/workspace -count=1
```

```text
ok  claude-squad/session/workspace  152.590s
```

Full repository suite:

```bash
/Users/yangguoliang/.local/bin/go test ./... -count=1
```

```text
PASS: all packages; claude-squad/session/workspace completed in 152.796s
```

Race-focused suite:

```bash
/Users/yangguoliang/.local/bin/go test -race ./session/workspace -run '^(TestProjectLock|TestProbeAtomicNoReplace|TestApplyRejectsDifferentFilesystems|TestApplyRejectsSymlinkedTransactionRoot|TestAtomicNoReplacePreflight)' -count=1
```

```text
ok  claude-squad/session/workspace  8.390s
```

Additional checks:

```bash
/Users/yangguoliang/.local/bin/go vet ./session/workspace
GOOS=linux /Users/yangguoliang/.local/bin/go build ./session/workspace
GOOS=windows /Users/yangguoliang/.local/bin/go build ./session/workspace
git diff --check
```

Result: PASS with no output.

### Self-review

- Confirmed no cleanup path removes the absent, unowned destination when the first rename fails.
- Confirmed collision operands are exclusively created and destination contents are checked before owned cleanup.
- Confirmed symlinked `.locks`, lock files, and transaction roots are rejected before redirected side effects; Unix lock-file open also uses `O_NOFOLLOW`.
- Confirmed repeated and nil `Close` calls are harmless.
- Confirmed the project lock remains non-blocking and spans the entire Apply operation.
- Confirmed no transaction schema, recovery state, or v1 Apply behavior changed.

### Commit

- SHA: `a70828e`
- Message: `fix(workspace): harden apply preflight paths`

### Concerns

None.

## Fix Round 2

### Status

Complete. Closed the remaining pathname-substitution races on Darwin/Linux with descriptor-relative lock and probe operations. Exported Task 2 interfaces, non-blocking flock, same-filesystem rejection, unsupported-platform fail-before-side-effect behavior, and Apply v1 state logic remain intact.

### Covering test files

- `session/workspace/project_lock_test.go`
  - `TestProjectLockHandlesLockDirectorySubstitutionWithoutAliasEscape`
  - `TestApplyRejectsTransactionRootSubstitutionBeforeProbe`
- `session/workspace/apply_test.go`
  - `TestApplyNoReplacePreflightFailsBeforeSourceMutation` (updated to inject the descriptor-relative rename seam)

### RED

Command:

```bash
/Users/yangguoliang/.local/go1.25.8/bin/gofmt -w /Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/project_lock_test.go && cd /Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces && /Users/yangguoliang/.local/bin/go test ./session/workspace -run '^(TestProjectLockRejectsLockDirectorySubstitutionBeforeOpen|TestApplyRejectsTransactionRootSubstitutionBeforeProbe)$' -count=1
```

Output:

```text
# claude-squad/session/workspace [claude-squad/session/workspace.test]
session/workspace/project_lock_test.go:98:14: undefined: projectLockAfterDirectoryOpenHook
session/workspace/project_lock_test.go:99:2: undefined: projectLockAfterDirectoryOpenHook
session/workspace/project_lock_test.go:103:21: undefined: projectLockAfterDirectoryOpenHook
session/workspace/project_lock_test.go:202:14: undefined: atomicApplyAfterTransactionRootOpenHook
session/workspace/project_lock_test.go:203:2: undefined: atomicApplyAfterTransactionRootOpenHook
session/workspace/project_lock_test.go:207:21: undefined: atomicApplyAfterTransactionRootOpenHook
FAIL    claude-squad/session/workspace [build failed]
FAIL
```

The absent deterministic seams proved the old implementation could not exercise substitution exactly between validation and pathname use. The tests were then finalized around the actual post-verification/pre-use seam names and lock-safe acceptance behavior.

### GREEN

Command:

```bash
cd /Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces && /Users/yangguoliang/.local/bin/go test ./session/workspace -run '^(TestProjectLockHandlesLockDirectorySubstitutionWithoutAliasEscape|TestApplyRejectsTransactionRootSubstitutionBeforeProbe)$' -count=1
```

Output:

```text
ok  claude-squad/session/workspace  3.205s
```

### Implementation and containment reasoning

- `.locks` is opened relative to a no-follow shadow-projects directory descriptor. The lock file is opened with `openat(..., O_NOFOLLOW)` relative to that stable `.locks` descriptor.
- A per-project coordinator file is opened relative to the stable shadow-projects descriptor and carries the non-blocking `flock`. Therefore, if `.locks` is renamed after verification, the in-flight operation may safely create the compatibility lock file in the renamed directory, but a replacement `.locks` cannot redirect that side effect outside and cannot create a second serialization domain. The deterministic test proves both no outside lock creation and continued contention through the stable coordinator.
- The transaction root is opened/created with `openat`/`mkdirat`, verified with `fstatat(..., AT_SYMLINK_NOFOLLOW)` against `fstat`, and all probe creation, reads, cleanup, and no-replace renames use that stable directory descriptor. If the directory is renamed after verification, the probe remains confined to the opened directory; Apply rejects the substituted pathname before planning, and the test proves neither the outside target nor the renamed directory retains probe artifacts.
- Descriptor-relative no-replace uses `renameat2(..., RENAME_NOREPLACE)` on Linux and `renameatx_np(..., RENAME_EXCL)` on Darwin.
- Unsupported platforms reject locking/preflight before opening or creating lock/probe paths.

### Regression evidence

Focused Task 2 suite:

```bash
/Users/yangguoliang/.local/bin/go test ./session/workspace -run '^(TestProjectLock|TestProbeAtomicNoReplace|TestApplyRejectsDifferentFilesystems|TestApplyRejectsSymlinkedTransactionRoot|TestApplyRejectsTransactionRootSubstitution|TestAtomicNoReplacePreflight|TestApplyNoReplacePreflight)' -count=1
```

```text
ok  claude-squad/session/workspace  11.731s
```

Race-focused suite:

```bash
/Users/yangguoliang/.local/bin/go test -race ./session/workspace -run '^(TestProjectLock|TestProbeAtomicNoReplace|TestApplyRejectsDifferentFilesystems|TestApplyRejectsSymlinkedTransactionRoot|TestApplyRejectsTransactionRootSubstitution|TestAtomicNoReplacePreflight|TestApplyNoReplacePreflight)' -count=1
```

```text
ok  claude-squad/session/workspace  13.393s
```

Workspace suite:

```bash
/Users/yangguoliang/.local/bin/go test ./session/workspace -count=1
```

```text
ok  claude-squad/session/workspace  160.249s
```

Full repository suite:

```bash
/Users/yangguoliang/.local/bin/go test ./... -count=1
```

```text
PASS: all packages; claude-squad/session/workspace completed in 163.783s
```

Additional checks:

```bash
/Users/yangguoliang/.local/bin/go vet ./session/workspace
GOOS=linux /Users/yangguoliang/.local/bin/go build ./session/workspace
GOOS=windows /Users/yangguoliang/.local/bin/go build ./session/workspace
git diff --check
```

Result: PASS with no output.

### Self-review

- Confirmed no lock/probe descendant operation after verification resolves through the replaceable `.locks` or transaction-root pathname.
- Confirmed the stable per-project coordinator prevents renamed/replaced `.locks` directories from becoming independent lock aliases.
- Confirmed transaction-root identity substitution is rejected before Apply planning while probe operations remain confined to the stable descriptor.
- Confirmed no-follow handling covers lock directory, lock file, transaction root, and probe opens.
- Confirmed source/transaction device comparison uses the opened transaction directory identity.
- Confirmed unsupported platforms return before lock/probe filesystem side effects.
- Confirmed no recovery implementation, WAL schema, or Apply v1 state transition was broadened.

### Commit

- SHA: `1703ccc`
- Message: `fix(workspace): close apply path substitution races`

### Concerns

A renamed `.locks` directory can retain the compatibility `<project-id>.lock` file opened through its stable descriptor. Serialization remains coherent because ownership is exclusively enforced by the per-project coordinator under the stable shadow-projects directory; lock-file contents and pathname presence are not ownership signals.

## Fix Round 3

### Status

Complete. Closed the two remaining Important findings without broadening into recovery or version 2 Apply integration. Changes are ready for a new local commit and will not be pushed.

### Covering test file

- `session/workspace/project_lock_test.go`
  - `TestProjectLockCoordinatorReplacementCannotSplitSerialization`
  - `TestProjectLocksForDistinctProjectsRemainIndependent`
  - `TestAtomicApplyPreflightRejectsTransactionRootSubstitutionAfterProbe`
  - `TestApplyRejectsTransactionRootSubstitutionBeforePlanning`

### RED

Command:

```bash
/Users/yangguoliang/.local/go1.25.8/bin/gofmt -w "/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/project_lock_test.go" && /Users/yangguoliang/.local/bin/go test ./session/workspace -run '^(TestProjectLockCoordinatorReplacementCannotSplitSerialization|TestProjectLocksForDistinctProjectsRemainIndependent|TestAtomicApplyPreflightRejectsTransactionRootSubstitutionAfterProbe|TestApplyRejectsTransactionRootSubstitutionBeforePlanning)$' -count=1
```

Output:

```text
--- FAIL: TestProjectLockCoordinatorReplacementCannotSplitSerialization (0.01s)
    project_lock_test.go:105:
        Error Trace: /Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/project_lock_test.go:105
        Error:       Should be in error chain:
                     expected: %!q(**workspace.OperationInProgressError=0xc00009a148)
                     in chain:
        Test:        TestProjectLockCoordinatorReplacementCannotSplitSerialization
        Messages:    replacing the coordinator pathname must not create a second lock domain
--- FAIL: TestAtomicApplyPreflightRejectsTransactionRootSubstitutionAfterProbe (0.00s)
    project_lock_test.go:247:
        Error Trace: /Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/project_lock_test.go:247
        Error:       An error is expected but got nil.
        Test:        TestAtomicApplyPreflightRejectsTransactionRootSubstitutionAfterProbe
--- FAIL: TestApplyRejectsTransactionRootSubstitutionBeforePlanning (3.02s)
    project_lock_test.go:284:
        Error Trace: /Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/project_lock_test.go:284
        Error:       Error "shadow transaction root \"/private/var/folders/0l/hwzrbkfd01b_yxsdw3rxz6yh0000gn/T/TestApplyRejectsTransactionRootSubstitutionBeforePlanning431264186/001/.claude-squad/shadow-projects/7b041c4aa84188bb0f268a067e9f4e4e84a4fdaf6a03ee8df59314ce36c31301/transactions\" is a symlink" does not contain "transaction root changed"
        Test:        TestApplyRejectsTransactionRootSubstitutionBeforePlanning
FAIL
FAIL    claude-squad/session/workspace    3.765s
FAIL
```

This proved coordinator replacement split the lock domain, direct preflight returned success after transaction-root substitution, and Apply reached planning before a later pathname check rejected the substitution. The distinct-project independence test passed in RED.

### GREEN

Command:

```bash
/Users/yangguoliang/.local/bin/go test ./session/workspace -run '^(TestProjectLockCoordinatorReplacementCannotSplitSerialization|TestProjectLocksForDistinctProjectsRemainIndependent|TestAtomicApplyPreflightRejectsTransactionRootSubstitutionAfterProbe|TestApplyRejectsTransactionRootSubstitutionBeforePlanning)$' -count=1
```

Output:

```text
ok  	claude-squad/session/workspace	3.423s
```

### Implementation and rationale

- Project ownership now uses non-blocking `flock` on the opened, validated project directory descriptor. Renaming, unlinking, or recreating the legacy coordinator pathname cannot produce another inode in the serialization domain while the canonical project directory remains the same.
- The compatibility `.locks/<project-id>.lock` file and legacy coordinator entry are still created with descriptor-relative, no-follow operations, but neither mutable regular-file pathname determines lock ownership.
- Each project directory has its own inode, so distinct projects remain independently lockable.
- Atomic Apply preflight now runs both no-replace probes through the stable transaction-directory descriptor and then immediately re-verifies that the canonical transaction-root entry still identifies that descriptor before returning success.
- A test-only plan-start seam proves `planApply` is not entered when final preflight identity verification fails; the source snapshot and both substituted directories remain unchanged and free of probe artifacts.
- All opened descriptors are closed on success, contention, and intermediate failure paths.

### Regression evidence

Focused Task 2 suite:

```bash
/Users/yangguoliang/.local/bin/go test ./session/workspace -run '^(TestProjectLock|TestProbeAtomicNoReplace|TestApplyRejectsDifferentFilesystems|TestApplyRejectsSymlinkedTransactionRoot|TestApplyRejectsTransactionRootSubstitution|TestAtomicApplyPreflight|TestApplyNoReplacePreflight)' -count=1
```

```text
ok  	claude-squad/session/workspace	10.571s
```

Race-focused suite:

```bash
/Users/yangguoliang/.local/bin/go test -race ./session/workspace -run '^(TestProjectLock|TestProbeAtomicNoReplace|TestApplyRejectsDifferentFilesystems|TestApplyRejectsSymlinkedTransactionRoot|TestApplyRejectsTransactionRootSubstitution|TestAtomicApplyPreflight|TestApplyNoReplacePreflight)' -count=1
```

```text
ok  	claude-squad/session/workspace	13.063s
```

Workspace suite:

```bash
/Users/yangguoliang/.local/bin/go test ./session/workspace -count=1
```

```text
ok  	claude-squad/session/workspace	161.642s
```

Full repository suite:

```bash
/Users/yangguoliang/.local/bin/go test ./... -count=1
```

```text
ok  	claude-squad	0.778s
ok  	claude-squad/app	31.411s
?   	claude-squad/cmd	[no test files]
?   	claude-squad/cmd/cmd_test	[no test files]
ok  	claude-squad/config	5.545s
?   	claude-squad/daemon	[no test files]
?   	claude-squad/keys	[no test files]
?   	claude-squad/log	[no test files]
ok  	claude-squad/session	9.950s
ok  	claude-squad/session/git	2.532s
ok  	claude-squad/session/tmux	2.279s
ok  	claude-squad/session/workspace	170.598s
ok  	claude-squad/ui	9.143s
?   	claude-squad/ui/overlay	[no test files]
```

Additional checks:

```bash
/Users/yangguoliang/.local/bin/go vet ./session/workspace
GOOS=linux /Users/yangguoliang/.local/bin/go build ./session/workspace
GOOS=windows /Users/yangguoliang/.local/bin/go build ./session/workspace
git diff --check
```

Result: PASS with no output. The focused Darwin tests exercise real `flock` on directory descriptors; the Linux build verifies the shared Unix implementation compiles, and the Windows build preserves unsupported-platform behavior.

### Self-review

- Confirmed mutable coordinator and compatibility-file inodes no longer define ownership.
- Confirmed a replaced coordinator entry still yields typed, non-blocking contention for the same project.
- Confirmed two distinct project directory inodes do not contend.
- Confirmed final transaction-root identity verification occurs after every test seam and probe and immediately before successful preflight return.
- Confirmed direct preflight fails and `Apply` does not enter `planApply` or mutate source data after substitution.
- Confirmed descriptor-relative descendant side effects and cleanup remain unchanged.
- Confirmed all descriptors close on all paths and unsupported-platform behavior still builds.
- Confirmed no recovery, WAL, or version 2 Apply behavior was added.

### Commit

- SHA: `6274fee`
- Message: `fix(workspace): preserve apply lock identity`
- Local commit only; not pushed.

### Concerns

The legacy coordinator pathname remains as a compatibility artifact but is intentionally not authoritative. Serialization relies on advisory directory locking, verified by real Darwin tests and the shared Darwin/Linux implementation build; filesystems that do not honor `flock` semantics remain outside the platform contract already required by Task 2.

## Fix Round 4

### Status

Complete. Moved authoritative ownership to the required per-project files in `.locks`, anchored the `.locks` inode to the opened trusted `shadow-projects` root, and retained non-blocking independent project locks. No recovery/v2 integration was added.

### RED

Command:

```bash
/Users/yangguoliang/.local/go1.25.8/bin/gofmt -w "/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/project_lock_test.go" && /Users/yangguoliang/.local/bin/go test ./session/workspace -run '^(TestProjectDirectoryReplacementCannotSplitSerialization|TestProjectAndLockAnchorReplacementCannotSplitSerialization|TestProjectLockRejectsInvalidProjectIDBeforePathUse|TestProjectLocksForDistinctProjectsRemainIndependent)$' -count=1
```

Output:

```text
--- FAIL: TestProjectDirectoryReplacementCannotSplitSerialization (0.01s)
    project_lock_test.go:123: replacing the canonical project directory must not create a second lock domain
--- FAIL: TestProjectAndLockAnchorReplacementCannotSplitSerialization (0.00s)
    project_lock_test.go:141: An error is expected but got nil.
--- FAIL: TestProjectLockRejectsInvalidProjectIDBeforePathUse (0.00s)
    project_lock_test.go:156: An error is expected but got nil.
FAIL
FAIL    claude-squad/session/workspace    0.697s
FAIL
```

The existing implementation locked the replaceable project-directory inode, accepted a replacement `.locks` inode, and used an unvalidated basename as the project ID. Distinct-project independence passed in RED.

### GREEN

Command:

```bash
/Users/yangguoliang/.local/bin/go test ./session/workspace -run '^(TestProjectDirectoryReplacementCannotSplitSerialization|TestProjectAndLockAnchorReplacementCannotSplitSerialization|TestProjectLockRejectsInvalidProjectIDBeforePathUse|TestProjectLocksForDistinctProjectsRemainIndependent)$' -count=1
```

Output:

```text
ok  claude-squad/session/workspace  0.610s
```

### Implementation and trust boundary

- The already validated/opened `shadow-projects` directory descriptor is the trust anchor. `.locks` is opened/created with no-follow `openat`/`mkdirat`, and `<project-id>.lock` is opened no-follow relative to that descriptor.
- On first use, the trusted root receives an immutable-by-protocol extended attribute containing the `.locks` device/inode identity. Initialization uses create-only xattr semantics, so concurrent initializers cannot overwrite the winner. Every later acquisition compares the opened `.locks` descriptor identity with that root-held identity and fails closed if the pathname was replaced.
- Consequently, a renamed/replaced canonical project directory is irrelevant to locking, while a renamed/replaced `.locks` path cannot establish a new domain. In-flight operations use their stable descriptor and perform final root-relative `.locks` identity checks before and after flock; future acquisitions reject a replacement anchor. Restoring the original anchor permits reacquisition.
- The authoritative non-blocking flock is on `.locks/<project-id>.lock`; distinct IDs remain separate lock inodes and do not serialize each other. The legacy root coordinator file is retained only as a compatibility artifact.
- Project IDs are rejected before any directory creation/path use when empty, dot entries, reserved `.locks`, or non-basename values are supplied.

### Tests and regressions

- Added `TestProjectDirectoryReplacementCannotSplitSerialization`.
- Added `TestProjectAndLockAnchorReplacementCannotSplitSerialization`.
- Added `TestProjectLockRejectsInvalidProjectIDBeforePathUse`.
- Preserved release/reacquire assertions and `TestProjectLocksForDistinctProjectsRemainIndependent`.
- Updated the prior `.locks` substitution test to require fail-closed behavior and restoration of the authoritative anchor.

Verification:

```text
focused Task 2: PASS (12.397s)
race project locks: PASS (1.727s)
workspace suite: PASS (163.527s)
full repository: PASS; workspace 182.107s
vet, git diff --check: PASS
GOOS=darwin and GOOS=linux workspace builds: PASS
```

### Self-review

- Confirmed project-directory replacement before a second acquisition still returns typed non-blocking contention.
- Confirmed simultaneous project and `.locks` replacement cannot yield a successful concurrent acquisition.
- Confirmed root-relative no-follow access, final anchor identity checks, and xattr create-only initialization prevent future anchor replacement from becoming authoritative.
- Confirmed each project ID keeps its own lock inode; there is no trusted-root-wide operation lock.
- Confirmed compatibility lock/coordinator artifacts are not deleted or PID/staleness interpreted.
- Confirmed unsupported platforms still fail before filesystem side effects and both Darwin/Linux compile.

### Commit

Pending new local commit.

### Concerns

The trusted `shadow-projects` root must support persistent extended attributes. Failure to read or create the anchor identity fails closed rather than weakening or globally serializing the lock domain.

## Fix Round 5

### Status

Complete. Removed the out-of-contract xattr anchor and legacy coordinator artifact. The authoritative lock is exactly `shadow-projects/.locks/<project-id>.lock`, opened descriptor-relatively with no-follow semantics and locked using non-blocking `flock`.

### RED

Command:

```bash
/Users/yangguoliang/.local/go1.25.8/bin/gofmt -w "/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/project_lock_test.go" && /Users/yangguoliang/.local/bin/go test ./session/workspace -run '^(TestProjectLockDoesNotRequireXattrSupport|TestProjectLockCreatesOnlyAuthoritativeLockArtifact)$' -count=1
```

Output:

```text
# claude-squad/session/workspace [claude-squad/session/workspace.test]
session/workspace/project_lock_test.go:117:14: undefined: projectLockSetXattr
session/workspace/project_lock_test.go:118:2: undefined: projectLockSetXattr
session/workspace/project_lock_test.go:119:21: undefined: projectLockSetXattr
FAIL    claude-squad/session/workspace [build failed]
FAIL
```

An earlier concrete RED run also showed the legacy artifact directly:

```text
--- FAIL: TestProjectLockCreatesOnlyAuthoritativeLockArtifact (0.00s)
    project_lock_test.go:135: "[d .locks/ - .project.lock-coordinator]" should have 1 item(s), but has 2
FAIL
```

### GREEN

Command:

```bash
/Users/yangguoliang/.local/go1.25.8/bin/gofmt -w "/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/project_lock.go" "/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/project_lock_unix.go" "/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/project_lock_unsupported.go" "/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/project_lock_test.go" && /Users/yangguoliang/.local/bin/go test ./session/workspace -run '^(TestProjectLockDoesNotRequireXattrSupport|TestProjectLockCreatesOnlyAuthoritativeLockArtifact)$' -count=1
```

Output:

```text
ok  claude-squad/session/workspace  0.718s
```

### Implementation and trust boundary

- Removed every production xattr read/write and inode-pinning prerequisite. A test seam that is never consulted by acquisition injects an xattr failure and proves locking is independent of xattr support.
- Removed the root-level `<project-id>.lock-coordinator` artifact and unused coordinator state/cleanup.
- `.locks/<validated-project-id>.lock` is the sole authoritative flock target. It is opened with `openat` relative to the stable `.locks` descriptor using `O_NOFOLLOW`, then receives `LOCK_EX|LOCK_NB`.
- Static symlinked `.locks` and lock-file entries still fail closed. If `.locks` is substituted during an in-flight acquisition, the stable descriptor confines the lock-file open to the originally opened directory; final root-relative identity verification rejects the changed canonical entry and no outside target is touched.
- The canonical project directory is not part of lock identity, so replacing it cannot split the per-project lock.
- The `.locks` metadata area is a trusted boundary for cooperative Claude Squad processes. This implementation intentionally does not claim protection against a same-credential adversary replacing the authoritative lock inode after acquisition; that guarantee is not enforceable with the specified POSIX advisory lock contract.
- Descriptor-relative transaction probes and final transaction-root identity revalidation were retained unchanged.

### Tests and regressions

Focused Task 2:

```bash
/Users/yangguoliang/.local/bin/go test ./session/workspace -run '^(TestProjectLock|TestProbeAtomicNoReplace|TestApplyRejectsDifferentFilesystems|TestApplyRejectsSymlinkedTransactionRoot|TestApplyRejectsTransactionRootSubstitution|TestAtomicApplyPreflight|TestAtomicNoReplacePreflight|TestApplyNoReplacePreflight)' -count=1
```

```text
ok  claude-squad/session/workspace  13.778s
```

Race-focused:

```bash
/Users/yangguoliang/.local/bin/go test -race ./session/workspace -run '^(TestProjectLock|TestProbeAtomicNoReplace|TestApplyRejectsDifferentFilesystems|TestApplyRejectsSymlinkedTransactionRoot|TestApplyRejectsTransactionRootSubstitution|TestAtomicApplyPreflight|TestAtomicNoReplacePreflight|TestApplyNoReplacePreflight)' -count=1
```

```text
ok  claude-squad/session/workspace  15.092s
```

Workspace suite:

```bash
/Users/yangguoliang/.local/bin/go test ./session/workspace -count=1
```

```text
ok  claude-squad/session/workspace  165.545s
```

Full repository:

```bash
/Users/yangguoliang/.local/bin/go test ./... -count=1
```

```text
PASS: all packages; claude-squad/session/workspace completed in 175.402s
```

Platform/static checks:

```bash
GOOS=linux /Users/yangguoliang/.local/bin/go build ./session/workspace
GOOS=windows /Users/yangguoliang/.local/bin/go build ./session/workspace
/Users/yangguoliang/.local/bin/go vet ./session/workspace
git diff --check
```

Output: no output; all passed. Darwin behavior was exercised by the native focused, race, workspace, and full suites. The Linux build passed. The unsupported Windows build confirms the early platform guard compiles; `AcquireProjectLock` invokes it before path validation or filesystem creation, preserving fail-before-side-effect behavior.

### Self-review

- Confirmed exact per-project non-blocking typed contention, release/reacquire, stale-content indifference, and distinct-project independence remain covered.
- Confirmed canonical project-directory replacement does not split locking.
- Confirmed static lock-directory/lock-file symlinks reject and in-flight `.locks` substitution cannot redirect the operation outside.
- Confirmed no xattr production calls, coordinator artifacts, or extra lock files remain.
- Confirmed transaction preflight implementation was not changed.
- Confirmed no test claims protection from hostile replacement of the authoritative lock inode.

### Commit

New local commit: `fix(workspace): align project lock with filesystem contract`. Not pushed.

### Concerns

None beyond the explicit trusted lock-metadata boundary described above.
