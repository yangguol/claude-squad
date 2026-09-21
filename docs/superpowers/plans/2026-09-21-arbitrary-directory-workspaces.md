# Arbitrary-Directory Isolated Workspaces Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let Claude Squad run from non-Git directories by creating isolated shadow-Git workspaces with safe three-way apply, while preserving current Git behavior and adding a small `ygl` startup mark.

**Architecture:** Replace `session.Instance`'s concrete `*git.GitWorktree` dependency with a capability-oriented workspace interface. Native Git sessions adapt the existing worktree code; directory sessions snapshot source files into a per-source shadow repository, create one branch/worktree per agent, and apply accepted changes through a staged three-way merge plus rollback transaction.

**Tech Stack:** Go 1.23/toolchain 1.25.8, Git CLI, tmux, Bubble Tea, Lip Gloss, `testify/require`.

**Spec:** `docs/superpowers/specs/2026-09-21-arbitrary-directory-workspaces-design.md`

## Global Constraints

- Existing Git-repository startup, branch selection, worktree, diff, pause/resume, push, and checkout behavior must remain unchanged.
- Non-Git source directories must never receive a `.git` directory.
- Directory sessions must not mutate the source before explicit Apply confirmation.
- Apply conflicts must leave the source unchanged.
- Internal cleanup must canonicalize paths and verify containment below the Claude Squad configuration root.
- Source scanning and apply must not follow symlinks outside the source tree.
- Shared dependency directories are `node_modules`, `.venv`, and `venv`; generated/cache directories are excluded and not linked.
- Directory mode has no branch picker, push, open-branch, or remote publishing.
- Avoid new third-party dependencies; use the Git CLI and Go standard library.
- Every behavior change is developed test-first, and `go test ./...` plus `go vet ./...` must pass before completion.

## File Structure

**Create:**

- `session/workspace/workspace.go` — workspace modes, capabilities, serialization DTOs, apply result/error types, and interface.
- `session/workspace/factory.go` — detects Git versus directory mode and restores persisted implementations.
- `session/workspace/git_workspace.go` — adapter around existing `session/git.GitWorktree`.
- `session/workspace/shadow.go` — shadow project/session creation and lifecycle.
- `session/workspace/policy.go` — default classification and `.claude-squad-ignore` parsing.
- `session/workspace/snapshot.go` — safe source-tree traversal and snapshot import.
- `session/workspace/apply.go` — three-way merge, manifest, transactional apply, rollback, and conflict reporting.
- Corresponding `*_test.go` files in `session/workspace/`.

**Modify:**

- `session/git/worktree.go` and `session/git/worktree_git.go` — expose existing worktree data needed by the adapter without changing behavior.
- `session/instance.go` — depend on `workspace.Workspace`, route lifecycle and capabilities, and fix partial-start cleanup.
- `session/storage.go` — persist versioned workspace data and migrate old worktree records.
- `main.go` — remove Git-only startup rejection and clean shadow projects on reset.
- `app/app.go` and `app/help.go` — capability-driven branch, push, checkout/apply, conflict, and help flows.
- `keys/keys.go` — add an Apply label/binding while retaining `c`.
- `ui/menu.go`, `ui/list.go`, and `ui/preview.go` — mode-aware labels and paused text.
- `ui/consts.go` — render the lower-right `ygl` mark.
- Existing tests beside each modified file.

---

### Task 1: Define Workspace Contracts and Native Git Adapter

**Files:**
- Create: `session/workspace/workspace.go`
- Create: `session/workspace/git_workspace.go`
- Create: `session/workspace/workspace_test.go`
- Modify: `session/git/worktree.go`

**Interfaces:**
- Produces: `workspace.Mode`, `workspace.Capabilities`, `workspace.Data`, `workspace.ApplyResult`, `workspace.ConflictError`, and `workspace.Workspace`.
- Produces: `workspace.NewGit(path, sessionName, branch string) (Workspace, string, error)` and `workspace.RestoreGit(data Data) (Workspace, error)`.
- Consumes: existing `git.NewGitWorktree`, `git.NewGitWorktreeFromBranch`, and `git.NewGitWorktreeFromStorage`.

- [ ] **Step 1: Write contract and adapter tests**

Create table-driven tests that assert `ModeGit`, `CanPush=true`, `CanApply=false`, native display name/branch, serialization round-trip, and rejection of malformed persisted Git data:

