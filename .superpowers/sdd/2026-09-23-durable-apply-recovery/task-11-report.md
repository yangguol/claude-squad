# Task 11 Report: Deterministic Subprocess Crash Matrix

## Status

Implemented and verified.

## Methodology

Every crash case launches the compiled Go test binary as a subprocess. The helper installs the package-private `durabilityCheckpoint`, exits with `os.Exit(86)` exactly at `CS_TEST_CRASH_AT`, and first writes only the fixture locator/snapshot needed by the parent. A fresh subprocess then reads the manifest and filesystem from disk with `ReadManifestStrict` and calls `RecoverTransaction`; it receives no in-memory manifest or hook state from the crashed process.

The matrix covers manifest before/after persistence, forward capture/publication/pre-commit/commit, rollback capture/restoration/terminal state, cleanup quarantine/removal/terminal state, create/update/delete, regular/symlink/nested-created-parent transitions, repeated crashes during recovery, external restoration blockers, and replacement source temporaries.

## RED

```text
$ go test ./session/workspace -run '^TestCrashRecovery' -count=1 -timeout=10m
undefined: durabilityCheckpoint
undefined: DurabilityPoint
FAIL
```

After adding the real-exit matrix, its first execution exposed two restart validation bugs: a create legitimately enters rollback phases that `phaseAllowed` rejected, and a restored created parent may have no ownership identity when it was never published. The focused run failed from `ReadManifestStrict` with `invalid operation or phase` / `missing created-parent ownership` until those cases were fixed.

## GREEN

```text
$ go test ./session/workspace -run '^TestCrashRecovery' -count=1 -timeout=15m
ok  claude-squad/session/workspace

$ go test ./session/workspace -run '^TestCrashRecovery' -count=3 -timeout=30m
ok  claude-squad/session/workspace

$ go test ./session/workspace -count=1
ok  claude-squad/session/workspace

$ go test ./... -count=1
all packages passed

$ git diff --check
passed
```

## Assertions

Pre-commit cases converge to the exact recorded pre-Apply source snapshot and `rolled-back`; durable commit cases converge to the merged `AppliedEntries` snapshot and `complete`. Cases recover twice to prove idempotence. Blocked cases assert durable blocked state, exact issue paths and retained paths, preservation of external occupants, and preservation of replacement source temporaries. Source snapshots are also checked for transaction temporary names.

## Bugs fixed

- Current manifests now accept create entries in durable rollback phases produced by real rollback.
- Restored created-parent entries no longer require an ownership identity when the parent was never published; pending entries with forged ownership remain rejected.

Runtime behavior is otherwise unchanged; the new checkpoint callback defaults to a no-op.

## Self-review

- Checkpoints are package-private and stable string values.
- All crash injection uses `os.Exit(86)`, never returned errors or panics.
- Recovery subprocesses reconstruct solely from disk.
- Tests use `exec.Command`, `filepath`, and Go filesystem APIs only; no shell assumptions.
- Existing hook tests remain unchanged and continue to pass.

## Commit

Pending at report creation.

## Concerns

The matrix runs a full prepared fixture for each boundary, so the focused test takes roughly 42 seconds once and 125 seconds at `-count=3`; the complete workspace suite takes roughly seven minutes on this Darwin host.
