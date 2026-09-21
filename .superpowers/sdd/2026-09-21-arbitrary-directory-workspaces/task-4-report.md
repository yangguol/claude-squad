# Task 4 Report: Shadow Projects and Session Baselines

## Status

Implemented and verified.

## Changes

- Added the directory-mode shadow workspace backend with canonical SHA-256 project identity, guarded project metadata, local shadow Git repository initialization, temporary-index snapshot import, per-session baseline commits, generated session IDs, branches, and linked worktrees.
- Added shared-directory links restricted to real source directories and absent, contained worktree destinations. Setup removes partial worktrees if linking fails.
- Added lifecycle, diff, numstat, commit, restore, serialization, validity, remove, prune, and cleanup behavior. Push, branch checkout checks, and Apply (until Tasks 6-7) return typed unsupported errors.
- Persisted project/source/repository/worktree/session/branch/baseline, policy snapshot, shared links, and apply metadata fields in `ShadowData`.
- Wired the workspace factory to create shadow workspaces for non-Git directories.
- Added tests for stable layout and canonical identity, source immutability, fresh independent session baselines, shared links, lifecycle/restore, metadata collision rejection, and unsafe persisted paths.

## TDD evidence

Initial focused test command:

```text
go test ./session/workspace -run 'TestShadow' -v
```

Initial result (expected failure):

```text
# claude-squad/session/workspace [claude-squad/session/workspace.test]
session/workspace/shadow_test.go:25:21: undefined: NewShadow
session/workspace/shadow_test.go:49:15: undefined: shadowProjectMetadata
session/workspace/shadow_test.go:51:19: undefined: shadowProjectMetadata
session/workspace/shadow_test.go:60:19: undefined: NewShadow
session/workspace/shadow_test.go:66:20: undefined: NewShadow
session/workspace/shadow_test.go:96:16: undefined: NewShadow
session/workspace/shadow_test.go:111:19: undefined: RestoreShadow
session/workspace/shadow_test.go:158:27: undefined: shadowProjectMetadata
session/workspace/shadow_test.go:162:14: undefined: NewShadow
session/workspace/shadow_test.go:167:3: unknown field ProjectID in struct literal of type ShadowData
session/workspace/shadow_test.go:167:3: too many errors
FAIL	claude-squad/session/workspace [build failed]
FAIL
```

## Final tests

Focused integration command:

```text
PATH=/Users/yangguoliang/.local/bin:$PATH go test ./session/workspace -run 'TestShadow' -v
```

Output:

```text
=== RUN   TestShadowLayoutStableIdentityAndCanonicalSource
--- PASS: TestShadowLayoutStableIdentityAndCanonicalSource (0.57s)
=== RUN   TestShadowSessionsHaveFreshIndependentBaselines
--- PASS: TestShadowSessionsHaveFreshIndependentBaselines (0.93s)
=== RUN   TestShadowSharedLinksAndLifecycle
--- PASS: TestShadowSharedLinksAndLifecycle (1.25s)
=== RUN   TestShadowRejectsMetadataCollisionAndUnsafeRestore
--- PASS: TestShadowRejectsMetadataCollisionAndUnsafeRestore (0.01s)
PASS
ok  	claude-squad/session/workspace	3.465s
```

Full uncached command:

```text
PATH=/Users/yangguoliang/.local/bin:$PATH go test -count=1 ./...
```

Output:

```text
ok  	claude-squad	0.430s
ok  	claude-squad/app	9.697s
?   	claude-squad/cmd	[no test files]
?   	claude-squad/cmd/cmd_test	[no test files]
ok  	claude-squad/config	5.190s
?   	claude-squad/daemon	[no test files]
?   	claude-squad/keys	[no test files]
?   	claude-squad/log	[no test files]
ok  	claude-squad/session	6.279s
ok  	claude-squad/session/git	3.315s
ok  	claude-squad/session/tmux	1.303s
ok  	claude-squad/session/workspace	6.497s
ok  	claude-squad/ui	9.227s
?   	claude-squad/ui/overlay	[no test files]
```

`git diff --check` also passed with no output.

