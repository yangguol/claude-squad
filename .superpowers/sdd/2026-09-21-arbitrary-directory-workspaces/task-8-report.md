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
