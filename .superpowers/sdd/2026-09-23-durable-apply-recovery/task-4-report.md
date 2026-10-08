# Task 4 Report: Complete Preallocated WAL

## Status

Complete. Apply preparation now creates and durably publishes a complete v2 WAL before source mutation while retaining the existing v1 executor for later Task 5 replacement.

## RED

Tests were added first for all required preparation behaviors plus the parked Task 3 nested-filesystem case-sensitivity finding.

```text
go test ./session/workspace -run '^(TestPrepareTransaction|TestPreparedManifest|TestManifestNamespaceComparisonUsesGoverningParentCaseBehavior)' -count=1
```

Expected build failure:

```text
session/workspace/apply_test.go:649:54: too many arguments in call to workspace.prepareTransaction
session/workspace/apply_test.go:656:24: cannot use entry (variable of struct type transactionEntry) as TransactionEntry value in assignment
session/workspace/apply_test.go:667:45: entry.Phase undefined
FAIL claude-squad/session/workspace [build failed]
```

This demonstrated that preparation still returned the v1 transaction shape and lacked v2 phases, paths, identities, and explicit parent entries.

## Implementation

- Changed preparation to `prepareTransaction(plan applyPlan, now time.Time) (TransactionManifest, error)` and publish `CurrentTransactionVersion` with `prepared`, transaction/project/session/source identities, applied tree, and timestamp.
- Added `preallocateTransactionEntries` to allocate every create/update/delete entry and every missing parent before mutation.
- Added random source-sibling capture and rollback-capture names and transaction-private stage/quarantine names. Names use cryptographic random bytes and are validated by the persisted manifest reader tests.
- Added complete operation identities: expected source state, result hash/mode/symlink target, and expected replacement-directory trees.
- Added explicit created-parent entries ordered shallow-first; their natural reverse order is deep-first for rollback.
- Added `stageTransactionResults`, which exclusively creates regular-file and symlink stages, syncs regular payload files, syncs stage directories, and syncs the transaction directory before durable manifest publication.
- Kept current v1 execution behavior through a narrow manifest-to-legacy execution adapter. Later tasks remain responsible for durable v2 forward phases and recovery.
- Preparation does not create source-side names or mutate source contents. Existing Apply failure cleanup removes only its owned transaction directory before execution starts.

## Parked Finding Correction

Namespace resolution now stores case behavior per path component and resets inherited case-insensitive state when traversal crosses a filesystem device boundary. Unresolved suffixes therefore use the behavior of their actual governing parent filesystem, rather than any case-insensitive ancestor. Existing case-insensitive-volume regressions continue to pass. Symlink leaf identities are handled without following them so staged symlink paths can be strictly read and validated.

## GREEN

```text
go test ./session/workspace -run '^(TestPrepareTransaction|TestPreparedManifest|TestManifestNamespaceComparisonUsesGoverningParentCaseBehavior)' -count=1
ok  claude-squad/session/workspace  12.792s

go test ./session/workspace -count=1
ok  claude-squad/session/workspace  185.881s

go test ./... -count=1
PASS (all repository packages; workspace 189.693s)

GOOS=linux go test ./session/workspace -run '^$' -exec=true -count=1
ok  claude-squad/session/workspace  0.029s

git diff --check
PASS
```

## Self-review

- Confirmed all recovery-critical names exist in the v2 manifest before the first source mutation.
- Confirmed source names are siblings of their targets and private names are transaction-contained.
- Confirmed source and private namespaces have no duplicate names across categories in the generated manifest.
- Confirmed regular stage data is synced before directory sync and durable `prepared` publication; symlink stage directory entries are synced before publication.
- Confirmed created-parent entries are explicit and shallow-first.
- Confirmed preparation failures are handled before mutation and Apply cleans only its owned private transaction directory.
- Confirmed current Apply rollback tests and full regressions pass through the compatibility adapter.

## Commit

