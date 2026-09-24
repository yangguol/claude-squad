# Task 3 Report: Strict Manifest Discovery and Legacy Classification

## Status

Complete. Added strict v2 manifest discovery, validation, deterministic classification, legacy retention, and validated orphan quarantine/removal.

## TDD Evidence

### RED

After adding the ten required tests in `session/workspace/recovery_test.go`, ran:

```text
go test ./session/workspace -run '^(TestReadManifestStrict|TestValidateManifest|TestInvalidManifest|TestLegacyIncomplete|TestOrphan|TestDiscoverySorts)' -count=1
```

Result: expected build failure because the Task 3 interfaces did not exist:

```text
session/workspace/recovery_test.go:50:13: undefined: ReadManifestStrict
session/workspace/recovery_test.go:70:138: undefined: ValidateManifest
session/workspace/recovery_test.go:133:21: undefined: DiscoverTransactions
session/workspace/recovery_test.go:175:19: undefined: removeOrphanTransaction
FAIL claude-squad/session/workspace [build failed]
```

### GREEN

After the minimal implementation and self-review fixes, ran the required focused command:

```text
go test ./session/workspace -run '^(TestReadManifestStrict|TestValidateManifest|TestInvalidManifest|TestLegacyIncomplete|TestOrphan|TestDiscoverySorts)' -count=1
ok  claude-squad/session/workspace  0.858s
```

## Implementation Details

- Added `DiscoveredTransaction`, `DiscoverTransactions`, `ReadManifestStrict`, and `ValidateManifest` in `session/workspace/recovery.go`.
- Strict v2 decoding uses `json.Decoder.DisallowUnknownFields` and requires EOF after the first JSON value.
- Discovery validates the project and transaction root, opens the root without following symlinks, checks root identity after enumeration, sorts entries by name, and reports invalid entries individually rather than rewriting them.
- Version 1 manifests are classified as `Legacy` using only their version envelope and are retained byte-for-byte for manual recovery. Unsupported versions are classified invalid.
- Validation rejects unknown states, operations, entry phases, cleanup phases, and operation/phase combinations.
- Validation rejects duplicate target paths and duplicate temporary names, including duplicates across temporary-name categories.
- Paths must be relative, clean, slash-canonical, NUL-free, contained in their trusted root, and reconstructible without traversing existing symlink/non-directory parents. Source-side capture names must reconstruct as siblings of their targets.
- Manifest project/source identity is checked against strictly decoded project metadata, and transaction identity is checked against the direct transaction directory name.
- Manifest and transaction-directory symlinks are rejected; regular-file identity is checked before and after reading.
- Manifest-less direct real-directory children are classified as orphans, revalidated, atomically renamed to a random same-root quarantine name, identity-checked, checked again for absence of a manifest, and only then recursively removed.
- Invalid and legacy manifest bytes are never rewritten.
- Existing Task 2 project-lock and atomic-preflight semantics were not changed.

## Verification

Required focused suite:

```text
ok  claude-squad/session/workspace  0.858s
```

Workspace regression suite:

```text
ok  claude-squad/session/workspace  165.942s
```

Full repository suite:

```text
ok  claude-squad
ok  claude-squad/app
ok  claude-squad/config
ok  claude-squad/session
ok  claude-squad/session/git
ok  claude-squad/session/tmux
ok  claude-squad/session/workspace
ok  claude-squad/ui
```

`git diff --check` passed with no output.

## Self-Review

- Tightened cleanup phase validation so an omitted/unknown cleanup phase fails closed.
- Corrected create-phase validation to the operation-specific state sequence from the design.
- Consolidated temporary-name uniqueness across all temporary categories.
- Reconstructed capture and rollback-capture paths under the source root and required them to be target siblings; stage and quarantine paths are reconstructed under the transaction directory.
- Ensured unknown versions are invalid rather than accidentally treated as current manifests.
- Removed an unsafe special-case that ignored stale quarantine-looking directory names during discovery.
- Added stable project metadata reads so source/project validation does not trust a symlinked or replaced metadata file.
- Confirmed no changes to lock or preflight behavior and no writes to invalid/legacy manifests.

## Commit

Task changes and this report are committed together as `feat(workspace): validate durable apply transactions`; the resulting hash is included in the completion response.

## Concerns

- This task adds the strict discovery/classification API but does not replace the legacy v1 apply executor or wire automatic recovery into workspace lifecycle; those are explicitly assigned to later durable-recovery tasks.
- Orphan cleanup is safe only for a directory with no manifest at discovery/revalidation time, matching the invariant that source mutation cannot begin before a durable v2 manifest exists.

