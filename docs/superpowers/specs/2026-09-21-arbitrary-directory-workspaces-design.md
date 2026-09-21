# Arbitrary-Directory Isolated Workspaces Design

## Summary

Claude Squad currently refuses to start outside a Git repository and couples every session directly to `GitWorktree`. This change allows `cs` to start in any readable directory while preserving isolated agent workspaces, diffs, pause/resume, cleanup, and an explicit way to apply accepted changes.

Git repositories retain their existing behavior. Non-Git directories use an internal shadow Git repository managed under Claude Squad's configuration directory. The source directory is not initialized as a Git repository and is not changed until the user explicitly applies a session.

The startup fallback logo also gains a small, low-contrast `ygl` mark at its lower-right corner.

## Goals

- Allow `cs` to start in a Git or non-Git directory.
- Give every agent an isolated worktree, including agents created from non-Git directories.
- Preserve existing Git-repository behavior.
- Preserve diff, pause, resume, restore, and cleanup behavior in directory mode.
- Apply directory-session results only after explicit confirmation.
- Use three-way merge semantics when the source directory changed after session creation.
- Keep the source directory free of `.git` metadata.
- Avoid importing common dependency, build, and cache directories into shadow history.
- Add a small `ygl` mark to the lower-right of the startup fallback logo.

## Non-Goals

- Pushing a non-Git directory to GitHub or creating a remote repository.
- Real-time synchronization from sessions to the source directory.
- Perfect dependency isolation when dependency directories are shared by symlink.
- Replacing Git as the diff and merge engine.
- Automatically resolving genuine content conflicts.

## Existing Constraints

The current implementation assumes Git at several layers:

- `main.go` rejects a working directory when `git.IsGitRepo` is false.
- `session.Instance` stores a concrete `*git.GitWorktree`.
- Session setup, pause, resume, cleanup, and diff call `GitWorktree` methods directly.
- The application always offers branch search, push, and checkout actions.
- Stored instance data contains only `GitWorktreeData`.
- List rendering describes every session with a branch.

The implementation must therefore introduce a workspace boundary rather than only remove the startup check.

## Architecture

### Workspace abstraction

Introduce a `session/workspace` package with a mode-aware interface used by `session.Instance`. The final method names should follow existing conventions, but the interface must cover:

```go
type Mode string

const (
    ModeGit       Mode = "git"
    ModeDirectory Mode = "directory"
)

type Workspace interface {
    Mode() Mode
    Setup() error
    WorkDir() string
    DisplayName() string
    Diff() *git.DiffStats
    DiffNumstat() *git.DiffStats
    IsValid() (bool, error)
    IsDirty() (bool, error)
    CommitChanges(message string) error
    Remove() error
    Prune() error
    Cleanup() error
    CanPush() bool
    PushChanges(message string, open bool) error
    CanApply() bool
    Apply() (*ApplyResult, error)
    Serialize() Data
}
```

Methods required only by native Git behavior, such as checking whether a branch is checked out, may be represented as capabilities or mode-specific methods rather than dummy implementations. The application must ask capabilities instead of type-asserting concrete implementations.

### Implementations

`GitWorkspace` wraps the existing `git.GitWorktree`. It preserves branch creation and selection, diff, commit, push, checkout/pause, resume, and cleanup behavior.

`ShadowWorkspace` owns a non-Git source directory, a shadow repository, a session branch, and an isolated worktree. It reuses the existing Git diff machinery where practical but exposes directory-specific capabilities:

- Push and open-branch are unavailable.
- Apply-to-source is available.
- Branch selection is unavailable.
- The display name is the source directory name rather than a user-facing branch.

A factory selects the implementation from the absolute startup path. A path inside a Git repository selects `GitWorkspace`; any other existing readable directory selects `ShadowWorkspace`.

## Shadow Project Layout

Shadow projects live below the Claude Squad configuration directory:

```text
~/.claude-squad/shadow-projects/<project-id>/
├── repo/                 # internal Git repository and baseline commits
├── metadata.json         # schema version, canonical source path, project ID
├── worktrees/
│   ├── <session-id>/
│   └── <session-id>/
└── transactions/         # temporary merge/apply and rollback material
```

`project-id` is a stable hash of the canonical absolute source path. Metadata stores the canonical path and is checked before reuse to detect hash collisions or moved directories. Session directory names use generated IDs rather than raw titles, preventing traversal and title-collision issues.

All deletion operations must verify that the canonical target is contained by the configured shadow-project root. Persisted paths are never passed directly to recursive deletion without containment validation.

## Session Creation in Directory Mode