Committed as `feat(workspace): preallocate complete apply WAL`; final hash is reported in the completion response.

## Concerns

- Forward execution still uses the legacy in-process executor after v2 preparation. It can overwrite the v2 manifest with v1 state during execution; replacing that behavior with durable v2 phases is explicitly Task 5.
- The nested-filesystem regression directly exercises the per-component comparison model because reliably creating a case-sensitive nested mount is not portable in unit tests.

## Fix Round 1

### RED evidence

The interrupted worker's regression tests were already present when this work was taken over; no inaccessible prior command output is claimed. Their failure modes are recoverable from the pre-fix diff against `58cfc25`: the compatibility adapter called the v1 manifest writer after backup, existing directories absent from `Snapshot.Entries` were classified as created, and replacement-directory trees came only from the file/symlink snapshot. The inherited tests that expose those defects are:

- `TestApplyKeepsCompleteV2ManifestAuthoritativeDuringLegacyExecution`
- `TestPrepareTransactionDoesNotRecordExistingParentsAsCreated`
- `TestPreparedReplacementDirectoryTreeIncludesDirectoriesModesAndEmptyDirectories`

Additional focused tests added during takeover cover validation-before-publication, deterministic source/private collision retries, exclusive transaction-directory collision retry, and the actual namespace resolver seam.

A first fresh workspace regression run also exposed three integration failures, subsequently corrected:

```text
TestApplyRollbackFailureRetainsIncompleteTransaction: expected applying, actual prepared
TestApplyDirectoryToFileAndFileToTree/file-to-tree: rollback path below a pre-replacement file returned ENOTDIR
TestValidateManifestRejectsNonexistentCaseEquivalentLeavesWhereSupported and TestValidateManifestRejectsTwoUnresolvedCaseEquivalentNamesWhereSupported: expected validation errors were absent
```

### Corrections

- The legacy executor now keeps the complete prepared v2 manifest authoritative and stores mutable compatibility state only in memory. Its retained failed transaction therefore remains v2 `prepared`; durable forward phases remain Task 5.
- Transaction directories use cryptographically random IDs, exclusive direct-child creation, collision retry, and parent-directory sync.
- Parent preallocation inspects the live source safely, so existing empty directories are not recorded as created.
- Source and transaction-private names are cryptographically generated, checked absent, reserved against in-manifest duplicates, and retried without creating source placeholders.
- Prepared manifests are strictly validated before durable publication.
- Replacement-directory identities include directories, empty directories, modes, regular files, and symlinks. Validation allows legitimate descendant operations beneath a path that this transaction replaces while retaining overlap checks elsewhere.
- Namespace case behavior is recomputed from each newly entered directory; unresolved components inherit their actual current parent behavior. The regression now drives `resolveManifestNamespacePath` through its lookup seam.
- Preparation failures leave source state unchanged and remove only the owned transaction directory.

### Fresh GREEN evidence

```text
go test ./session/workspace -run '^(TestPrepareTransaction|TestPreparedManifest|TestApplyKeepsCompleteV2ManifestAuthoritativeDuringLegacyExecution|TestPreallocationRetriesOccupiedSourceAndPrivateNames|TestExclusiveTransactionDirectoryRetriesCollision|TestManifestNamespaceComparisonUsesGoverningParentCaseBehavior)' -count=1
ok  claude-squad/session/workspace  19.211s

go test ./session/workspace -count=1
ok  claude-squad/session/workspace  178.457s

go test ./... -count=1
PASS (all repository packages; workspace 185.012s)

GOOS=linux go test ./session/workspace -run '^$' -exec=true -count=1
ok  claude-squad/session/workspace  0.014s

git diff --check
PASS
```

### Remaining concern

Durable v2 phase transitions and crash recovery are intentionally deferred to Task 5. During current legacy execution the authoritative manifest remains at `prepared`, even after source mutation; this round prevents schema destruction but does not claim crash-safe forward progress.