## Fix Round 1

### Status

Complete. Hardened Task 3 against the seven review findings while preserving byte-for-byte legacy/invalid classification, deterministic discovery order, and Task 2 lock/preflight behavior.

### Covering Files and Exact Tests

- `session/workspace/recovery_test.go`
  - `TestReadManifestStrictRejectsDuplicateMembersRecursively`
  - `TestValidateManifestUsesCanonicalTrustedRootsAndMetadataIdentity`
  - `TestValidateManifestSeparatesNamespacesAndRejectsActualCollisions`
  - `TestValidateManifestRequiresRecoveryPathsAndValidExpectedTree`
  - `TestDiscoveryValidatesCleanupMarkerTypeAndContents`
  - `TestOrphanCleanupBindsRootAcrossSubstitution`
  - `TestOrphanCleanupRetainsOwnedAndUnownedContentAfterQuarantineSubstitution`
- `session/workspace/recovery.go`
- `session/workspace/recovery_orphan_unix.go`

### RED

After adding the deterministic regression tests first, the focused command failed to build as expected because the orphan substitution seams did not exist:

```text
session/workspace/recovery_test.go:369:2: undefined: orphanBeforeQuarantineHook
session/workspace/recovery_test.go:390:2: undefined: orphanAfterQuarantineHook
FAIL claude-squad/session/workspace [build failed]
```

### Fixes

- Orphan quarantine now opens and retains a stable transaction-root descriptor, captures with descriptor-relative atomic no-replace rename, verifies inode identity through descriptors, and recursively unlinks only descriptor-bound private content without following symlinks. Any substitution or ambiguous entry fails closed and retains content.
- Strict manifest structure scanning rejects duplicate JSON object members recursively before version or typed decoding.
- Trusted project and source roots must be absolute canonical real paths; canonical macOS temporary paths remain valid after test fixtures resolve them through `EvalSymlinks`.
- Metadata validation now requires the project directory ID to equal `shadowProjectID(metadata.SourcePath)` before trusting manifest source identity.
- Collision validation compares canonical reconstructed identities in separate source and transaction namespaces. Target/capture/rollback collisions and stage/quarantine collisions are rejected, while equal relative strings in disjoint roots are allowed.
- Recovery-required paths are enforced by operation; every recovery path is reconstructed from its trusted root. `ExpectedTree` keys must match embedded paths, remain beneath the target, avoid unsafe existing parents, and not overlap ancestor/descendant entries.
- Cleanup markers are ignored only after stable regular-file and strict committed-v1 marker validation; directories, symlinks, and malformed marker bytes are reported invalid.

### GREEN and Regressions

```text
go test ./session/workspace -run '^(TestReadManifestStrict|TestValidateManifest|TestInvalidManifest|TestLegacyIncomplete|TestOrphan|TestDiscovery)' -count=1
ok  claude-squad/session/workspace  0.809s

go test ./session/workspace -count=1
ok  claude-squad/session/workspace  171.678s

go test ./... -count=1
PASS (all repository packages)

git diff --check
PASS
```

### Concerns

- Descriptor-bound orphan deletion is implemented for the repository's supported atomic-recovery platforms (`darwin` and `linux`); unsupported platforms remain outside the existing durable-apply support contract.

## Fix Round 2

### Status

Complete. Closed the four remaining review findings with tests written and observed failing before implementation.

### RED Evidence

The new focused tests failed on the captured-private-inode manifest rule, lexical aliases, filesystem aliases, ancestor overlaps, and missing create/update/delete/created-parent fields. Representative failures were:

```text
TestOrphanCleanupAllowsWritesToCapturedPrivateInode: quarantined orphan transaction contains a manifest
TestValidateManifestRejectsLexicalTrustedRootAliases: expected error but got nil
TestValidateManifestRejectsAncestorNamespaceOverlaps: expected error but got nil
TestValidateManifestRequiresOperationSpecificRecoveryFields/create_rollback_capture: expected error but got nil
```

### Fixes and Exact Coverage

