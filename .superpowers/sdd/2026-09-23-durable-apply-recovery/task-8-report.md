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