## Self-review

- Canonical source paths are resolved before hashing and persistence; source symlinks are not traversed by the scanner or shared-directory discovery.
- Existing metadata must be a regular file and exactly match version, project ID, and canonical source before project reuse.
- Session IDs are random 128-bit hex values; user titles never determine branch or worktree paths.
- Restore and every destructive lifecycle path reconstruct and validates expected repository/worktree locations beneath the configured shadow root before use.
- Shared links require real source directories, contained destinations, absent destinations, and non-symlink destination parents. Any failure removes the partial worktree.
- Snapshot imports use isolated temporary Git indexes and low-level Git object/index/tree/commit commands, preserving independent baselines while sharing repository objects.
- Diffs and commits explicitly exclude shared links so dependency contents are not staged or reported.

## Concerns

- `Apply()` intentionally remains unsupported in Task 4; Tasks 6 and 7 implement planning and transactional source updates while the capability advertises the intended directory-mode operation.
- Cleanup removes the session worktree and branch but retains the shared project repository; project-level garbage collection is assigned to Task 9.

## Commit

`33c0c2d feat: create shadow git workspaces`

## Fix Round 1

### Changes

- Validate persisted baseline values as full hexadecimal object IDs before Git use, require them to resolve to commits, and verify the baseline/branch ancestry relationship.
- Canonicalize and revalidate internal project, repository, and worktree paths without symlink ancestors before creation, removal, pruning, or other destructive Git operations.
- Make cleanup remove and prune the worktree before deleting its branch; branch deletion is skipped whenever worktree removal is unsafe, fails, or remains registered.
- Verify restore/session identity through metadata, real repository identity, baseline object type, branch history, and exact worktree repository/branch ownership.
- Revalidate snapshot regular files at import time using containment, lstat/open identity, type, and SHA-256 checks; reject mutation and symlink swaps.
- Revalidate shared source paths and every ancestor at link creation time.
- Validate and repair local repository identity and required user configuration on every project reuse, including partial initialization recovery.
- Added focused regressions for option injection, internal symlink escapes, stale registration and failed-removal safety, wrong identity, import races, shared ancestor swaps, and partial initialization.

### Exact verification

```text
PATH=/Users/yangguoliang/.local/bin:$PATH go test ./session/workspace -run 'TestShadow|TestImportSnapshot' -count=1 -v
```

```text
=== RUN   TestShadowLayoutStableIdentityAndCanonicalSource
--- PASS: TestShadowLayoutStableIdentityAndCanonicalSource (1.41s)
=== RUN   TestShadowSessionsHaveFreshIndependentBaselines
--- PASS: TestShadowSessionsHaveFreshIndependentBaselines (2.45s)
=== RUN   TestShadowSharedLinksAndLifecycle
--- PASS: TestShadowSharedLinksAndLifecycle (2.92s)
=== RUN   TestShadowRejectsMetadataCollisionAndUnsafeRestore
--- PASS: TestShadowRejectsMetadataCollisionAndUnsafeRestore (0.00s)
=== RUN   TestShadowRejectsBaselineInjectionAndWrongIdentity
--- PASS: TestShadowRejectsBaselineInjectionAndWrongIdentity (1.40s)
=== RUN   TestShadowRejectsSymlinkedInternalPaths
--- PASS: TestShadowRejectsSymlinkedInternalPaths (0.79s)
=== RUN   TestShadowSetupPrunesStaleRegistrationAndCleanupPreservesLiveBranch
--- PASS: TestShadowSetupPrunesStaleRegistrationAndCleanupPreservesLiveBranch (1.54s)
=== RUN   TestImportSnapshotRejectsMutationAndSymlinkSwap
--- PASS: TestImportSnapshotRejectsMutationAndSymlinkSwap (0.06s)
=== RUN   TestShadowRejectsSharedSourceAncestorSymlinkSwap
--- PASS: TestShadowRejectsSharedSourceAncestorSymlinkSwap (0.99s)
=== RUN   TestShadowRepairsPartialRepositoryInitialization
--- PASS: TestShadowRepairsPartialRepositoryInitialization (1.30s)
PASS
ok  claude-squad/session/workspace  13.597s
```