```go
func TestGitWorkspaceCapabilities(t *testing.T) {
    repo := initGitRepo(t)
    ws, branch, err := NewGit(repo, "feature", "")
    require.NoError(t, err)
    require.NotEmpty(t, branch)
    require.Equal(t, ModeGit, ws.Mode())
    require.True(t, ws.Capabilities().Push)
    require.False(t, ws.Capabilities().Apply)
}
```

- [ ] **Step 2: Run the focused test and verify failure**

Run: `go test ./session/workspace -run 'TestGitWorkspace' -v`

Expected: FAIL because `session/workspace` and its types do not exist.

- [ ] **Step 3: Implement minimal contracts and adapter**

Define:

```go
type Mode string
const ( ModeGit Mode = "git"; ModeDirectory Mode = "directory" )

type Capabilities struct { BranchPicker, Push, Apply bool }

type ApplyResult struct { Changed []string }
type ConflictError struct { Paths []string }
func (e *ConflictError) Error() string

type Data struct {
    Version int         `json:"version"`
    Mode    Mode        `json:"mode"`
    Git     *GitData    `json:"git,omitempty"`
    Shadow  *ShadowData `json:"shadow,omitempty"`
}

type Workspace interface {
    Mode() Mode
    Capabilities() Capabilities
    WorkDir() string
    DisplayName() string
    BranchName() string
    Setup() error
    IsValid() (bool, error)
    IsDirty() (bool, error)
    IsBranchCheckedOut() (bool, error)
    Diff() *git.DiffStats
    DiffNumstat() *git.DiffStats
    CommitChanges(string) error
    PushChanges(string, bool) error
    Apply() (*ApplyResult, error)
    Remove() error
    Prune() error
    Cleanup() error
    Serialize() Data
}
```

Add a read-only `Data()` accessor to `GitWorktree` so the adapter does not duplicate private-field logic. Unsupported native Apply returns a typed `ErrUnsupported`.

- [ ] **Step 4: Run focused tests**

Run: `go test ./session/workspace ./session/git -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add session/workspace/workspace.go session/workspace/git_workspace.go session/workspace/workspace_test.go session/git/worktree.go
git commit -m "refactor: introduce workspace abstraction"
```

### Task 2: Detect Workspace Mode and Remove the Git-Only Startup Gate

**Files:**
- Create: `session/workspace/factory.go`
- Create: `session/workspace/factory_test.go`
- Modify: `main.go:42-50`
- Modify: `session/instance.go:149-184`

**Interfaces:**
- Consumes: Task 1 `Workspace`, `ModeGit`, `ModeDirectory`, and `NewGit`.
- Produces: `workspace.DetectMode(path string) (Mode, error)` and `workspace.New(path, sessionName, branch string) (Workspace, string, error)`.

- [ ] **Step 1: Write mode-detection tests**

Cover a Git root, a nested Git directory, a plain directory, a missing path, and a regular file:

```go
func TestDetectModePlainDirectory(t *testing.T) {
    dir := t.TempDir()
    mode, err := DetectMode(dir)
    require.NoError(t, err)
    require.Equal(t, ModeDirectory, mode)
}
```

Also add a root-command test that executes startup validation from a plain temporary directory without receiving “must be run from within a git repository.” Extract a small `validateWorkingDirectory(path string) error` function in `main.go` to avoid launching the TUI in the test.

- [ ] **Step 2: Run tests and verify failure**

Run: `go test ./session/workspace . -run 'TestDetectMode|TestValidateWorkingDirectory' -v`

Expected: FAIL because detection and validation functions do not exist.

- [ ] **Step 3: Implement detection and startup validation**

Use `filepath.Abs`, `filepath.EvalSymlinks`, and `os.Stat`; choose Git mode with `git.IsGitRepo`. Replace the Git-only error with checks that the path exists and is a directory. Wire `InstanceOptions.Path` through the workspace factory during first setup; the directory constructor may initially return an explicit not-yet-provisioned error until Task 5.

- [ ] **Step 4: Run focused and existing tests**

Run: `go test ./session/workspace ./session . -v`

Expected: PASS for mode detection and all existing Git-backed instance tests.

- [ ] **Step 5: Commit**

```bash
git add session/workspace/factory.go session/workspace/factory_test.go main.go session/instance.go
git commit -m "feat: detect directory workspace mode"
```