Each new session receives a baseline representing the source directory at that session's creation time:

1. Canonicalize the source path and validate that it exists, is a directory, and is readable.
2. Load default classification rules and `.claude-squad-ignore` overrides.
3. Scan included files without following directory symlinks outside the source tree.
4. Import the included tree into the shared shadow repository.
5. Create an immutable baseline commit for this session.
6. Create a session branch and Git worktree from that baseline.
7. Add approved shared-dependency symlinks to the worktree.
8. Start tmux with the shadow worktree as its working directory.

Creating a baseline per session prevents a later session from inheriting a stale snapshot. Multiple sessions for one source directory share repository objects but not branches, indexes, or working files.

## Ignore and Shared-Directory Policy

### Default shared dependencies

Initially classify these directories as dependencies when found at conventional locations:

- `node_modules`
- `.venv`
- `venv`

They are excluded from shadow history and linked from the source directory into each session worktree. Links are created only when the source entry is a directory and the destination path is absent. Failure to create a required link aborts session setup with a specific error; it does not silently copy content.

Changes within a shared dependency directory are visible to all sessions. The UI/help text documents this exception to workspace isolation.

### Default excluded generated content

Initially exclude common generated and cache directories, including:

- `dist`, `build`, `target`, `out`
- `.cache`, `.pytest_cache`, `.mypy_cache`, `.ruff_cache`
- `__pycache__`
- common editor and operating-system metadata

Generated directories are neither imported nor linked. Agents may regenerate them independently in their worktrees.

### Project overrides

A root-level `.claude-squad-ignore` uses gitignore-style patterns and supports explicit categories:

```text
# ordinary exclusions
coverage/
*.tmp

[shared]
vendor/

[generated]
.generated/
```

Ordinary patterns exclude matching paths. `[shared]` entries are excluded and symlinked from the source. `[generated]` entries are excluded without links. Explicit project rules take precedence over defaults. The file itself is included in the baseline unless explicitly excluded, so behavior is visible in session diffs.

## Diff and Session Lifecycle

Because every directory-mode agent operates in a Git worktree, existing full-diff and numstat behavior remains available. The baseline commit is session-specific.

Pause commits dirty work locally, detaches tmux, and removes the worktree while retaining the session branch. Resume recreates the worktree from that branch and restores or starts tmux. Kill removes tmux, the session worktree, and session branch. The shared shadow project is removed only when no stored or active session references it.

Restoration validates source metadata and all internal paths. If the source directory was moved or deleted, the session remains viewable/recoverable where possible but apply is disabled with a precise error.

## Applying Changes to the Source Directory

Directory mode replaces the checkout action with **Apply to source**. It is always explicit and requires confirmation.

### Inputs

- `BASE`: the immutable baseline commit created for the selected session.
- `OURS`: a fresh snapshot of the current source directory using the session's ignore policy.
- `THEIRS`: the selected agent worktree, including uncommitted changes.

### Merge behavior

A temporary Git index/tree performs a three-way merge equivalent to merging `THEIRS` into `OURS` with `BASE` as the merge base.

- Agent-only changes are applied.
- Source-only changes are retained.
- Non-overlapping changes are merged automatically.
- Same-region text edits conflict.
- Modify/delete conflicts conflict.
- Different same-path additions conflict.
- Binary files changed on both sides conflict.
- File/symlink and file/directory type changes conflict when Git cannot merge them safely.

Conflicts are detected entirely in a transaction directory. The source directory is unchanged and the UI shows the conflicting paths. No conflict markers are written into the source directory.

### Transactional update

When the merge is clean:

1. Materialize the merged result in a transaction directory on the same filesystem when possible.
2. Build an explicit manifest of creates, updates, deletes, modes, and symlink targets.
3. Revalidate source files touched by the manifest against the `OURS` snapshot to close the scan-to-apply race.
4. Back up only paths that will be changed or deleted.
5. Apply files with temporary siblings and atomic rename; apply deletions only after backups exist.
6. On failure, restore all touched paths from the backup manifest.
7. Preserve ignored/shared/generated paths.

A successful apply pauses the session and records the applied source snapshot and time. It does not delete the session. Applying the same unchanged session again is a no-op. If the agent changes the session after resume, a subsequent apply performs a new three-way merge against the original session baseline and current source.

The source root itself and files outside it are never deleted or overwritten. Symlink traversal is rejected during apply.

## Persistence and Compatibility

Extend `InstanceData` with versioned workspace data:

```go
type WorkspaceData struct {
    Version int             `json:"version"`
    Mode    workspace.Mode  `json:"mode"`
    Git     *GitData        `json:"git,omitempty"`
    Shadow  *ShadowData     `json:"shadow,omitempty"`
}
```

