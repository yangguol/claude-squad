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
