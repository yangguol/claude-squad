# Task 10 Report: Locked Reset and Blocked Transaction Preservation

## Status

Implemented and verified.

## RED

Command:

```bash
go test . ./session/workspace -run '^TestReset' -count=1
```

Failed as expected before implementation because `WithResetPreflight` and the reset lock-order observation hook did not exist:

```text
session/workspace/shadow_test.go:22:9: undefined: WithResetPreflight
...
session/workspace/shadow_test.go:115:2: undefined: resetProjectLockedHook
```

The seven required reset tests were added:

- `TestResetHoldsProjectLockAcrossStorageAndShadowCleanup`
- `TestResetLockContentionPreservesStorageAndProjects`
- `TestResetPreservesRecoveryBlockedTransaction`
- `TestResetPreservesCleanupBlockedTransaction`
- `TestResetPreservesLegacyIncompleteManifest`
- `TestResetRecoversOrdinaryPreCommitTransactionBeforeCleanup`
- `TestResetAcquiresMultipleProjectLocksInSortedOrder`

## GREEN

Focused reset tests:

```text
$ go test . ./session/workspace -run '^TestReset' -count=1
ok  claude-squad
ok  claude-squad/session/workspace
```

Affected packages:

```text
$ go test . ./session ./session/workspace -count=1
ok  claude-squad
ok  claude-squad/session
ok  claude-squad/session/workspace
```

Full repository:

```text
$ go test ./... -count=1
```

All packages passed; packages without tests reported `[no test files]`.

Diff validation:

```text
$ git diff --check
```

Passed with no output.

## Implementation and lock scope

`workspace.WithResetPreflight` now validates persisted references before mutation, enumerates every valid shadow project, sorts canonical project paths, and acquires every distinct project lock in that order. If any acquisition fails, the callback is not invoked and already acquired locks are released in reverse order.

After all locks are held, each project is metadata-validated and passed through the existing locked recovery implementation. Safe ordinary pre-commit transactions are recovered before cleanup. Any report with blocked/manual access state aborts reset before storage, tmux, native worktree, shadow-project, or daemon cleanup.

`main.go` wraps the complete destructive reset sequence in this preflight callback. Therefore locks remain held across storage deletion, tmux cleanup, native Git worktree cleanup, shadow cleanup, and daemon stop, and are released in reverse order afterward. Git-only behavior and cleanup ordering remain otherwise unchanged.

## Blocked preservation

Recovery-blocked and cleanup-blocked manifests cause preflight failure before the destructive callback. Legacy/invalid transaction discovery and retained-orphan reports also produce read-only/manual recovery state and abort reset. Their transaction material and project roots are not recursively removed. Lock contention likewise aborts before any reset mutation.

## Self-review

- Confirmed validation and all-lock acquisition precede recovery or destructive callback work.
- Confirmed deterministic canonical-path acquisition and reverse release.
- Confirmed partial lock acquisition unwinds through the deferred release path.
- Confirmed blocked-state errors cannot reach storage deletion or shadow cleanup.
- Confirmed existing recovery code is reused rather than duplicated.
- Confirmed no Task 11 crash-matrix work was included.

## Commit

`b692569` — `feat(reset): preserve blocked apply transactions`

## Concerns

None identified. The test-only lock hook is package-private and reset-specific.

## Fix Round 1

### Status

Implemented and verified.

### RED

Added deterministic regressions for project/reference insertion after enumeration, insertion immediately before locked-set cleanup, globally mixed recoverable and blocked projects, later-sorted lock contention with persisted storage and transaction material, and the real reset sequence. The first focused run failed because the new race hooks and validated-set cleanup wiring did not exist.

### Changes