```text
PATH=/Users/yangguoliang/.local/bin:$PATH go test -count=1 ./...
```

```text
ok  claude-squad  0.602s
ok  claude-squad/app  9.814s
?   claude-squad/cmd  [no test files]
?   claude-squad/cmd/cmd_test  [no test files]
ok  claude-squad/config  6.174s
?   claude-squad/daemon  [no test files]
?   claude-squad/keys  [no test files]
?   claude-squad/log  [no test files]
ok  claude-squad/session  6.042s
ok  claude-squad/session/git  2.873s
ok  claude-squad/session/tmux  1.327s
ok  claude-squad/session/workspace  18.314s
ok  claude-squad/ui  9.425s
?   claude-squad/ui/overlay  [no test files]
```

```text
git diff --check
```

No output (passed).

### Self-review

- All persisted data is structurally validated before Git invocation; baseline values cannot begin with an option and must identify an existing commit.
- Filesystem checks are repeated immediately before sensitive operations and reject symlink components rather than trusting lexical containment.
- Cleanup is fail-closed: a failed or still-live worktree prevents branch deletion.
- Restore no longer accepts an unrelated Git repository/worktree at the expected pathname.
- Snapshot imports consume only the opened regular file whose identity was checked and whose bytes still match the scan hash.
- Source contents remain immutable; no Apply or project garbage collection behavior was added.

Final post-review verification:

```text
PATH=/Users/yangguoliang/.local/bin:$PATH go fmt ./session/workspace && PATH=/Users/yangguoliang/.local/bin:$PATH go test ./session/workspace -run 'TestShadow|TestImportSnapshot' -count=1 && PATH=/Users/yangguoliang/.local/bin:$PATH go test -count=1 ./... && git -C "/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces" diff --check
```

```text
ok  claude-squad/session/workspace  15.375s
ok  claude-squad  0.683s
ok  claude-squad/app  9.683s
?   claude-squad/cmd  [no test files]
?   claude-squad/cmd/cmd_test  [no test files]
ok  claude-squad/config  5.442s
?   claude-squad/daemon  [no test files]
?   claude-squad/keys  [no test files]
?   claude-squad/log  [no test files]
ok  claude-squad/session  6.268s
ok  claude-squad/session/git  2.963s
ok  claude-squad/session/tmux  1.640s
ok  claude-squad/session/workspace  17.963s
ok  claude-squad/ui  8.740s
?   claude-squad/ui/overlay  [no test files]
```

## Fix Round 2

### Changes

- Require `.git` to be a real, contained directory whose exact Git dir and common dir are the intended shadow repository metadata before configuration or lifecycle operations; reject symlinked and external gitdirs.
- Restore only existing worktrees that belong to the exact shadow repository and persisted session branch, while continuing to accept legitimately absent paused worktrees.
- Revalidate every snapshot entry during import: regular-file type, permission mode, file identity, and content hash; symlink type and exact target; and contained, non-symlink ancestors.
- Detect an uninitialized shadow repository by inspecting its exact `.git` path rather than repository discovery, initialize that exact path, and validate identity before writing repository-local configuration.
- Added focused regressions for linked/external Git metadata, unrelated versus absent restored worktrees, regular mode and symlink target/type mutation, and initialization nested beneath an enclosing repository without modifying enclosing config.

### TDD failure evidence

```text
PATH=/Users/yangguoliang/.local/bin:$PATH go test ./session/workspace -run 'TestShadowRejectsLinkedOrExternalGitMetadata|TestRestoreShadowRejectsUnrelatedWorktreeButAllowsAbsentPausedWorktree|TestImportSnapshotRejectsModeAndSymlinkMutations|TestShadowInitializesExactRepoWithoutModifyingEnclosingConfig' -count=1 -v
```