### Task 3: Implement Ignore Classification and Safe Tree Scanning

**Files:**
- Create: `session/workspace/policy.go`
- Create: `session/workspace/policy_test.go`
- Create: `session/workspace/snapshot.go`
- Create: `session/workspace/snapshot_test.go`

**Interfaces:**
- Produces: `Policy`, `EntryClass`, `LoadPolicy(source string) (Policy, error)`, and `Scan(source string, policy Policy) (Snapshot, error)`.
- Produces: `Snapshot.Entries map[string]Entry`, where `Entry` records relative slash path, file type, mode, content hash, and symlink target.
- Consumed by Tasks 4 and 6.

- [ ] **Step 1: Write policy parser tests**

Test defaults, ordinary patterns, `[shared]`, `[generated]`, comments, blank lines, negation precedence, and malformed section headers. Use fixtures written under `t.TempDir()`.

```go
func TestLoadPolicyClassifiesDefaultsAndOverrides(t *testing.T) {
    source := t.TempDir()
    require.NoError(t, os.WriteFile(filepath.Join(source, ".claude-squad-ignore"),
        []byte("[shared]\nvendor/\n[generated]\n.generated/\n"), 0o644))
    policy, err := LoadPolicy(source)
    require.NoError(t, err)
    require.Equal(t, ClassShared, policy.Classify("vendor"))
    require.Equal(t, ClassGenerated, policy.Classify("dist"))
}
```

- [ ] **Step 2: Run policy tests and verify failure**

Run: `go test ./session/workspace -run 'TestLoadPolicy|TestPolicy' -v`

Expected: FAIL because policy APIs do not exist.

- [ ] **Step 3: Implement policy parsing without dependencies**

Use `path.Match` plus directory-prefix matching for gitignore-like rooted patterns. Support `!pattern` to re-include and section headers exactly `[shared]` and `[generated]`; return a line-numbered error for any other bracketed section.

- [ ] **Step 4: Write scanner security tests**

Cover included regular files, executable bits, empty directories omission, internal symlinks recorded as symlinks, directory symlinks not traversed, external symlink targets recorded but never read, excluded paths, and deterministic sorted traversal.

- [ ] **Step 5: Run scanner tests and verify failure**

Run: `go test ./session/workspace -run 'TestScan' -v`

Expected: FAIL because `Scan` is undefined.

- [ ] **Step 6: Implement the scanner**

Use `filepath.WalkDir`, `os.Lstat`, `filepath.Rel`, and `filepath.ToSlash`. Never call `Stat` on symlinks. Hash regular-file bytes with SHA-256 and reject sockets, devices, and named pipes with a relative-path error.

- [ ] **Step 7: Run focused tests**

