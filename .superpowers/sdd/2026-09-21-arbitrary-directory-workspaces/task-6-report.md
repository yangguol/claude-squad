# Task 6 Report

## Status

Implemented three-way apply planning for directory-mode shadow workspaces. Planning creates fresh `OURS` and `THEIRS` commits from the persisted policy, includes uncommitted agent changes, performs the merge without changing the session branch, returns sorted typed conflicts, and builds a deterministic create/update/delete manifest without writing the source directory.

## Evidence

### TDD red

`go test ./session/workspace -run 'TestPlanApply|TestApplyConflict' -v`

Failed at compile time because `shadowWorkspace.planApply` did not exist.

### Focused tests

`go test ./session/workspace -run 'TestPlanApply|TestApplyConflict' -count=1 -v`

PASS (`ok claude-squad/session/workspace 33.405s`). Coverage includes clean agent/source/non-overlapping edits, additions, deletion, rename, executable mode, unchanged reapply, persisted exclusions/generated content, and text/add-add/modify-delete/binary/file-directory/file-symlink conflicts. Every case verifies that planning leaves source bytes and metadata unchanged.

### Full suite

`go test -count=1 ./...`

PASS. `session/workspace` completed in 60.970s; all repository packages passed.

### Static patch check

`git diff --check`

PASS with no output.

## Implementation

- Added `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/apply.go`.
- Added `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/apply_test.go`.
- Updated `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/shadow.go` to share snapshot import with transaction-local temporary indexes and delegate `Apply` to planning.
- Updated `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/shadow_test.go` to remove the obsolete unsupported-apply assertion.

## Self-review

- `BASE` is always the validated immutable baseline commit.
- Fresh source and worktree scans use only the persisted policy snapshot; shared, generated, and excluded content is omitted.
- Worktree `.git` metadata is explicitly excluded from the imported `THEIRS` tree.
- Temporary indexes are contained under the validated per-project transaction root.
- `merge-tree` and object arguments are derived from validated 40-character lowercase object IDs, not paths or user-supplied options.
- Manifest/conflict paths reject absolute paths, empty/dot/dot-dot components, backslashes, non-canonical cleaning, containment escapes, and source-parent symlink traversal.
- Conflict stages are reconstructed in a transaction-local index and reported through sorted source-relative `ConflictError.Paths`.
- Source-only changes are absent from the manifest because it is derived from `OURS` to the merged tree.
- `Apply` intentionally returns the plan result without source writes; Task 7 owns transactional materialization and rollback.

## Concerns

- Git object creation necessarily writes immutable objects into the shadow repository object database; filesystem indexes and conflict state remain transaction-contained, and no branch/ref or source path is modified.
- Transaction directories from direct internal `planApply` calls are retained for Task 7 consumption; exported Task 6 `Apply` removes its planning directory after returning because it performs no writes yet.

## Commit

`7756679 feat: plan safe three-way directory merges`

## Fix Round 1

### Status

Resolved all three Important findings. Conflict transport is NUL-safe and exact-path checked, merge-tree parsing now fails closed while distinguishing stage records from informational records, and complete source/worktree snapshots are re-scanned after import and again immediately before a plan or conflict result can be returned.

The ruling against platform-specific transaction-directory owner checks was followed. Deferred Minor coverage was not expanded beyond regressions required by these fixes.

### TDD red evidence

`go test ./session/workspace -run 'TestApplyConflictPreservesUnusualPathExactly|TestParseMergeTreeOutputFailsClosed|TestPlanApplyRejectsCompleteSnapshotRaces|TestMergeApplyCommitsDoesNotAcceptFailedCommandWithStages' -count=1`

Failed to compile because the parser and deterministic hooks did not exist. After their initial implementation, focused tests also exposed strict-parser compatibility cases for clean merges and legitimate conflicts with omitted stages; the parser was corrected without relaxing malformed/truncated-output rejection.

### Covering regression tests

`go test ./session/workspace -run 'TestApplyConflictPreservesUnusualPathExactly|TestParseMergeTreeOutputFailsClosed|TestPlanApplyRejectsCompleteSnapshotRaces|TestMergeApplyCommitsDoesNotAcceptFailedCommandWithStages' -count=1 -v`

PASS (`ok claude-squad/session/workspace 8.158s`). Covers exact tab/newline/quote/non-ASCII conflict paths, truncated/unrecognized/malformed merge-tree data, command failure despite parsed stages, source additions after import, and late worktree mutation.

### Focused Task 6 tests

`go test ./session/workspace -run 'TestPlanApply|TestApplyConflict|TestParseMergeTreeOutput|TestMergeApplyCommits' -count=1`

PASS (`ok claude-squad/session/workspace 41.991s`).

### Uncached full suite

`go test -count=1 ./...`

PASS. `session/workspace` completed in 69.029s; all repository packages passed.

### Diff check

`git diff --check`

PASS with no output.

### Fix implementation evidence

- `git merge-tree --write-tree -z` is parsed as NUL-delimited bytes; stage headers and informational records are validated structurally, duplicate/invalid stages and truncation are rejected, and only expected exit status 1 with parsed conflicts is accepted.
- Conflict index input uses `git update-index -z --index-info`; `git ls-files -u -z` output is fully validated and compared against the exact expected path/stage partition before paths are returned.
- Source and agent snapshots are complete `Snapshot` values, so equality checks include the complete path set plus entry type, mode, content hash, and symlink target. Re-scans detect additions, deletions, and mutations after individual import, with a final check after manifest/conflict processing to prevent stale plans from proceeding.
