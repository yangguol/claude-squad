# Task 8 Report

## Status

Implemented capability-driven directory workspace behavior in the TUI. Directory startup skips Git branch fetch/search/picker, directory sessions expose Apply instead of push/checkout, mixed restored modes render their own mode and display workspace, and confirmed Apply results are propagated through Bubble Tea and shown as success, no-op, conflict, or error overlays.

## Evidence

### TDD red

`go test ./ui -run 'TestMenu|TestDirectoryInstance' -v`

FAIL at compile time because `keys.KeyApply` did not exist, proving the menu had no directory-specific Apply action.

The first focused app run also caught the menu-highlight resend behavior in the tests and the confirmation overlay's intentional line wrapping; tests were corrected to exercise the second key delivery and semantic message fragments.

### Focused UI tests

`go test ./ui -run 'TestMenu|TestDirectoryInstance' -v`

PASS. Covers retained Git push/checkout actions, directory Apply labeling, hidden directory push/checkout actions, and source/mode rendering without internal shadow branch or worktree leakage.

### Focused app/UI/keys suite

`go test ./app ./ui ./keys -count=1`

PASS (`app` 15.768s, `ui` 6.768s; `keys` has no test files). Covers directory prompt creation without a branch picker, ignored push input, Apply confirmation, success/no-op/conflict/error messages, confirmation message propagation, shared-dependency help, mixed-mode list rendering, and existing UI behavior.

### Uncached full suite

`go test -count=1 ./...`

PASS. All repository packages passed; `session/workspace` completed in 113.063s.

### Static patch check

`git diff --check`

PASS with no output.

## Implementation

- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/app/app.go`: detects startup workspace mode, skips Git branch fetch/search for directory creation, gates push by capabilities, routes `c` to confirmed Apply for directory sessions, pauses and persists after successful Apply, propagates confirmation commands, and renders typed result overlays.
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/app/help.go`: provides capability-aware general/start help and documents that shared dependency directories are not isolated.
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/keys/keys.go`: adds `KeyApply` on the existing `c` key with the `apply` label.
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/ui/menu.go`: derives push/apply/checkout/resume choices from the selected instance's capabilities and state.
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/ui/list.go`: renders `Git` or `Directory (isolated)` with the public display workspace.
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/ui/preview.go`: removes branch-checkout copy from paused directory sessions.
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/ui/overlay/textInput.go`: supports prompt/profile input without a branch picker.
- `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/app/app_test.go`, `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/ui/list_test.go`, and `/Users/yangguoliang/myExplore/cs/.worktrees/arbitrary-directory-workspaces/ui/menu_test.go`: add focused capability, rendering, Apply flow, help, and message-propagation coverage.

## Self-review

- Git behavior remains available only where `Push` is true; directory `p` is ignored and directory `c` maps to Apply.
- Apply executes only after explicit confirmation. Errors and conflicts do not pause the session; successful and no-op results pause it and save updated session/workspace metadata.
- Confirmation callbacks no longer execute and discard action messages. The callback queues the command and `handleKeyPress` returns it to Bubble Tea.
- Directory-facing text uses `DisplayWorkspace()` and never exposes the shadow branch or internal worktree path; mixed restored sessions are evaluated independently.
- Directory prompt overlays preserve profile selection while omitting branch controls and searches.
- No startup logo changes were made.

## Concerns

- Startup mode is detected once from the process working directory, matching current session creation behavior where all new sessions use `Path: "."`.
- Apply and pause currently run in the returned Bubble Tea command; large applies remain off the event loop, but no progress spinner is introduced in this task.

## Commit

`8e44ee3 feat: add directory workspace actions to the TUI`

## Fix Round 1

### Status

Fixed confirmed-action concurrency and ownership, corrected Apply persistence/pause semantics, made help capability-driven, and replaced stub-only coverage with command-to-message-to-Update flow tests.

### Exact evidence

- `go test ./app -run 'TestKillCommand|TestApply|TestGeneralHelp|TestDirectoryApply' -count=1 -v` — PASS. Covers real workspace Apply dispatch, success/no-op/conflict/error rendering, pre/post-pause saves, save and pause failures, stale selection, and duplicate Apply/Kill suppression.
- `go test ./app ./ui ./keys -count=1` — PASS (`app` 24.637s, `ui` 7.264s; `keys` has no test files).
- `go test -count=1 ./...` — PASS. All packages passed; `session/workspace` completed in 122.444s.
- `git diff --check` — PASS with no output.

### Fix details

- Confirmed Apply/Kill commands now perform backend work only and return target-specific typed messages. List, terminal, status, and storage mutations happen synchronously in `Update` handlers.
- Pending action tracking prevents duplicate Apply/Kill operations for one target. Result handlers retain the original target, so selection changes cannot pause or delete a different session.
- Apply metadata is saved after source mutation and before pause cleanup, then paused status is saved after cleanup. Persistence failure stops cleanup and leaves the session available; pause failure is reported separately while retaining recoverable state.
- Apply overlays distinguish source conflicts/errors from successful source mutation followed by persistence or pause-cleanup failures; successful no-op wording no longer implies changed files.
- General and start help derive Push and Apply independently. Nil selection advertises neither push nor checkout/apply.
- `Instance.PreparePause` separates backend pause cleanup from synchronous status mutation while preserving the existing `Pause` API behavior.

### Concerns

- A successful backend kill followed by persistence deletion failure intentionally leaves the in-memory entry visible and reports that it stopped but could not be removed from persisted state; this is recoverable on retry/restart.

## Fix Round 2

### Status

Fixed successful Apply terminal cleanup ownership and partial-pause state reporting. A physically removed workspace is now paused even when prune reports an error; the paused state is persisted, the original instance's terminal is cleaned synchronously in `Update`, and the cleanup error remains visible.

### Exact evidence

- TDD red: `go test ./session -run TestPreparePauseReportsPausedWhenRemoveSucceededButPruneFailed -count=1` — FAIL because `PreparePause` returned `paused=false` after workspace removal succeeded and prune failed.
- `go test ./app -run 'TestKillCommand|TestApply|TestGeneralHelp|TestDirectoryApply' -count=1 -v` — PASS (`claude-squad/app` 16.159s). Includes command→message→Update coverage for successful Apply terminal cleanup against the original target, post-pause save failure, `paused=true` plus pause error, and the retained success/no-op/conflict/error/stale/duplicate paths.
- `go test ./session -run 'TestPreparePause|TestPause' -count=1 -v` — PASS (`claude-squad/session` 0.434s). Covers prune-after-remove partial success and validation failure.
- `go test ./app ./ui ./keys -count=1` — PASS (`app` 28.703s, `ui` 7.241s; `keys` has no test files).
- `go test -count=1 ./...` — PASS. All repository packages passed; `session/workspace` completed in 133.385s.
- `git diff --check` — PASS with no output.

### Fix details

- Successful and partially successful pauses clean the terminal cache only in the synchronous result handler and use the original action target's title, independent of current selection.
- A prune failure after successful removal returns `paused=true` with the error. The app sets and saves `Paused`, cleans the terminal, and reports the pause-cleanup failure without claiming the session remains ready.
- Failures before workspace removal continue to return `paused=false`; the session remains in its actual recoverable running/ready state.

### Concerns

- A post-pause persistence failure leaves the correct in-memory `Paused` state and reports the error, but disk state can remain pre-pause until a later successful save or shutdown save.