Shadow data includes the project ID, canonical source path, repository path, worktree path, session ID, branch, baseline commit, ignore-policy snapshot, and latest apply metadata.

Existing records with `worktree` but no workspace mode are migrated in memory as Git workspaces. Saving writes the new representation. No old session is silently converted to directory mode.

## CLI and TUI Behavior

The startup Git-only guard in `main.go` is removed and replaced with validation that the current path is an accessible directory.

The UI shows the selected session mode:

- `Git`
- `Directory (isolated)`

Git mode retains current keys and semantics. In directory mode:

- Branch fetching and branch picker are skipped.
- The session subtitle identifies the source directory instead of presenting a user-facing branch.
- The push action and open-branch behavior are hidden/disabled.
- The checkout action is labeled `Apply` and invokes the transactional apply flow.
- Diff, preview, attach, pause/resume, reorder, and delete remain available.
- Apply conflicts appear in an overlay listing paths, with no source mutation.

The menu derives available actions from selected-workspace capabilities, so mixed restored sessions render correctly.

## `ygl` Startup Mark

The fallback/startup ASCII logo in `ui/consts.go` receives a small lowercase `ygl` mark at the lower-right of the rendered logo block. The mark uses a low-contrast adaptive color and appears only in the startup/fallback view, not in the persistent menu or status area.

Rendering computes placement from visible terminal width. If there is insufficient width for the logo plus mark, the mark is omitted rather than wrapping or shifting the logo. This is a branding-only change and does not affect keys or layout sizing.

## Error Handling and Recovery

Session creation fails without registering a started session when:

- source scanning fails;
- the source is inaccessible;
- shadow repository initialization fails;
- required shared symlinks cannot be created;
- tmux cannot start.

Partial resources are cleaned in reverse order. Existing `Instance.Start` cleanup behavior must be corrected as part of introducing the workspace lifecycle so setup failures do not leak worktrees.

Apply errors preserve the agent workspace. Conflict errors are typed and include sorted relative paths. Rollback failures report both the original and rollback errors and retain transaction data for manual recovery. On next startup, incomplete transactions are detected and reported rather than discarded automatically.

`cs reset` cleans native worktrees and shadow workspaces after validating containment. It must not touch source directories.

## Testing Strategy

### Unit tests

- Workspace mode detection for repository roots, Git subdirectories, and non-Git directories.
- Stable project IDs and path-containment validation.
- Default ignore classification and `.claude-squad-ignore` precedence.
- Shared dependency symlink creation and generated-directory omission.
- Workspace serialization and migration of legacy `GitWorktreeData`.
- Capability-driven menu and branch-picker behavior.
- `ygl` placement, low-width omission, and no wrapping.

### Integration tests

Using temporary directories and real Git commands:

- Two directory-mode sessions receive independent worktrees.
- One agent never sees another's source edits before apply.
- Diff includes added, modified, deleted, renamed, executable, symlink, and binary files.
- Pause/resume and process restart preserve session changes.
- Clean three-way merges combine source-only and agent-only edits.
- Text, add/add, modify/delete, binary, and type conflicts leave the source unchanged.
- Apply creates, updates, and deletes expected files while preserving ignored paths.
- Injected mid-apply failure restores all touched source paths.
- Reapplying an unchanged session is a no-op.
- Git-mode tests continue to pass unchanged.

### End-to-end acceptance

From a temporary directory with no `.git`:

1. Run `cs` successfully.
2. Start two agents and verify different worktree paths.
3. Modify the same tracked file differently and verify isolated diffs.
4. Apply one session and verify only its clean result reaches the source.
5. Attempt to apply the other session and verify a conflict is reported without further source changes.
6. Verify no `.git` directory was added to the source.
7. Verify the startup fallback logo shows `ygl` at the lower-right on a sufficiently wide terminal.

## Security and Data-Safety Requirements

- Canonicalize and containment-check every internal cleanup path.
- Never follow source symlinks while scanning or applying.
- Never overwrite a source path that changed after the final pre-apply validation.
- Never mutate the source on merge conflict.
- Never place shadow repository metadata inside the source directory.
- Do not use force operations against user repositories in directory mode.
- Preserve file permissions supported by Git; report unsupported special files rather than silently converting them.

## Delivery Scope

The initial implementation targets existing supported desktop platforms where Git and tmux are available. It adds no remote publishing for directory mode and no interactive conflict editor. Conflict resolution remains external: users adjust the source or agent worktree and retry Apply.
