# Task 2 Report: Workspace Mode Detection and Startup Validation

## Status

DONE

## Summary

Added canonical path validation and Git/plain-directory mode detection, removed the Git-only startup guard, and routed first-time instance setup through the workspace factory. Git sessions continue through the Task 1 adapter; plain-directory construction returns the explicitly permitted directory-backend-unavailable error until the shadow backend is implemented.

## Files changed

- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/factory.go`
  - Added `DetectMode`, `New`, canonical directory resolution, and `ErrDirectoryBackendUnavailable`.
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/factory_test.go`
  - Added Git root, nested Git directory, plain directory, missing path, regular file, symlink, and unavailable-directory-backend coverage.
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/main.go`
  - Replaced the Git-only startup gate with `validateWorkingDirectory`, which accepts existing directories and rejects missing/non-directory paths.
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/main_test.go`
  - Added startup validation tests for plain directories, missing paths, and regular files.
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/instance.go`
  - Routed first-time setup through `workspace.New` and used the returned workspace for setup, work directory selection, and cleanup on tmux startup failure.

The approved plan/spec files were not modified.

## TDD evidence and exact test commands/results

### RED

Command:

```bash
PATH=/Users/yangguoliang/.local/bin:$PATH go test ./session/workspace . -run 'TestDetectMode|TestValidateWorkingDirectory' -v
```

Result: FAIL as expected. Both packages failed to compile because `DetectMode` and `validateWorkingDirectory` did not exist.

### GREEN — prescribed focused and existing tests

Command:

```bash
PATH=/Users/yangguoliang/.local/bin:$PATH go test ./session/workspace ./session . -v
```

Result: PASS.

- `ok claude-squad/session/workspace 2.263s`
- `ok claude-squad/session 0.617s`
- `ok claude-squad 0.969s`

### GREEN — full suite

Command:

```bash
PATH=/Users/yangguoliang/.local/bin:$PATH go test ./...
```

Result: PASS for all packages.

- `claude-squad`: PASS
- `claude-squad/app`: PASS
- `claude-squad/config`: PASS
- `claude-squad/session`: PASS
- `claude-squad/session/git`: PASS
- `claude-squad/session/tmux`: PASS
- `claude-squad/session/workspace`: PASS
- `claude-squad/ui`: PASS
- Packages without tests reported `[no test files]`.

### Post-commit focused verification

Command:

```bash
PATH=/Users/yangguoliang/.local/bin:$PATH go test ./session/workspace ./session . -v
```

Result: PASS; all workspace, session, and root validation tests passed (cached).

## Commit

- `879301f feat: detect directory workspace mode`

The implementation commit contains exactly the five source/test files listed above. This report is stored in the task `.superpowers` area and is not part of the implementation commit.

## Self-review

- Confirmed detection calls `filepath.Abs`, `filepath.EvalSymlinks`, and `os.Stat` before selecting a mode with `git.IsGitRepo`.
- Confirmed Git roots and nested Git directories resolve to `ModeGit`; plain directories resolve to `ModeDirectory`.
- Confirmed missing paths and regular files return errors from detection and startup validation.
- Confirmed startup no longer emits or enforces the Git-only requirement.
- Confirmed first-time instance setup uses `workspace.New`, including the requested source path, session title, and selected branch.
- Confirmed existing restoration behavior remains on the legacy concrete Git path pending the broader Task 5 lifecycle/persistence refactor.
- Confirmed Git setup uses the workspace adapter's `Setup`, `WorkDir`, and `Cleanup` methods without changing existing restored-session behavior.
- Confirmed formatting, `git diff --check`, focused tests, and the full suite pass.
- Confirmed no unrelated files and no plan/spec changes were committed.

## Concerns

- Plain-directory startup validation now succeeds, but creating a directory-backed session intentionally returns `ErrDirectoryBackendUnavailable` until Task 4 implements and wires the shadow workspace constructor, per the ruling.
- `Instance` temporarily contains both the legacy `gitWorktree` field and the new first-setup `sessionWorkspace` field. Task 5 is explicitly responsible for completing the lifecycle and persistence migration to the workspace interface.

## Fix round 1

### Status

DONE

### Findings addressed

- Factory-created Git sessions now populate the legacy `gitWorktree` compatibility field from the factory workspace, preserving repo display, serialization, diff, pause, resume, and related Git lifecycle behavior until Task 5 completes the interface migration.
- `Kill` prefers `sessionWorkspace.Cleanup` and falls back to `gitWorktree.Cleanup`, so factory-created sessions clean their workspace exactly once while restored legacy sessions retain existing cleanup behavior.

### Files changed

- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/instance.go`
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/instance_test.go`
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/session/workspace/git_workspace.go`
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/.superpowers/sdd/2026-09-21-arbitrary-directory-workspaces/task-2-report.md`

### TDD evidence and exact commands/results

RED command:

```bash
PATH=/Users/yangguoliang/.local/bin:$PATH go test ./session -run 'TestFactoryCreatedGitInstance' -v
```

Result: FAIL with a nil-pointer panic in `Instance.RepoName` because factory-created Git sessions had no `gitWorktree`.

GREEN targeted command:

```bash
PATH=/Users/yangguoliang/.local/bin:$PATH go test ./session -run 'TestFactoryCreatedGitInstance' -v
```

Result: PASS. Covering tests:

- `TestFactoryCreatedGitInstanceRepoNameAndDisplay`
- `TestFactoryCreatedGitInstanceSerialization`
- `TestFactoryCreatedGitInstanceComputeDiff`
- `TestFactoryCreatedGitInstancePause`
- `TestFactoryCreatedGitInstanceKillCleansWorkspaceExactlyOnce`

Broader targeted command:

```bash
PATH=/Users/yangguoliang/.local/bin:$PATH go test ./session/workspace ./session . -v
```

Result: PASS for workspace, session, and root packages.

Full-suite command:

```bash
PATH=/Users/yangguoliang/.local/bin:$PATH go test ./...
```

Result: PASS for all packages.

### Self-review

- Confirmed the compatibility accessor is limited to the Git adapter and does not expose its concrete implementation type.
- Confirmed the same concrete worktree backs both fields, preserving all existing Git-only lifecycle paths without duplicating state.
- Confirmed cleanup selects one owner rather than invoking both aliases.
- Confirmed restored legacy sessions still use the previous concrete cleanup path.
- Confirmed formatting and `git diff --check` pass.

### Concerns

- The concrete `gitWorktree` compatibility field remains intentionally temporary; Task 5 should remove the bridge after all lifecycle and persistence paths use `workspace.Workspace`.
