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