Run: `go test ./session/workspace -run 'TestLoadPolicy|TestPolicy|TestScan' -v`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add session/workspace/policy.go session/workspace/policy_test.go session/workspace/snapshot.go session/workspace/snapshot_test.go
git commit -m "feat: classify and scan directory workspaces"
```

### Task 4: Create Shadow Projects and Session Baselines

**Files:**
- Create: `session/workspace/shadow.go`
- Create: `session/workspace/shadow_test.go`
- Modify: `session/workspace/factory.go`

**Interfaces:**
- Consumes: Task 3 `LoadPolicy` and `Scan`.
- Produces: `NewShadow(source, sessionName string) (Workspace, string, error)` and `RestoreShadow(data Data) (Workspace, error)`.
- Produces persisted `ShadowData` fields: project ID, source path, repository path, worktree path, session ID, branch, baseline commit, policy snapshot, and apply metadata.

- [ ] **Step 1: Write shadow-layout and isolation tests**

Set `HOME` to a temporary directory so `config.GetConfigDir()` is isolated. Assert stable SHA-256 path-derived project IDs, canonical source metadata, no `.git` under source, separate branches/worktrees for two sessions, and per-session baselines reflecting source state at their respective creation times.

- [ ] **Step 2: Run tests and verify failure**

Run: `go test ./session/workspace -run 'TestShadow' -v`

Expected: FAIL because shadow workspace construction is absent.

- [ ] **Step 3: Implement shadow repository initialization**

Create `<config>/shadow-projects/<id>/repo`, initialize Git there, configure repository-local identity (`Claude Squad`, `claude-squad@local`), import each session snapshot through a temporary index using `GIT_INDEX_FILE`, `git hash-object`, `git update-index`, `git write-tree`, and `git commit-tree`, then create `session/<session-id>` and a linked worktree from the resulting baseline. Do not copy `.git` metadata into source.

- [ ] **Step 4: Implement shared dependency links**

After worktree creation, create links only for policy entries classified shared and present as source directories. Validate each destination stays below the worktree root. Record created links in `ShadowData` for deterministic restore and cleanup.

- [ ] **Step 5: Implement lifecycle and capability methods**

Implement diff/numstat against the baseline, commit, validity, remove, prune, cleanup, serialization, and restore. Return `ErrUnsupported` for push and branch checkout checks. `Capabilities()` returns `{BranchPicker:false, Push:false, Apply:true}`.

- [ ] **Step 6: Run focused tests**

Run: `go test ./session/workspace -run 'TestShadow' -v`

Expected: PASS, including two-session isolation and source immutability.

- [ ] **Step 7: Commit**

```bash
git add session/workspace/shadow.go session/workspace/shadow_test.go session/workspace/factory.go
git commit -m "feat: create shadow git workspaces"
```

### Task 5: Refactor Instance Lifecycle and Persistence

**Files:**
- Modify: `session/instance.go`
- Modify: `session/instance_test.go`
- Modify: `session/storage.go`
- Create: `session/storage_test.go`

**Interfaces:**
- Consumes: Tasks 1 and 4 workspace factory, lifecycle, diff, and serialization methods.
- Produces: `Instance.WorkspaceMode() workspace.Mode`, `Instance.Capabilities() workspace.Capabilities`, `Instance.Apply() (*workspace.ApplyResult, error)`, and `Instance.DisplayWorkspace() string`.

- [ ] **Step 1: Write instance lifecycle tests with a fake workspace**

Define a test fake implementing `workspace.Workspace`. Verify Setup precedes tmux Start, tmux receives `WorkDir()`, failure after Setup calls Cleanup even before `started=true`, pause commits/removes, resume sets up missing worktree, kill cleans both resources, and diff delegates to the interface.

- [ ] **Step 2: Run tests and verify failure**

Run: `go test ./session -run 'TestInstanceWorkspace|TestStartCleans' -v`

Expected: FAIL because Instance still holds concrete `GitWorktree` and lacks injection.

- [ ] **Step 3: Refactor `Instance` to own `workspace.Workspace`**

Replace `gitWorktree` with `workspace`. Add `Workspace` to `InstanceOptions` for test injection; otherwise call the factory on first start. Route setup, work directory, diff, pause/resume, cleanup, push/apply capabilities, and display methods through the interface. Replace `GetGitWorktree` usage with intention-revealing Instance methods.

Fix partial setup cleanup by tracking acquired resources locally and invoking workspace cleanup directly when tmux startup fails; do not depend on `Kill()` while `started` is false.

- [ ] **Step 4: Write persistence migration tests**

Test new Git and directory records round-trip. Feed legacy JSON containing only `worktree` and assert it restores as `ModeGit` with unchanged paths, branch, base SHA, and existing-branch flag.

- [ ] **Step 5: Run persistence tests and verify failure**

Run: `go test ./session -run 'TestStorage|TestLegacy|TestInstanceData' -v`

Expected: FAIL until `WorkspaceData` is serialized and migrated.

- [ ] **Step 6: Implement versioned persistence**

Add `Workspace workspace.Data` while retaining `Worktree *GitWorktreeData 'json:"worktree,omitempty"'` solely for decoding legacy records. `FromInstanceData` chooses the versioned field when present and otherwise constructs `ModeGit` data from legacy fields. New saves omit the legacy field.

- [ ] **Step 7: Run session and Git regression tests**

Run: `go test ./session ./session/git ./session/workspace -v`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add session/instance.go session/instance_test.go session/storage.go session/storage_test.go
git commit -m "refactor: route sessions through workspace backends"
```

### Task 6: Implement Three-Way Merge Planning and Conflict Reporting

**Files:**
- Create: `session/workspace/apply.go`
- Create: `session/workspace/apply_test.go`

**Interfaces:**
- Consumes: `ShadowWorkspace` baseline/repository/source/worktree fields and Task 3 scanner.
- Produces: internal `planApply() (applyPlan, error)` and exported `Apply() (*ApplyResult, error)`.
- Produces: `ConflictError.Paths`, sorted and source-relative.

