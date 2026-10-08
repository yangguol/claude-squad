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

## Fix Round 2

### RED

The new focused tests were added before implementation. The first run failed to compile because the required deterministic parent-classification, directory-sync, and replacement-scan seams did not exist:

```text
session/workspace/apply_test.go:955:2: undefined: applyAfterParentClassificationHook
session/workspace/apply_test.go:970:18: undefined: applySyncDirectory
session/workspace/apply_test.go:1008:2: undefined: replacementScanBeforeOpenHook
FAIL claude-squad/session/workspace [build failed]
```

The tests cover source-capture and transaction-quarantine occupants appearing after preparation, malformed ancestor exemptions, cleanup removal/sync ordering and removal failure retention, descendant substitution during replacement-tree scanning, and stale created-parent classification.

### Corrections

- Recovery names remain cryptographically generated absent names rather than claimed reservations. Source and private absence checks now traverse from opened stable parent descriptors. Execution consumes source capture and the persisted rollback-capture name only through atomic no-replace renames; private backup creation remains exclusive. Occupants appearing after publication are preserved and cause safe failure rather than overwrite or deletion.
- Descendant reconstruction exemptions now require a validated directory-to-non-directory replacement, or the exact created-directory transition paired with replacement of a non-directory at the same path. Arbitrary create/delete ancestors no longer suppress overlap or reconstructibility checks.
- Every failed cleanup of an unpublished, durably-created transaction directory removes first and then syncs the transaction root. Removal failures are joined into the returned error and leave the directory discoverable; post-publication failures retain the authoritative manifest.
- Replacement directory snapshots now recursively scan through no-follow directory descriptors on Darwin/Linux. Every directory and regular file is identity-bound to its parent; symlinks are read without following; special files are rejected; directory entries, empty directories, modes, regular hashes, and symlink targets are retained.
- Parent existence/type classifications are retained in memory and revalidated descriptor-relatively immediately before staging and durable manifest publication. Appearance, disappearance, or type change aborts preparation without mutating source content.

### GREEN and regressions

```text
go test ./session/workspace -run '^(TestPreparedCaptureOccupantIsPreservedAndApplyFailsSafely|TestPreparedRollbackCaptureOccupantIsPreservedAndRollbackFailsSafely|TestPreparedQuarantineOccupantIsPreservedAndApplyFailsSafely|TestPrepareTransactionRevalidatesParentClassificationBeforePublish|TestPrepareTransactionCleanupSyncsRootAfterRemoval|TestPrepareTransactionCleanupFailurePreservesAndReportsTransaction|TestPreparedReplacementDirectoryScanRejectsSwappedDescendant|TestValidateManifestDoesNotExemptDescendantsBelowMalformedAncestor)$' -count=1
ok  claude-squad/session/workspace  21.484s

go test ./session/workspace -count=1
ok  claude-squad/session/workspace  195.380s

go test ./... -count=1
PASS (all repository packages; workspace 213.275s)

GOOS=linux go test ./session/workspace -run '^$' -exec=true -count=1
ok  claude-squad/session/workspace  0.014s

git diff --check
PASS
```

The first full workspace regression run exposed the valid file-to-directory transition being rejected by the new parent check. The classification was corrected to retain exact entry types and validation was narrowed to the paired non-directory replacement semantics; the full package and repository suites then passed.

### Remaining concern

Durable v2 phase transitions and crash recovery remain Task 5. The fix deliberately guarantees absent-at-preparation plus atomic/exclusive no-replace consumption; it does not claim impossible hostile namespace reservation.

## Fix Round 3

### RED

The focused substitution and publication-window tests were added first. Their initial run failed to compile because the deterministic seams did not yet exist:

```text
session/workspace/apply_test.go:1061:2: undefined: preparedPathAfterOpenHook
session/workspace/apply_test.go:1085:2: undefined: replacementScanAfterRootOpenHook
session/workspace/apply_test.go:1105:2: undefined: applyAfterManifestValidationHook
FAIL claude-squad/session/workspace [build failed]
```

### Corrections