- `TestOrphanCleanupRetainsReplacementAtPrivateQuarantinePath` proves descriptor cleanup never unlinks a replacement at the quarantine pathname.
- `TestOrphanCleanupManifestBeforeCaptureBlocksDeletion` proves a manifest installed before capture retains the source orphan.
- `TestOrphanCleanupAllowsWritesToCapturedPrivateInode` proves post-capture writes belong to the owned private inode and may be removed.
- `TestValidateManifestRejectsLexicalTrustedRootAliases` rejects project, transaction, and metadata source aliases before cleaning.
- `TestValidateManifestRejectsFilesystemAliasesWhereSupported` uses `os.SameFile` and skips only when the fixture filesystem is case-sensitive.
- `TestValidateManifestRejectsAncestorNamespaceOverlaps` covers source and transaction namespace ancestor/descendant collisions.
- `TestValidateManifestRequiresOperationSpecificRecoveryFields` covers every required create, update, delete, and created-parent path or identity.

Recursive orphan removal now begins only after atomic capture and a final pre-capture-manifest check. It remains bound to the retained directory descriptor; the empty private directory is intentionally retained because safely unlinking it would require resolving a replaceable pathname again. Namespace validation rejects lexical overlaps and existing device/inode aliases without case folding. Required recovery fields now match the Task 4/5 state machines.

### GREEN and Regressions

```text
go test ./session/workspace -run '^(TestReadManifestStrict|TestValidateManifest|TestInvalidManifest|TestLegacyIncomplete|TestOrphan|TestDiscovery)' -count=1
ok  claude-squad/session/workspace  0.903s

go test ./session/workspace -count=1
ok  claude-squad/session/workspace  165.321s

go test ./... -count=1
PASS (all repository packages; workspace 173.996s)

GOOS=linux go test ./session/workspace -run '^$' -exec=true -count=1
ok  claude-squad/session/workspace  0.014s

git diff --check
PASS
```

### Concerns

- Successful orphan cleanup intentionally leaves one empty random `.orphan-*` directory in the transaction root. Removing that final directory by name would reintroduce the pathname-substitution race; later private-quarantine cleanup can reclaim it under the Task 7 trust model.

## Fix Round 3

### Status

Complete. Closed the four open findings with focused tests written and observed failing before implementation.

### RED

```text
go test ./session/workspace -run '^(TestValidateManifestRejectsMalformedEntryIdentities|TestDiscoverySkipsOnlyValidatedRetainedOrphanTombstones|TestValidateManifestRejectsNonexistentCaseEquivalentLeavesWhereSupported)$' -count=1
FAIL
```

Failures showed malformed identities were accepted, retained orphan directories were rediscovered, and case-equivalent nonexistent leaves were not detected.

### Fixes

- Trusted roots retain the workspace-created canonical representation; Darwin additionally compares the open directory descriptor's `F_GETPATH` spelling. Linux documents the platform boundary and retains descriptor identity/no-follow validation without case-folding heuristics.
- Namespace validation now traverses components descriptor-relatively without following symlinks, records stable device/inode identities, and compares nonexistent suffixes under verified parents. Case-equivalent spelling is used only after the filesystem has demonstrated case-insensitive lookup.
- Expected, Result, and ExpectedTree identities now validate allowed types, permission-only modes, SHA-256 hashes, and mutually exclusive regular/symlink/directory fields.
- Successful orphan cleanup writes and syncs a private descriptor-relative terminal marker. Discovery skips only exact random quarantine names containing that sole strictly validated marker; arbitrary similarly named directories remain orphans.

### GREEN and Regressions

```text
go test ./session/workspace -run '^(TestReadManifestStrict|TestValidateManifest|TestInvalidManifest|TestLegacyIncomplete|TestOrphan|TestDiscovery)' -count=1
ok  claude-squad/session/workspace  1.135s

go test ./session/workspace -count=1
ok  claude-squad/session/workspace  171.707s

go test ./... -count=1
PASS (all repository packages; workspace 173.031s)

GOOS=linux go test ./session/workspace -run '^$' -exec=true -count=1
ok  claude-squad/session/workspace  0.017s

git diff --check
PASS
```

### Concerns

- Linux has no portable descriptor API exposing authoritative stored path spelling. The implementation therefore fails closed on detectable aliases and uses stable inode identities plus exact lexical names otherwise, as required by the ruling.

## Fix Round 4

### Status

Complete. Closed the remaining canonical-spelling, namespace traversal, unresolved case-sensitivity, and forged tombstone findings with deterministic tests first.

### RED

```text
go test ./session/workspace -run '^(TestValidateManifestAppliesStoredSpellingToAllTrustedDirectories|TestValidateManifestBindsNamespaceIdentityToOpenedComponent|TestDiscoveryClassifiesForgedRetainedOrphansAndFIFOWithoutBlocking)$' -count=1
FAIL (build failed: undefined manifestComponentBeforeOpenHook)
```

