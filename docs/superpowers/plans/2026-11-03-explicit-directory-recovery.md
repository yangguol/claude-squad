# Explicit Directory Recovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let users explicitly clear validated cleanup tombstones and resume a blocked isolated-directory session, while showing truthful actions and recovery details.

**Architecture:** Add a narrowly scoped recovery operation to directory workspaces that accepts only retained orphan directories containing exactly the validated `.cleanup-complete` marker, removes them under the existing per-project lock, reruns recovery, and updates cached access state. Expose this through the existing `r resume` lifecycle action; when safe cleanup is required, explain it in a confirmation before resuming. Non-recoverable blocked states remain read-only with diagnostic text and no actionable key.

**Tech Stack:** Go, Bubble Tea, tmux, existing `session/workspace` recovery and project-lock primitives, testify.

**Spec:** `docs/superpowers/specs/2026-09-21-arbitrary-directory-workspaces-design.md`

## Global Constraints

- Multiple `cs` processes remain supported; coordination uses the existing non-blocking per-project lock.
- Never delete an unvalidated transaction, manifest, capture, source path, or arbitrary retained directory.
- Explicit recovery may delete only a retained orphan whose directory name and sole `.cleanup-complete` marker pass existing descriptor-relative validation.
- Git workspace behavior is unchanged.
- Existing `c apply & pause` copy remains unchanged.

## Review Focus

- Another `cs` process holds the project lock: recovery reports contention and changes nothing.
- A retained orphan contains extra files or a forged marker: recovery remains blocked and preserves it.
- Mixed blocked state (valid tombstone plus another recovery issue): do not offer or perform recover-and-resume.
- Recovery succeeds but tmux restore/start fails: tombstone stays cleared, session remains paused, and the start error is shown.
- Ordinary paused Git and directory sessions still render and execute plain `r resume`.

---

### Task 1: Safely acknowledge retained cleanup tombstones

**Files:**
- Modify: `session/workspace/workspace.go`
- Modify: `session/workspace/shadow.go`
- Modify: `session/workspace/recovery.go`
- Modify: `session/workspace/recovery_orphan_unix.go`
- Modify: `session/workspace/recovery_orphan_unsupported.go`
- Test: `session/workspace/shadow_test.go`
- Test: `session/workspace/recovery_test.go`

**Interfaces:**
- Produces: `Capabilities.Recover bool`.
- Produces: `Workspace.Recover() error`.
- Directory `Recover()` updates the workspace’s cached `AccessState`; Git returns `ErrUnsupported`.

- [ ] **Step 1: Write failing capability tests**

Add tests proving `Recover` is true only when every blocking item is a validated retained orphan cleanup tombstone, and false for normal, legacy, malformed, cleanup-blocked, recovery-blocked, and mixed states.

- [ ] **Step 2: Run capability tests and verify RED**

Run: `go test ./session/workspace -run 'TestRestoreRetainedOrphan|TestRecoverCapability' -count=1`

Expected: FAIL because `Capabilities.Recover` does not exist.

- [ ] **Step 3: Add minimal recovery classification**

Track whether recovered read-only access consists exclusively of validated retained orphan tombstones. Publish that as `Capabilities.Recover` without weakening `Apply` or `Resume`.

- [ ] **Step 4: Write failing mutation tests**

Add tests asserting `Recover()`:
- removes one or multiple validated tombstones under the project lock;
- reruns recovery and clears read-only access;
- refuses malformed/mixed states without mutation;
- returns `OperationInProgressError` under lock contention;
- is unsupported on platforms without atomic directory recovery.

- [ ] **Step 5: Run mutation tests and verify RED**

Run: `go test ./session/workspace -run 'TestRecoverRetainedOrphans' -count=1`

Expected: FAIL because `Workspace.Recover()` does not exist.

- [ ] **Step 6: Implement minimal `Recover()`**

Reuse existing descriptor-relative retained-orphan validation and removal. Acquire the canonical project lock once, validate all targets before deleting any, remove only validated tombstones, rerun `recoverProjectLocked`, and update `w.access` only from the resulting report.

- [ ] **Step 7: Verify workspace tests**