- [ ] **Step 1: Write clean-merge tests**

Cover agent-only edits, source-only edits, non-overlapping edits in one text file, additions, deletions, renames, executable mode changes, and unchanged reapply. Assert planning does not mutate source.

- [ ] **Step 2: Write conflict tests**

Cover same-line text edits, add/add, modify/delete, binary/binary, file-versus-directory, and file-versus-symlink conflicts. Assert `errors.As(err, *ConflictError)`, sorted paths, and byte-for-byte unchanged source.

- [ ] **Step 3: Run merge tests and verify failure**

Run: `go test ./session/workspace -run 'TestPlanApply|TestApplyConflict' -v`

Expected: FAIL because apply planning is absent.

- [ ] **Step 4: Implement source snapshot commit and merge tree**

Import current source as an `OURS` commit using the session's persisted policy. Materialize agent working changes as a temporary `THEIRS` commit without changing the session branch. Use `git merge-tree --write-tree BASE OURS THEIRS`; parse conflict stages with `git ls-files -u` against a temporary index. Return `ConflictError` before writing source.

- [ ] **Step 5: Build the apply manifest**

Compare `OURS` and merged tree with `git diff-tree --raw -r -z`. Populate deterministic creates, updates, deletes, modes, and symlink targets. Reject paths that are absolute, contain `..`, escape after `filepath.Clean`, or traverse a source-directory symlink.

- [ ] **Step 6: Run merge tests**

Run: `go test ./session/workspace -run 'TestPlanApply|TestApplyConflict' -v`

Expected: PASS; source remains unchanged because transaction writing is Task 7.

- [ ] **Step 7: Commit**

```bash
git add session/workspace/apply.go session/workspace/apply_test.go
git commit -m "feat: plan safe three-way directory merges"
```

### Task 7: Apply Transactions, Rollback, and Recovery Metadata

**Files:**
- Modify: `session/workspace/apply.go`
- Modify: `session/workspace/apply_test.go`
- Modify: `session/workspace/shadow.go`
- Modify: `session/workspace/shadow_test.go`

**Interfaces:**
- Consumes: Task 6 `applyPlan` and merge tree.
- Produces: transaction manifest persisted under `<project>/transactions/<session-id>-<nonce>/manifest.json` with state `prepared`, `applying`, `rolled_back`, or `committed`.
- Produces: `FindIncompleteTransactions(root string) ([]Transaction, error)` for startup diagnostics.

- [ ] **Step 1: Write transactional apply tests**

Verify successful create/update/delete/mode/symlink application, preservation of ignored paths, final revalidation failure when source changes after planning, and a no-op result for unchanged trees.

- [ ] **Step 2: Write injected-failure rollback tests**

Inject filesystem operations through an unexported `fileOps` interface. Fail after one update and one deletion, then assert every touched source path matches its pre-apply bytes, modes, and symlink targets. Assert transaction metadata remains when rollback itself fails.

- [ ] **Step 3: Run transaction tests and verify failure**

Run: `go test ./session/workspace -run 'TestApplyTransaction|TestApplyRollback|TestIncompleteTransaction' -v`

Expected: FAIL because manifest execution and recovery metadata are absent.

- [ ] **Step 4: Implement prepare and revalidation**

Materialize merged blobs under the transaction directory, save the manifest atomically, and compare every touched source entry to the `OURS` snapshot immediately before writes. Return a conflict-style error if any entry changed.

- [ ] **Step 5: Implement apply and rollback**

Back up touched entries, write regular files to temporary siblings followed by `os.Rename`, create validated symlinks without dereferencing targets, and perform deletions after backup. On any failure, restore in reverse manifest order and report both errors with `errors.Join`.

- [ ] **Step 6: Implement completion and recovery discovery**

Mark successful transactions committed, update `ShadowData.LastAppliedTree` and timestamp, then remove completed transaction payloads. Scan and report nonterminal manifests on restore; never auto-delete them.

- [ ] **Step 7: Run transaction and workspace tests**

