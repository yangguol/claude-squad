# Claude Squad — Isolated AI Agents for Any Directory

**English** | [简体中文](README.zh-CN.md)

[![CI](https://github.com/yangguol/claude-squad/actions/workflows/build.yml/badge.svg)](https://github.com/yangguol/claude-squad/actions/workflows/build.yml)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](LICENSE.md)

Run multiple isolated AI coding agents from **any directory** — no Git repository required.

- **Keep agents isolated** — every agent works in its own private workspace.
- **Keep your source clean** — ordinary directories never receive a `.git` directory.
- **Apply changes safely** — review first, then merge with conflict detection and crash recovery.

Supported agents include [Claude Code](https://github.com/anthropics/claude-code), [Codex](https://github.com/openai/codex), [Gemini CLI](https://github.com/google-gemini/gemini-cli), [Aider](https://github.com/Aider-AI/aider), and other terminal programs.

![Claude Squad Screenshot](assets/screenshot.png)

## Why this version?

| Capability | Upstream Claude Squad | This project |
|---|---:|---:|
| Run agents in Git repositories | Yes | Yes |
| Run agents in ordinary directories | No | Yes |
| Keep ordinary directories free of `.git` | — | Yes |
| Review and explicitly apply directory changes | — | Yes |
| Three-way merge and conflict detection | — | Yes |
| Crash-safe Apply and recovery | — | Yes |

In a Git repository, Claude Squad uses a native Git worktree and branch. In an ordinary directory, it creates an internal shadow Git project under `~/.claude-squad/`. Agent changes remain isolated until you explicitly apply them.

This project is an independent continuation of [smtg-ai/claude-squad](https://github.com/smtg-ai/claude-squad). It retains the upstream multi-agent TUI, tmux integration, Git branch workflow, diff preview, profiles, and auto-accept mode while adding:

- **Run from any directory** — Git repositories and ordinary directories are detected automatically.
- **One isolated workspace per agent** — ordinary directories use private shadow branches and worktrees.
- **No `.git` in your source directory** — shadow metadata lives under `~/.claude-squad/shadow-projects/`.
- **Explicit Apply** — ordinary-directory changes reach the source only after confirmation.
- **Three-way merge** — Apply compares the session baseline, current source, and agent result.
- **Conflict safety** — incompatible edits report sorted conflict paths and leave the source unchanged.
- **Crash-safe transactions** — a durable write-ahead log supports rollback and restart recovery.
- **Fail-closed recovery** — uncertain files are retained instead of being overwritten or deleted.
- **Pause and resume** — worktrees can be removed and recreated while session branches are preserved.
- **Capability-aware UI** — Git-only actions are hidden from directory sessions and blocked sessions are read-only.

## Requirements

- [Git](https://git-scm.com/) — required for both Git and directory workspaces.
- [tmux](https://github.com/tmux/tmux/wiki/Installing) — hosts each agent terminal.
- [Go 1.23+](https://go.dev/doc/install) with the Go 1.25.8 toolchain available — required to build this project from source.
- The agent command you want to run, such as `claude`, `codex`, `gemini`, or `aider`.
- [GitHub CLI (`gh`)](https://cli.github.com/) — optional; needed for Git-mode GitHub push/open workflows.

### Platform support

| Feature | Darwin | Linux | Windows / other platforms |
|---|---:|---:|---:|
| Git-repository workspaces | Yes | Yes | Available where the existing Git/tmux workflow works |
| Ordinary-directory workspaces | Yes | Yes | Not currently supported |
| Transactional Apply | Yes | Yes | Not supported |

Directory mode requires project locking and atomic filesystem operations currently implemented only on Darwin and Linux. Applying changes also requires the source directory and `~/.claude-squad/shadow-projects/` transaction storage to be on the same filesystem. Session creation, editing, and diff remain available when the filesystem check fails later, but Apply fails before publishing a transaction manifest or modifying the source.

## Installation

Until this project publishes its own release artifacts, build it from source:

```bash
git clone https://github.com/yangguol/claude-squad.git
cd claude-squad
go build -o cs .
mkdir -p ~/.local/bin
install -m 0755 cs ~/.local/bin/cs
```

Ensure `~/.local/bin` is on your `PATH`.

The upstream Homebrew formula installs the upstream release and may not contain this project's arbitrary-directory features.

## Quick start

Run Claude Squad from the directory you want agents to work on:

```bash
cd /path/to/project
cs
```

Create a session with `n`, or create one and provide its initial prompt with `N`.

Claude Squad chooses the workspace automatically:

- A path inside a Git repository uses **Git mode**.
- Any other readable directory uses **Directory (isolated) mode** on Darwin/Linux.

Use another agent command with `-p`:

```bash
cs -p "codex"
cs -p "gemini"
cs -p "aider --model <model>"
```

Use `-y` to pass the experimental auto-accept setting to new sessions:

```bash
cs -y
```

## Workspace modes

### Git mode

Git mode retains the original Claude Squad workflow:

1. Each session receives a native Git worktree and branch.
2. Agent edits stay on that branch.
3. `c` commits changes, pauses the session, and makes the branch available for checkout.
4. `p` commits and pushes the branch to GitHub.
5. `r` recreates a paused worktree and resumes the agent.

### Directory mode

Directory mode creates a stable internal project for the source directory:

```text
~/.claude-squad/shadow-projects/<project-id>/
├── repo/                  # internal Git repository and commits
├── metadata.json          # canonical source identity
├── worktrees/<session-id>/
└── transactions/          # Apply WAL, stage, and rollback data
```

For each session Claude Squad:

1. Scans the source and records an immutable baseline.
2. Creates a private `session/<id>` branch and worktree.
3. Runs the agent inside that worktree.
4. Shows the agent's diff without changing the source.
5. Applies accepted changes only when you press `c` and confirm.

Multiple agents created from the same ordinary directory therefore do not edit each other's worktrees or directly overwrite the source.

## Applying directory-session changes

Directory Apply uses a three-way merge:

- **BASE** — the session's immutable starting snapshot.
- **OURS** — a fresh snapshot of the current source directory.
- **THEIRS** — the selected agent workspace.

This preserves independent source edits made after the session started. If OURS and THEIRS changed the same content incompatibly, Apply reports the conflicting paths before source mutation. Resolve either the source or session changes, then retry.

A successful Apply:

1. acquires the per-project lock;
2. validates platform, filesystem, source identity, and transaction state;
3. builds and durably publishes a transaction manifest;
4. applies each planned operation with atomic, no-replace filesystem operations;
5. durably records the commit point;
6. returns the applied tree and commit time from the durable transaction record;
7. attempts to persist that metadata, pause the session, and clean private transaction material; any post-commit persistence, pause, or cleanup problem is reported without rolling back committed source changes.

There is no unsafe rename fallback on unsupported filesystems.

## Crash recovery and blocked states

At startup, Claude Squad examines durable directory-mode transactions before restoring sessions.

- An interrupted **pre-commit** Apply is rolled back automatically.
- A durably **committed** Apply is completed and cleaned without rolling back source changes.
- If recovery cannot prove a path belongs to the transaction, the project becomes `recovery-blocked`.
- If source changes committed but private cleanup cannot safely finish, Apply succeeds with a warning and the transaction becomes `cleanup-blocked`.

Blocked recovery is deliberately fail-closed:

- questionable files and transaction data are retained;
- affected and retained paths are reported when available;
- restored sessions are read-only;
- Apply and Resume remain unavailable until the external filesystem conflict is resolved and recovery succeeds.

Do not blindly delete retained paths. Inspect the reported blocker first. A common example is an external file occupying a path that recovery needs to restore atomically.

Creation, restore, Apply, recovery, Resume, and reset use a non-blocking per-project lock. If another Claude Squad process owns it, the operation fails immediately instead of mutating concurrently. Kill/workspace cleanup is outside that lock, and the lock does not defend against arbitrary external programs rewriting files directly.

## File inclusion policy

Directory mode classifies source paths as:

- **included** — copied into each session and eligible for Apply;
- **excluded** — not copied or applied;
- **shared** — symlinked to the source and visible immediately to every session;
- **generated** — omitted from isolation and Apply.

Default shared directories:

```text
node_modules/
.venv/
venv/
```

Default generated paths include common build outputs, caches, editor state, and OS artifacts such as `dist/`, `build/`, `target/`, `out/`, `.cache/`, Python caches, `.idea/`, `.vscode/`, `.DS_Store`, `Thumbs.db`, `Desktop.ini`, and `*.sw?`.

Shared paths are not isolated. Writes inside them are immediately visible to the source and other sessions, and they are never applied.

Customize the policy with `.claude-squad-ignore` in the source root. It uses gitignore-style patterns: `/` anchors at the root, a trailing `/` matches directories, `#` starts comments, `!` adds a later inclusion exception, and later matching rules win. `[shared]` and `[generated]` change the category for following patterns.

```gitignore
# Excluded
.env
/private/
!/private/example.env

[shared]
vendor/

[generated]
coverage/
*.tmp
```

## Session lifecycle

### Pause and Resume

Pausing commits dirty session changes, detaches the tmux client, removes the session worktree, and preserves its branch. Resume recreates or validates the worktree and reconnects or starts tmux. If a stored session's tmux process disappeared after a reboot or `tmux kill-server`, it is restored as paused so it can be resumed.

### Kill

`D` closes the selected tmux session and removes that session's worktree and branch. It then removes the persisted session record directly without trying to restore the already-deleted workspace. The shared directory-mode project may remain while other sessions or retained recovery data still reference it.

### Reset

```bash
cs reset
```

Reset is global and fail-closed. It locks all discovered shadow projects in canonical order and first performs side-effect-free classification and feasibility checks. If any project is malformed, busy, `recovery-blocked`, or otherwise unsafe, reset aborts without deleting `state.json` records. After every project passes classification, reset may complete recoverable transactions under those locks; only then does it delete stored sessions and clean tmux sessions, native worktrees, safe shadow projects, and the daemon.

## Keyboard shortcuts

The bottom menu shows actions valid for the selected session.

### Sessions

| Key | Action |
|---|---|
| `n` | Create a session |
| `N` | Create a session with an initial prompt |
| `D` | Kill the selected session |
| `↑` / `k` | Select previous session |
| `↓` / `j` | Select next session |
| `J` / `K` | Reorder sessions |
| `Enter` / `o` | Attach to the selected session |
| `Ctrl-q` | Detach from an attached tmux session |

### Handoff

| Key | Git mode | Directory mode |
|---|---|---|
| `c` | Commit, pause, and prepare branch checkout | Confirm Apply to source, then attempt to persist and pause |
| `p` | Commit and push branch | Unavailable |
| `r` | Resume an eligible paused session | Resume an eligible paused, non-blocked session |

### Navigation

| Key | Action |
|---|---|
| `Tab` | Cycle preview, diff, and terminal tabs |
| `Shift-↑` / `Shift-↓` | Scroll preview, diff, or terminal content |
| `?` | Open help |
| `q` | Quit Claude Squad |

If the embedded terminal shows a counter such as `[0/31]` and stops accepting input, tmux is in copy mode. Press `q` (or `Esc`) to exit copy mode; avoid `Ctrl-c` unless you intend to interrupt the agent.

## Configuration and persisted data

Run `cs debug` to print the active configuration path and values.

```text
~/.claude-squad/config.json                  # program and profile configuration
~/.claude-squad/state.json                   # persisted sessions and UI state
~/.claude-squad/shadow-projects/             # ordinary-directory projects
~/.claude-squad/shadow-projects/.locks/      # per-project operation locks
```

### Profiles

Profiles define named agent commands. When multiple profiles exist, the creation overlay lets you choose one with `←` and `→`.

```json
{
  "default_program": "claude",
  "profiles": [
    { "name": "claude", "program": "claude" },
    { "name": "codex", "program": "codex" },
    { "name": "gemini", "program": "gemini" },
    { "name": "aider", "program": "aider --model <model>" }
  ]
}
```

Without profiles, `default_program` is used directly. The default command is `claude`.

## CLI

```text
Usage:
  cs [flags]
  cs [command]

Commands:
  completion  Generate shell completion
  debug       Print configuration paths and values
  help        Show command help
  reset       Safely reset stored sessions and workspaces
  version     Print the version

Flags:
  -p, --program string   Program to run in new sessions
  -y, --autoyes          Enable experimental automatic prompt acceptance
  -h, --help             Show help
```

## Troubleshooting

### A session fails to start

Update the selected agent command and verify that Git and tmux are installed. If startup reports a tmux timeout, confirm the agent command launches normally outside Claude Squad.

### Directory mode is unavailable

Directory mode currently requires Darwin or Linux. Check that `~/.claude-squad/` is writable and that no other Claude Squad process holds the project's lock.

### Apply reports a cross-device or unsupported-filesystem error

The source and `~/.claude-squad/shadow-projects/` must be on the same filesystem, and that filesystem must provide the required atomic and durable operations. Move Claude Squad's configuration storage or the source so they share a supported filesystem; do not bypass the safety check.

### Apply reports conflicts

The source was changed after session creation in a way that conflicts with the agent result. The source is unchanged by the failed Apply. Review the listed paths, reconcile one side, and retry.

### Startup reports `recovery-blocked` or `cleanup-blocked`

Preserve the transaction directory. Inspect the affected and retained paths, remove or relocate only the external blocker, then restart so recovery can re-evaluate the transaction. Do not delete the entire shadow project unless you intentionally accept losing retained recovery data.

## How it works

Claude Squad combines:

1. **tmux** for persistent interactive agent terminals;
2. **native Git worktrees** for repositories;
3. **shadow Git projects** for ordinary directories;
4. **three-way Git merge plumbing** for directory Apply planning;
5. **descriptor-bound filesystem operations and a durable WAL** for crash-safe source mutation;
6. **a Bubble Tea TUI** for session management, previews, diffs, and terminal access.

The detailed implemented architecture is documented in [`docs/superpowers/specs/2026-09-21-arbitrary-directory-workspaces-design.md`](docs/superpowers/specs/2026-09-21-arbitrary-directory-workspaces-design.md).

The startup/fallback logo includes a subtle `ygl` mark when the terminal is wide enough.

## License

[AGPL-3.0](LICENSE.md)