Run: `go test ./session/workspace -run 'TestRecover|TestRestoreRetainedOrphan' -count=1`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add session/workspace/workspace.go session/workspace/shadow.go session/workspace/recovery.go session/workspace/recovery_orphan_unix.go session/workspace/recovery_orphan_unsupported.go session/workspace/shadow_test.go session/workspace/recovery_test.go
git commit -m "feat(workspace): add explicit tombstone recovery"
```

### Task 2: Recover and resume from the instance lifecycle

**Files:**
- Modify: `session/instance.go`
- Test: `session/instance_test.go`

**Interfaces:**
- Consumes: `Workspace.Recover() error`, `Capabilities.Recover` from Task 1.
- Produces: `(*Instance).RecoverAndResume() error`.

- [ ] **Step 1: Write failing lifecycle tests**

Test that recoverable paused directory instances call `Recover()` then `Resume()`, stop before resume when recovery fails, and remain paused when tmux start fails after successful recovery. Verify ordinary `Resume()` behavior remains unchanged.

- [ ] **Step 2: Run lifecycle tests and verify RED**

Run: `go test ./session -run 'TestRecoverAndResume' -count=1`

Expected: FAIL because `RecoverAndResume` does not exist.

- [ ] **Step 3: Implement `RecoverAndResume()`**

Reject instances without `Capabilities.Recover`, call workspace recovery once, then invoke the existing resume path. Do not duplicate setup or tmux logic.

- [ ] **Step 4: Verify lifecycle tests**

Run: `go test ./session -run 'TestRecoverAndResume|TestResume' -count=1`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add session/instance.go session/instance_test.go
git commit -m "feat(session): recover blocked directory sessions"
```

### Task 3: Render and handle truthful recovery actions

**Files:**
- Modify: `keys/keys.go`
- Modify: `ui/menu.go`
- Modify: `ui/menu_test.go`
- Modify: `ui/preview.go`
- Modify: `ui/preview_test.go`
- Modify: `app/app.go`
- Test: `app/app_test.go`

**Interfaces:**
- Consumes: `Capabilities.Recover`, `Instance.AccessState()`, and `Instance.RecoverAndResume()`.
- Produces: recoverable and ordinary paused sessions both show `r resume`; recoverable sessions explain cleanup in a confirmation; unrecoverable states show no `r` action and display the access message plus retained paths.

- [ ] **Step 1: Write failing menu and preview tests**

Add focused fixtures with configurable `AccessState`. Assert exact labels and preview copy for ordinary paused, recoverable tombstone, malformed/mixed blocked, and Git sessions. Blocked previews must not say “Press 'r'” unless the action exists.

- [ ] **Step 2: Run UI tests and verify RED**

Run: `go test ./ui -run 'TestMenu.*Recover|TestPreview.*Recover|TestPreview.*Blocked' -count=1`

Expected: FAIL because recovery UI does not exist and blocked preview always advertises `r`.

- [ ] **Step 3: Implement capability-driven labels and diagnostics**

Add an internal `KeyRecover` action on `r` with the same user-facing help text `resume`. Select it only for paused instances with `Recover`; retain `KeyResume` for ordinary resumable instances. Render `AccessState.Message`, affected paths, and retained paths for blocked non-recoverable sessions.

- [ ] **Step 4: Write failing app flow tests**

Test that pressing `r` on recoverable sessions prompts for confirmation, confirmation invokes `RecoverAndResume`, errors are surfaced, and ordinary resume remains immediate. Verify Enter remains disabled while paused.

- [ ] **Step 5: Run app tests and verify RED**

Run: `go test ./app -run 'Test.*Recover.*Resume' -count=1`

Expected: FAIL because the handler has no recovery branch.

- [ ] **Step 6: Implement confirmed recover-and-resume handling**

Map `r` to `KeyRecover` when the selected instance advertises recovery, show a confirmation explaining that the previous cleanup was incomplete and safe retained metadata will be cleared, execute recovery asynchronously through Bubble Tea, save state after success, and refresh menu/preview. Keep per-instance pending-action protection.

- [ ] **Step 7: Verify UI and app tests**

Run: `go test ./ui ./app -run 'Test.*(Recover|Resume|Blocked|DirectoryActions)' -count=1`

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add keys/keys.go ui/menu.go ui/menu_test.go ui/preview.go ui/preview_test.go app/app.go app/app_test.go
git commit -m "fix(ui): expose explicit directory recovery"
```

### Task 4: Full regression verification

**Files:**
- Verify only.

**Interfaces:**
- Consumes all prior tasks.
- Produces a verified branch with no regressions.

- [ ] **Step 1: Format changed Go files**

Run: `gofmt -w session/workspace/workspace.go session/workspace/shadow.go session/workspace/recovery.go session/workspace/recovery_orphan_unix.go session/workspace/recovery_orphan_unsupported.go session/workspace/shadow_test.go session/workspace/recovery_test.go session/instance.go session/instance_test.go keys/keys.go ui/menu.go ui/menu_test.go ui/preview.go ui/preview_test.go app/app.go app/app_test.go`

- [ ] **Step 2: Run full test suite**

Run: `go test ./...`

Expected: PASS with zero failures.

- [ ] **Step 3: Run race-focused lifecycle tests**

Run: `go test -race ./session/workspace ./session ./app ./ui`

Expected: PASS with no race reports.

- [ ] **Step 4: Review diff scope**

Run: `git diff --stat && git diff --check`

Expected: only planned files changed; `git diff --check` exits 0.