- Prepared-path classification now retains every opened ancestor descriptor and its parent/name identity binding, verifies each binding before accepting presence or absence, and closes the root and every child descriptor on all returns.
- Replacement-tree snapshots now open the root descriptor-relative through a retained stable source-root chain, keep every ancestor descriptor alive, and revalidate the replacement root plus the complete ancestor chain after scanning before accepting `ExpectedTree`.
- Parent classifications are revalidated again after staging and strict manifest validation, immediately before durable manifest publication. A late appearance, disappearance, or type change aborts and removes only the unpublished transaction directory.
- Added deterministic final-parent substitution, replacement-root substitution, late parent mutation, and descriptor-reuse leak regressions. Source objects are not modified by preparation failures.

### GREEN and regressions

```text
go test ./session/workspace -run '^(TestPrepareTransaction|TestPreparedManifest|TestPreparationTraversal|TestPreparedReplacementDirectoryScan)' -count=1
ok  claude-squad/session/workspace  30.873s

go test ./session/workspace -count=1
ok  claude-squad/session/workspace  208.936s

go test ./... -count=1
PASS (all repository packages; workspace 207.724s)

GOOS=linux go test ./session/workspace -run '^$' -exec=true -count=1
ok  claude-squad/session/workspace  0.013s

git diff --check
PASS
```

### Remaining concern

Pathname-based forward execution capture and rollback remain intentionally scoped to Task 5. This round binds preparation checks and snapshots without broadening into legacy execution migration.

## Fix Round 4

### RED

Direct deterministic tests were added at the two remaining vulnerable boundaries. Before implementation, both present/absent final-component substitutions and source-root substitution were accepted:

```text
TestPreparedPathClassificationRejectsSwappedFinalComponent/present_leaf: expected error, got nil
TestPreparedPathClassificationRejectsSwappedFinalComponent/absent_leaf: expected error, got nil
TestPreparedReplacementDirectoryScanRejectsSwappedSourceRoot: expected error, got nil
```

### Corrections

- Present prepared-path leaves are opened descriptor-relatively with nonblocking, no-follow semantics; the exact opened object is `fstat`-classified, retained through ancestor verification, and rebound to its parent/name before acceptance. Linux uses `O_PATH`; Darwin uses `O_SYMLINK` so symlink and special leaves are inspectable without following or blocking.
- Absent prepared-path leaves are checked absent again after the retained ancestor chain is verified.
- Replacement scanning resolves the already-canonical source path, opens it component-by-component from the filesystem root, retains every descriptor and parent/name identity, and revalidates the complete chain after scanning. This binds the source-root leaf to its canonical parent while retaining the existing canonical source-path trust boundary.
- The Unix FD reuse regression moved from cross-platform `apply_test.go` into Darwin/Linux-tagged `apply_unix_test.go`; the shared test file no longer imports `x/sys/unix`.

### GREEN and regressions

```text
go test ./session/workspace -run '^(TestPreparedPathClassificationRejectsSwappedFinalComponent|TestPreparedReplacementDirectoryScanRejectsSwappedSourceRoot|TestPreparationTraversalClosesAllDescriptors)$' -count=1
ok  claude-squad/session/workspace  0.792s

go test ./session/workspace -run '^(TestPrepareTransaction|TestPreparedManifest|TestPreparationTraversal|TestPreparedPathClassification|TestPreparedReplacementDirectoryScan)' -count=1
ok  claude-squad/session/workspace  27.391s

go test ./session/workspace -count=1
ok  claude-squad/session/workspace  198.680s

go test ./... -count=1
PASS (all repository packages; workspace 206.064s)

GOOS=darwin go test ./session/workspace -run '^$' -exec=true -count=1
ok  claude-squad/session/workspace  0.013s

GOOS=linux go test ./session/workspace -run '^$' -exec=true -count=1
ok  claude-squad/session/workspace  0.012s

git diff --check
PASS
```

Windows test compilation was attempted with `GOOS=windows go test ./session/workspace -run '^$' -count=1`. It remains blocked by pre-existing missing unsupported-platform recovery definitions in `recovery.go` (`orphanQuarantinePrefix`, `manifestFileIdentity`, and related functions), rather than by the moved FD test or this round's tagged implementation.

### Remaining concern

The stable ancestor traversal anchors at the opened filesystem root and rechecks every canonical source-path component, but no userspace scheme can prevent replacement of an already-open ultimate filesystem mount/root anchor. Durable v2 execution migration remains deferred to Task 5.
