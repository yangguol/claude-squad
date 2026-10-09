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

## Fix Round 1

Expanded the disk-only subprocess matrix to exercise real create, update, delete, nested-parent, regular/symlink replacement, and directory-to-leaf replacement boundaries. Every crash now reads the strict on-disk manifest before recovery and asserts the explicitly mapped transaction state plus entry/cleanup phase; terminal points assert all relevant phases. Initial manifest publication has distinct pre-publication and post-rename/directory-sync checkpoints, proving orphan cleanup before publication and prepared rollback after publication.

Blocked recovery cases now assert exact paths and retained paths, preserve external occupants and replacement temporaries, and prove an identical durable blocked manifest after a second fresh recovery subprocess. Expected symlink modes are normalized from a platform probe before comparison rather than copied from observed results.

The expanded cases exposed and fixed two recovery bugs: pending directory-replacement creates were incorrectly treated as external targets before capture, and strict validation rejected expected directory-tree descendants after their parent had already been replaced by a leaf.

Verification:

```text
go test ./session/workspace -run '^TestCrashRecovery' -count=1 -timeout=15m  PASS (114.997s)
go test ./session/workspace -run '^TestCrashRecovery' -count=3 -timeout=30m  PASS (351.396s)
go test ./session/workspace -count=1 -timeout=20m                         PASS (514.104s)
go test ./... -count=1 -timeout=30m                                      PASS
git diff --check                                                         PASS
```

Concern: the expanded real-process matrix increases focused runtime to about two minutes once and six minutes at `-count=3` on this Darwin host.

## Fix Round 2

Moved `initial-manifest.before-publication` to the actual publication boundary: after the private manifest temporary has been fully written, synced, and closed, immediately before the atomic rename to `manifest.json`. The post-publication checkpoint remains after rename and transaction-directory sync.

The subprocess regression now proves that a crash at the pre-publication boundary leaves exactly one private `.manifest-*` file and no `manifest.json`. It deliberately corrupts those temporary bytes before launching fresh-process discovery, demonstrating that discovery classifies by the absence of the published manifest rather than trusting private temporary content. Recovery removes the transaction namespace, leaves at most an empty marked orphan tombstone with no temporary/orphan payload, and preserves the exact source snapshot.

TDD evidence and verification:

```text
go test ./session/workspace -run '^TestCrashRecoveryInitialManifestPublication$/before_publication' -count=1  RED (expected one .manifest-*; found none)
go test ./session/workspace -run '^TestCrashRecoveryInitialManifestPublication$' -count=1                     PASS (5.727s)
go test ./session/workspace -run '^TestCrashRecovery' -count=1 -timeout=15m                                   PASS (119.172s)
go test ./session/workspace -run '^TestCrashRecovery' -count=3 -timeout=30m                                   PASS (362.654s)
go test ./session/workspace -count=1 -timeout=20m                                                             PASS (526.689s)
go test ./... -count=1 -timeout=30m                                                                           PASS
git diff --check                                                                                              PASS
```

Concern: identity-safe orphan deletion intentionally retains an empty `.orphan-*` tombstone containing only its cleanup marker; no manifest temporary or untrusted orphan payload remains after recovery.