Run: `go test ./session/workspace -v`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add session/workspace/apply.go session/workspace/apply_test.go session/workspace/shadow.go session/workspace/shadow_test.go
git commit -m "feat: apply directory changes transactionally"
```

### Task 8: Integrate Directory Capabilities into the TUI

**Files:**
- Modify: `app/app.go`
- Modify: `app/app_test.go`
- Modify: `app/help.go`
- Modify: `keys/keys.go`
- Modify: `ui/menu.go`
- Modify: `ui/list.go`
- Modify: `ui/preview.go`
- Modify: `ui/list_test.go`
- Create: `ui/menu_test.go`

**Interfaces:**
- Consumes: Task 5 `WorkspaceMode`, `Capabilities`, `Apply`, and `DisplayWorkspace`.
- Produces: capability-driven menu choices and mode-specific help/copy.

- [ ] **Step 1: Write menu and list rendering tests**

For a Git instance, assert push and checkout remain. For a directory instance, assert `p push branch` is absent, `c apply` is present, `Directory (isolated)` is shown, and no internal shadow branch/path is exposed.

- [ ] **Step 2: Run UI tests and verify failure**

Run: `go test ./ui -run 'TestMenu|TestDirectoryInstance' -v`

Expected: FAIL because the UI ignores workspace capabilities.

- [ ] **Step 3: Implement capability-driven UI labels**

Add `KeyApply` using the existing `c` key. Build menu options from `instance.Capabilities()`. Render `DisplayWorkspace()` and mode instead of assuming every subtitle is a branch. Update paused fallback and start/general help for directory semantics.

- [ ] **Step 4: Write app flow tests**

Test that directory mode skips `git.FetchBranches`/branch search, `p` is ignored, and `c` opens “Apply changes from session … to …?” confirmation. Test an `ApplyResult`, a `ConflictError`, and a general apply error are returned to Bubble Tea as messages rather than discarded.

- [ ] **Step 5: Run app tests and verify failure**

Run: `go test ./app -run 'TestDirectory|TestConfirmAction' -v`

Expected: FAIL until handlers route by capability and confirmation returns action messages.

- [ ] **Step 6: Implement app behavior**

Skip branch fetch/picker when the current mode is directory. Route `p` only when `Push=true`. Route `c` to native pause/help in Git mode and confirmed `Instance.Apply()` followed by pause in directory mode. Add an apply-result/conflict overlay. Change confirmation execution to enqueue the action's returned `tea.Msg` rather than discard it.

- [ ] **Step 7: Run app and UI tests**

Run: `go test ./app ./ui ./keys -v`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add app/app.go app/app_test.go app/help.go keys/keys.go ui/menu.go ui/menu_test.go ui/list.go ui/list_test.go ui/preview.go
git commit -m "feat: add directory workspace actions to the TUI"
```

### Task 9: Add Shadow Cleanup to Reset

**Files:**
- Modify: `session/workspace/shadow.go`
- Modify: `session/workspace/shadow_test.go`
- Modify: `main.go`

**Interfaces:**
- Produces: `workspace.CleanupShadowProjects() error`.
- Consumes: canonical containment helpers from Tasks 3–4.

- [ ] **Step 1: Write cleanup safety tests**

Create valid shadow project directories and a malicious metadata record pointing outside the config root. Assert valid internal paths are removed, external paths and source directories survive, and the function reports the rejected path.

- [ ] **Step 2: Run cleanup tests and verify failure**

Run: `go test ./session/workspace -run 'TestCleanupShadowProjects' -v`

Expected: FAIL because cleanup is absent.

- [ ] **Step 3: Implement safe global cleanup**

Canonicalize the configured shadow root and every candidate. Remove only descendants of that root, aggregate errors, and never traverse metadata-provided source paths. Call it from `resetCmd` after stored instances are cleared and before reporting completion.

- [ ] **Step 4: Run tests**

Run: `go test ./session/workspace . -run 'TestCleanupShadowProjects|TestReset' -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add session/workspace/shadow.go session/workspace/shadow_test.go main.go
git commit -m "feat: clean shadow workspaces on reset"
```

### Task 10: Add the `ygl` Startup Mark

**Files:**
- Modify: `ui/consts.go`
- Create: `ui/consts_test.go`
- Modify: `ui/preview.go`

**Interfaces:**
- Produces: `RenderFallbackLogo(width int) string`.
- Consumes: fallback rendering width from `PreviewPane.String()`.

- [ ] **Step 1: Write logo rendering tests**

Assert a wide rendering includes lowercase `ygl` on the final visible logo line, uses no extra line, and has visible width at most the requested width. Assert a width narrower than the logo omits `ygl` and does not wrap.

