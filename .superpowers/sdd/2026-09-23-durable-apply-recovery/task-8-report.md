# Task 8 Report

## Status

Implemented locked project recovery and read-only shadow lifecycle integration.

## TDD

RED command:

```bash
go test ./session/workspace ./session -run '^(TestNewShadowHolds|TestApplyAndResume|TestNewSessionRejected|TestRestore|TestReadOnly|TestHealthyRecovered)' -count=1
```

RED result: failed to compile because `AccessState`, `Capabilities.Resume`, and the new workspace interface methods did not exist.

GREEN command:

```bash
go test ./session/workspace ./session -run '^(TestNewShadowHolds|TestApplyAndResume|TestNewSessionRejected|TestRestore|TestReadOnly|TestHealthyRecovered)' -count=1
```

GREEN result: both packages passed.

Affected regression command:

```bash
go test ./session/workspace -run '^(TestApplyTransactionCleanMerge|TestPlanApplyUnchangedReapplyIsNoop|TestPreparedRevalidationFailure|TestApplyRejectsSymlinkedTransactionRoot|TestApplyAndResume)' -count=1
```

Result: passed.

Full repository command:

```bash
go test ./... -count=1
```

Result: passed all packages.

Diff check:

```bash
git diff --check
```

Result: clean.

## Lock scope

New shadow creation acquires one project lock before project initialization/recovery and retains it through policy loading, baseline scanning/import, branch creation, and workspace publication. Restore acquires one lock before recovery and retains it through access-state publication and worktree identity validation. Resume acquires one lock before recovery and retains it through workspace setup. Apply continues to hold one lock for its complete source-mutating pass. Private locked helpers avoid recursive acquisition.

## Disposition mapping

- Healthy and successfully recovered projects remain writable; rolled-back recovery is surfaced without disabling capabilities.
- `recovery-blocked`, `cleanup-blocked`, valid legacy transactions, invalid transactions, and retained orphans map to read-only `AccessState` with issue paths and retained paths.
- New sessions are rejected while project recovery is blocked.
- Lock contention remains the typed `OperationInProgressError`; it is not interpreted as corruption.

## Capability and restore behavior

Read-only shadow workspaces remain loadable and serializable, but publish `Apply=false` and `Resume=false`. Instance restore does not recreate a worktree or start/restore tmux for a read-only workspace. Direct Apply and Resume calls fail read-only. Healthy recovery rebuilds normal Apply/Resume capabilities immediately without process restart. Git workspaces return zero access state and acknowledge apply as a no-op.

## Self-review

Confirmed recovery remains fail-closed for invalid, legacy, and retained-orphan classification; source-mutating operations are disabled for blocked sessions; `.locks` is excluded from shadow-project cleanup enumeration; and metadata repair/reset behavior was not added.

## Concerns

`AccessState` is runtime-derived and intentionally not persisted. Task 9 still owns apply metadata repair, and Task 10 still owns reset integration.

## Fix Round 1

### Status

Resolved all lifecycle review findings. Apply and directory Resume now refresh automatic recovery and publish the resulting access state while retaining the same project lock through rejection or subsequent mutation/setup. Instance-level cached read-only checks no longer bypass that refresh.

### TDD and integration coverage

Added deterministic tests proving:

- Apply rolls back an actual interrupted transaction, publishes writable capabilities under the same lock, and immediately applies the pending worktree change.
- Apply discovers a newly invalid manifest before source mutation and publishes exact retained paths.
- Directory Resume refreshes a previously read-only restored workspace even when its worktree remains valid, preserves uncommitted worktree content, and becomes writable without restart.
- Actual `RestoreShadow`/`FromInstanceData` blocked restores neither recreate a missing worktree nor create/restore tmux.
- Recovery-blocked and cleanup-blocked manifests expose exact issue and retained paths.
- A retained orphan produces one exact stable retained path rather than duplicate issue/orphan entries.
- Automatic rollback and access-state publication reject deterministic lock probes with typed `OperationInProgressError`.

The initial RED run failed because the publication hook and refresh behavior did not exist. After implementation, focused lifecycle tests, full `session/workspace`, full `session`, and `go test ./... -count=1` pass. `git diff --check` is clean.

### Concerns

None within Task 8 scope. Task 9 metadata repair and Task 10 reset integration remain intentionally unchanged.

## Fix Round 2

### Status

Closed the remaining lifecycle evidence gaps without production changes.

### Evidence

The Apply recovery test now constructs a real durable update transaction, moves the original source into its preallocated capture, publishes the staged interrupted result, and persists the applying state. It proves automatic recovery restores the original before writable access is published, then proves the same Apply proceeds and publishes the pending worktree result.

A second deterministic test starts from an actually committed transaction with captured source material. Typed project-lock contention probes run during manifest discovery, cleanup quarantine mutation, cleaning/complete state persistence, and capability publication, proving the same project lock remains held across the entire recovery and publication interval.

### Verification

Focused lifecycle tests, full `session/workspace`, full `session`, and `go test ./... -count=1` pass. `git diff --check` is clean.

### Concerns

None. Cleanup-blocked access publication was already covered by exact read-only state tests and did not require production changes.