After introducing the seam but before completing the fixes, focused failures showed transaction-root aliases still passed and namespace parent substitution was not rejected.

### Fixes and Rationale

- Canonical trusted-directory validation now covers the transaction root and caller transaction directory as well as project and metadata source paths. Darwin applies `F_GETPATH` spelling checks to each; Linux retains its documented descriptor/no-follow boundary.
- Namespace traversal opens each existing component no-follow, derives identity with `fstat` from that exact descriptor, and revalidates the parent/name binding after open. It no longer combines `fstatat` identity with a separately opened object.
- On the first missing component, case sensitivity is inferred non-mutatingly from existing entries in that actual parent. Unresolved suffix comparisons case-fold only when that parent demonstrates case-insensitive lookup.
- Retained `.orphan-<hex>` entries are never silently skipped based on forgeable marker content. Exact lowercase names and sole-entry regular markers are validated with `O_NONBLOCK|O_NOFOLLOW`, but even valid tombstones are returned as explicit orphan/invalid classifications for manual recovery. Malformed and uppercase lookalikes are retained and classified rather than deleted.

### GREEN and Regressions

```text
go test ./session/workspace -run '^(TestReadManifestStrict|TestValidateManifest|TestInvalidManifest|TestLegacyIncomplete|TestOrphan|TestDiscovery)' -count=1
ok  claude-squad/session/workspace  1.016s

go test ./session/workspace -count=1
ok  claude-squad/session/workspace  170.639s

go test ./... -count=1
PASS (all repository packages; workspace 175.602s)

GOOS=linux go test ./session/workspace -run '^$' -exec=true -count=1
ok  claude-squad/session/workspace  0.014s

git diff --check
PASS
```

### Concerns

- Final removal of a retained orphan directory remains intentionally deferred: pathname-only unlink cannot safely guarantee that the checked directory is the one removed. Discovery now reports every retained tombstone explicitly instead of trusting forgeable in-entry evidence.
- Darwin provides authoritative descriptor path spelling; Linux cannot portably provide the equivalent and retains the documented platform boundary.

## Fix Round 5

### Status

Complete. Closed the final shared-directory-offset and blocking FIFO findings while preserving descriptor-bound identity checks, retained orphan classification, canonical roots, and Task 2 behavior.

### RED

```text
go test ./session/workspace -run '^(TestValidateManifestRejectsTwoUnresolvedCaseEquivalentNamesWhereSupported|TestValidateManifestRejectsFinalFIFOWithoutBlocking)$' -count=1
FAIL
```

`TestValidateManifestRejectsTwoUnresolvedCaseEquivalentNamesWhereSupported` was accepted unexpectedly, and `TestValidateManifestRejectsFinalFIFOWithoutBlocking` timed out after two seconds.

### Fixes

- Component lookup now performs one deterministic scan of the opened parent descriptor and returns exact spelling, case-equivalent identity, and demonstrated filesystem case behavior together. No independent scan relies on a consumed shared directory stream offset.
- Namespace components are opened with `O_NONBLOCK|O_NOFOLLOW`, immediately identified using `fstat`, and rebound to the parent entry. Directories continue traversal, regular leaves retain identity, symlinks are not followed, and special files such as FIFOs are rejected without blocking.
- Added focused coverage for two unresolved case-equivalent names beneath a case-insensitive parent and for prompt rejection of a final FIFO leaf.

### GREEN and Regressions

```text
go test ./session/workspace -run '^(TestValidateManifestRejectsTwoUnresolvedCaseEquivalentNamesWhereSupported|TestValidateManifestRejectsFinalFIFOWithoutBlocking)$' -count=1
ok  claude-squad/session/workspace  1.033s

go test ./session/workspace -run '^(TestReadManifestStrict|TestValidateManifest|TestInvalidManifest|TestLegacyIncomplete|TestOrphan|TestDiscovery)' -count=1
ok  claude-squad/session/workspace  2.243s

go test ./session/workspace -count=1
ok  claude-squad/session/workspace  192.543s

go test ./... -count=1
PASS (all repository packages; workspace 167.397s)

GOOS=linux go test ./session/workspace -run '^$' -exec=true -count=1
ok  claude-squad/session/workspace  0.016s

git diff --check
PASS
```

### Concerns

- The case-equivalence regression is skipped on case-sensitive filesystems by design; Linux compilation verifies the shared implementation, while runtime case-insensitive behavior is exercised where the fixture filesystem supports it.