```go
func TestRenderFallbackLogoAddsYGLWhenWide(t *testing.T) {
    rendered := RenderFallbackLogo(80)
    require.Contains(t, stripANSI(rendered), "ygl")
    require.LessOrEqual(t, lipgloss.Width(rendered), 80)
}
```

- [ ] **Step 2: Run test and verify failure**

Run: `go test ./ui -run 'TestRenderFallbackLogo' -v`

Expected: FAIL because the renderer does not exist.

- [ ] **Step 3: Implement responsive mark rendering**

Keep the existing ASCII art unchanged. Add a low-contrast adaptive Lip Gloss style and compose `ygl` at the lower-right of the logo's bounding box. `PreviewPane.setFallbackState` calls `RenderFallbackLogo(p.width)`; omit the mark when it would wrap or reduce logo width.

- [ ] **Step 4: Run UI tests**

Run: `go test ./ui -v`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add ui/consts.go ui/consts_test.go ui/preview.go
git commit -m "feat: add ygl mark to startup logo"
```

### Task 11: Document Directory Mode and Verify End-to-End Behavior

**Files:**
- Modify: `README.md`
- Create: `session/workspace/e2e_test.go`

**Interfaces:**
- Consumes: all prior tasks.
- Produces: user-facing usage, limitations, ignore-file format, and automated acceptance coverage.

- [ ] **Step 1: Write the end-to-end non-Git test**

Use a temporary HOME and source directory. Through workspace/session APIs, start two directory sessions with mocked tmux, modify the same file differently, verify isolated diffs, apply one, verify source contents, then apply the other and assert `ConflictError` with no additional source mutation. Assert the source has no `.git`.

- [ ] **Step 2: Run the end-to-end test and fix integration gaps**

Run: `go test ./session/workspace -run 'TestDirectoryWorkspaceEndToEnd' -v`

Expected: PASS after resolving only integration defects exposed by the acceptance flow.

- [ ] **Step 3: Update README**

Document automatic Git/directory mode detection, shadow storage location, explicit Apply behavior, conflict guarantees, shared dependency caveat, default generated exclusions, and `.claude-squad-ignore` syntax. Keep existing Git instructions intact.

- [ ] **Step 4: Run formatting and complete verification**

Run:

```bash
gofmt -w main.go app session keys ui
go test ./...
go vet ./...
git diff --check
```

Expected: all commands exit 0.

- [ ] **Step 5: Manually smoke-test both startup modes**

Build with `go build -o /tmp/claude-squad-ygl .`. Run `/tmp/claude-squad-ygl` once in a temporary initialized Git repository and once in a temporary plain directory. Verify the TUI starts, mode labels and actions differ correctly, and the fallback logo displays `ygl` on a sufficiently wide terminal. Exit without creating external state beyond the temporary directories and normal Claude Squad config state.

- [ ] **Step 6: Commit**

```bash
git add README.md session/workspace/e2e_test.go
git commit -m "docs: explain isolated directory workspaces"
```

### Task 12: Final Review and Readiness Check

**Files:**
- Review all files changed since `5fb4205`.

**Interfaces:**
- Consumes: completed implementation and tests.
- Produces: review findings resolved and a clean branch ready for user-selected integration.

- [ ] **Step 1: Inspect the complete diff**

Run:

```bash
git status --short
git diff --stat 5fb4205..HEAD
git diff 5fb4205..HEAD
git log --oneline 5fb4205..HEAD
```

Expected: only scoped workspace, UI, test, and README changes are present.

- [ ] **Step 2: Review security invariants explicitly**

Confirm from tests and code that cleanup cannot escape the config root, scans do not follow symlinks, conflicts do not touch source, final revalidation closes the scan/apply race, rollback covers creates/updates/deletes, and directory mode never creates source `.git` metadata.

- [ ] **Step 3: Run final verification**

Run:

```bash
go test ./...
go test -race ./session/... ./app/... ./ui/...
go vet ./...
git diff --check
git status --short --branch
```

Expected: tests and vet exit 0; no uncommitted implementation changes remain.

- [ ] **Step 4: Request code review**

Invoke `superpowers:requesting-code-review` against the complete diff from `5fb4205` and address any correctness, safety, or scope findings with focused tests and new commits.

- [ ] **Step 5: Present integration options**

Invoke `superpowers:finishing-a-development-branch`. Do not push, open a pull request, or merge until the user explicitly chooses that action.
