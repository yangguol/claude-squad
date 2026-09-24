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