- Reset now re-reads persisted references and re-enumerates canonical shadow projects after acquiring the sorted lock snapshot, aborting before recovery when either set changed.
- A read-only transaction classification mode leaves pre-manifest orphans untouched. Every locked project is strictly classified before any recovery starts, so invalid, legacy, blocked, retained-orphan, or manual state prevents all recovery mutation.
- Main reset now receives a shadow cleanup closure bound to the validated locked project set. The closure rechecks the set immediately before deletion and never independently deletes newly appearing projects.
- The injectable `runResetSequence` is the sequence used by main reset; its test proves project locks remain held through storage deletion, tmux cleanup, native Git worktree cleanup, shadow cleanup, and daemon stop.
- Contention coverage now uses two projects, contention on the later sorted lock, persisted state, and prepared transactions, and proves callback suppression, byte/state preservation, and release of the earlier lock.

### Verification

```text
$ go test . ./session/workspace -run '^TestReset|^TestRunReset' -count=1
ok  claude-squad
ok  claude-squad/session/workspace

$ go test . ./session -count=1
ok  claude-squad
ok  claude-squad/session

$ go test ./session/workspace -count=1
ok  claude-squad/session/workspace

$ go test ./... -count=1
all packages passed

$ git diff --check
passed
```

### Concerns

Reset remains cooperative: a non-cooperating writer can create a project after the final set check. The validated cleanup closure detects such an insertion before project deletion and fails safely, preserving the new project; cooperative project creators remain serialized by project locks.

## Fix Round 2

### Status

Implemented and verified.

### RED

Added deterministic sorted two-project regressions for rollback restoration occupancy and committed-cleanup identity mismatch. Before implementation both tests reached mutating recovery, changed the earlier project's manifest/source, and only then discovered the later conflict; the initial focused run also showed reset errors omitted issue and retained paths.

### Changes

- Added side-effect-free transaction recovery preflight for prepared, applying, rolling-back, committed, and cleaning states.
- Rollback preflight reuses the rollback entry reconciliation logic with manifest persistence, rename, quarantine, and deletion disabled while simulating successful phase transitions needed by later decisions.
- Committed cleanup preflight shares descriptor-bound capture/quarantine identity checks with mutating cleanup.
- Reset runs recovery feasibility for every transaction in every locked project before any recovery mutation and reports the exact issue paths and retained paths on failure.
- Tests prove a later external restoration occupant or cleanup identity mismatch aborts reset while the earlier recoverable project's source tree and manifest bytes remain unchanged.

### Verification

```text
$ go test . ./session/workspace -run '^TestReset|^TestRunReset' -count=1
ok  claude-squad
ok  claude-squad/session/workspace

$ go test . ./session ./session/workspace -count=1
ok  claude-squad
ok  claude-squad/session
ok  claude-squad/session/workspace

$ go test ./... -count=1
all packages passed

$ git diff --check
passed
```

### Concerns

None identified.

## Fix Round 3

### Status

Implemented and verified.

### RED

Added `TestResetPreflightAcceptsPublishedUpdateAndCreateRollback`, which constructs one applying transaction with both a published update and a published create. The initial run failed with `RecoveryBlocked`: preflight simulated moving each published result to its rollback capture, but private retention then queried the absent real capture path and reported that the retained result changed.

### Changes

- Rollback reconciliation now records an explicit simulated rollback-capture location and the descriptor-bound identity observed at the real published target.
- Later private-retention validation consumes that virtual observation during preflight; mutating rollback continues to validate the real rollback capture through the same reconciliation path.
- The deterministic regression proves direct preflight passes without changing source, transaction tree, or manifest bytes; reset then performs real capture, restoration/removal, private retention, reaches `TransactionRolledBack`, and enters cleanup.
- Existing restoration-occupancy and committed-cleanup conflict tests remain unchanged and passing, preserving global zero-mutation behavior when preflight rejects reset.

### Verification

```text
$ go test . ./session/workspace -run '^TestReset|^TestRunReset' -count=1
ok  claude-squad
ok  claude-squad/session/workspace

$ go test ./session/workspace -count=1
ok  claude-squad/session/workspace

$ go test . -count=1
ok  claude-squad

$ go test ./session -count=1
ok  claude-squad/session

$ go test ./... -count=1
all packages passed

$ git diff --check
passed
```

### Concerns

None identified.