```text
--- FAIL: TestShadowRejectsLinkedOrExternalGitMetadata (0.61s)
    Error "shadow repository identity is invalid" does not contain "git metadata"
--- FAIL: TestRestoreShadowRejectsUnrelatedWorktreeButAllowsAbsentPausedWorktree (1.70s)
    An error is expected but got nil.
--- FAIL: TestImportSnapshotRejectsModeAndSymlinkMutations (0.64s)
    regular_mode: An error is expected but got nil.
    target: An error is expected but got nil.
    type: An error is expected but got nil.
--- FAIL: TestShadowInitializesExactRepoWithoutModifyingEnclosingConfig (0.40s)
    Received unexpected error: shadow repository identity is invalid
FAIL
```

### Exact verification

```text
PATH=/Users/yangguoliang/.local/bin:$PATH go test ./session/workspace -run 'TestShadow|TestImportSnapshot' -count=1 -v
```

```text
=== RUN   TestShadowLayoutStableIdentityAndCanonicalSource
--- PASS: TestShadowLayoutStableIdentityAndCanonicalSource (1.64s)
=== RUN   TestShadowSessionsHaveFreshIndependentBaselines
--- PASS: TestShadowSessionsHaveFreshIndependentBaselines (3.16s)
=== RUN   TestShadowSharedLinksAndLifecycle
--- PASS: TestShadowSharedLinksAndLifecycle (3.82s)
=== RUN   TestShadowRejectsMetadataCollisionAndUnsafeRestore
--- PASS: TestShadowRejectsMetadataCollisionAndUnsafeRestore (0.00s)
=== RUN   TestShadowRejectsBaselineInjectionAndWrongIdentity
--- PASS: TestShadowRejectsBaselineInjectionAndWrongIdentity (1.60s)
=== RUN   TestShadowRejectsSymlinkedInternalPaths
--- PASS: TestShadowRejectsSymlinkedInternalPaths (1.00s)
=== RUN   TestShadowSetupPrunesStaleRegistrationAndCleanupPreservesLiveBranch
--- PASS: TestShadowSetupPrunesStaleRegistrationAndCleanupPreservesLiveBranch (1.97s)
=== RUN   TestImportSnapshotRejectsMutationAndSymlinkSwap
--- PASS: TestImportSnapshotRejectsMutationAndSymlinkSwap (0.05s)
=== RUN   TestShadowRejectsSharedSourceAncestorSymlinkSwap
--- PASS: TestShadowRejectsSharedSourceAncestorSymlinkSwap (1.22s)
=== RUN   TestShadowRepairsPartialRepositoryInitialization
--- PASS: TestShadowRepairsPartialRepositoryInitialization (1.57s)
=== RUN   TestShadowRejectsLinkedOrExternalGitMetadata
--- PASS: TestShadowRejectsLinkedOrExternalGitMetadata (0.66s)
=== RUN   TestImportSnapshotRejectsModeAndSymlinkMutations
--- PASS: TestImportSnapshotRejectsModeAndSymlinkMutations (0.19s)
=== RUN   TestShadowInitializesExactRepoWithoutModifyingEnclosingConfig
--- PASS: TestShadowInitializesExactRepoWithoutModifyingEnclosingConfig (2.01s)
PASS
ok  claude-squad/session/workspace  19.328s
```

```text
PATH=/Users/yangguoliang/.local/bin:$PATH go test -count=1 ./...
```

```text
ok  claude-squad  0.760s
ok  claude-squad/app  10.367s
?   claude-squad/cmd  [no test files]
?   claude-squad/cmd/cmd_test  [no test files]
ok  claude-squad/config  6.289s
?   claude-squad/daemon  [no test files]
?   claude-squad/keys  [no test files]
?   claude-squad/log  [no test files]
ok  claude-squad/session  6.355s
ok  claude-squad/session/git  3.781s
ok  claude-squad/session/tmux  1.904s
ok  claude-squad/session/workspace  25.495s
ok  claude-squad/ui  9.689s
?   claude-squad/ui/overlay  [no test files]
```

```text
git diff --check
```

No output (passed).

### Concerns

- Filesystem identity checks are fail-closed and substantially narrow race windows, but Go's portable path APIs cannot provide a single atomic openat-style transaction across every path component and Git subprocess invocation.
