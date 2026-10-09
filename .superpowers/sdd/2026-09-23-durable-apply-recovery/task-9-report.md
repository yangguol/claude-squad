# Task 9 Report: WAL Metadata Repair and App Warning Flow

## Status

Implemented Task 9 from base `58cab77f6feb2becd81e93eb98053582381f7269` with strict RED/GREEN coverage.

## RED

Command:

```bash
go test ./session ./app -run '^(TestLoadInstances|TestApplyCleanupWarning|TestApplyMetadataSaveFailure|TestBlockedSessionMenu)' -count=1
```

Initial result: failed as required. Storage tests did not compile because `LastAppliedTransactionID` and `Instance.AccessState` were absent. App tests failed because cleanup warnings were not acknowledged or rendered and blocked paused sessions still exposed Resume.

## Implementation

- Added `LastAppliedTransactionID` to serialized shadow metadata and included `SessionID` in `ApplyMetadataRepair`.
- Complete/cleanup-blocked WAL receipts now emit authoritative metadata repairs.
- `Storage.LoadInstances` now:
  1. decodes raw records;
  2. groups directory sessions by canonical project path;
  3. recovers each project in sorted path order;
  4. applies WAL metadata repairs to raw records;
  5. persists repaired records;
  6. constructs instances with the already-derived project access state;
  7. acknowledges matching complete receipts only after persistence succeeds.
- A failed repaired-metadata save returns before acknowledgement, leaving the WAL receipt recoverable.
- Complete committed transactions without a matching stored session are cleaned during project recovery rather than applied to unrelated metadata.
- Apply flow ordering is now Apply → save applied metadata → acknowledge complete receipt → pause → save paused state.
- Cleanup-blocked remains logical Apply success. Its acknowledgement attempt cannot remove the non-complete receipt, the session still pauses and persists, and the success overlay displays transaction ID, affected paths, and retained paths.
- Read-only restored sessions expose neither Apply nor Resume in the menu, and Resume key handling also checks capability.

## GREEN

Focused:

```bash
go test ./session ./app -run '^(TestLoadInstances|TestApplyCleanupWarning|TestApplyMetadataSaveFailure|TestBlockedSessionMenu)' -count=1
```

Result: PASS (`session` 24.655s, `app` 4.468s).

Storage/app packages:

```bash
go test ./session ./app -count=1
```

Result: PASS (`session` 38.452s, `app` 31.064s).

Affected workspace/UI packages:

```bash
go test ./session/workspace ./ui -count=1
```

Result: PASS (`session/workspace` 378.147s, `ui` 6.610s).

Full repository:

```bash
go test ./... -count=1
```

Result: PASS across all packages (`session/workspace` 378.983s).

Diff check:

```bash
git diff --check
```

Result: PASS.

## Ordering and Warning UX

A matching complete receipt is acknowledged only after repaired/applied metadata has been saved. Save failure leaves the receipt intact. Cleanup-blocked receipts remain retained and recoverable because acknowledgement only accepts complete receipts; the UI reports Apply Complete plus a typed cleanup warning and deterministic retained paths rather than entering Apply Failed.

## Self-review

- Verified project recovery runs once per grouped project before instance construction, avoiding per-session recovery races and preserving Task 8 read-only access state.
- Verified repaired state overrides stale serialized metadata.
- Verified no acknowledgement occurs after failed metadata persistence.
- Verified blocked sessions cannot invoke Resume by hidden key even when the menu omits it.
- Reset locking and crash-matrix expansion remain out of scope for Tasks 10 and 11.

## Concerns

None within Task 9 scope. Cleanup-blocked acknowledgement intentionally returns an error internally because the receipt is not complete; the warning path deliberately retains that receipt and continues the successful Apply lifecycle.

## Fix Round 1

### RED

Added deterministic regressions for newest-receipt selection (including lexical/time inversion and an equal-time transaction-ID tie), exact repaired metadata, cleanup-blocked restart without acknowledgement, orphan reporting, lock continuity through save/publication/acknowledgement, and hidden Apply/Resume keys. The first focused run failed on the pre-fix orphan behavior; the new multi-receipt and cleanup-blocked expectations additionally exposed the prior unconditional acknowledgement and directory-order selection.

### Implementation

- Selects one authoritative repair per session by parsed durable `AppliedAt`, then transaction ID as a deterministic tie-break, while acknowledging every complete receipt only after metadata persistence. Cleanup-blocked receipts are never acknowledged.
- Adds a scoped `ProjectRecoveryLease` callback API so grouped recovery, metadata save, instance/access publication, and eligible acknowledgement remain under Task 8 project locks. Locked restore avoids recursive lock acquisition.
- Restores cleanup-blocked sessions read-only with exact issue paths, retained paths, disposition, and message while persisting exact transaction/tree/time metadata.
- Reports cleaned absent-session complete receipts as informational loader evidence without unnecessarily blocking unrelated sessions.
- Blocks hidden checkout/apply and resume invocations when directory capabilities deny them.

### Verification

```bash
go test ./session ./app ./session/workspace -run '^(TestLoadInstances|TestApplyCleanupWarning|TestApplyMetadataSaveFailure|TestBlockedSessionMenu|TestRestoreShadow)' -count=1
```

PASS: `session` 34.040s, `app` 4.950s, `session/workspace` 6.667s.

```bash
go test ./session ./app -count=1
```

PASS: `session` 47.860s, `app` 31.085s.

```bash
go test ./session/workspace ./ui -count=1
```

PASS: `session/workspace` 384.798s, `ui` 6.665s.

```bash
go test ./... -count=1
```

PASS across all packages; `session/workspace` 386.530s.

```bash
git diff --check
```

PASS.

### Fix Round 1 Concerns

None. Task 10 reset locking and Task 11 crash-matrix expansion remain out of scope.
