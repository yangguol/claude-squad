# Claude Squad [![CI](https://github.com/smtg-ai/claude-squad/actions/workflows/build.yml/badge.svg)](https://github.com/smtg-ai/claude-squad/actions/workflows/build.yml) [![GitHub Release](https://img.shields.io/github/v/release/smtg-ai/claude-squad)](https://github.com/smtg-ai/claude-squad/releases/latest)

[Claude Squad](https://smtg-ai.github.io/claude-squad/) is a terminal app that manages multiple [Claude Code](https://github.com/anthropics/claude-code), [Codex](https://github.com/openai/codex), [Gemini](https://github.com/google-gemini/gemini-cli) (and other local agents including [Aider](https://github.com/Aider-AI/aider)) in separate workspaces, allowing you to work on multiple tasks simultaneously.


![Claude Squad Screenshot](assets/screenshot.png)

### Highlights
- Complete tasks in the background (including yolo / auto-accept mode!)
- Manage instances and tasks in one terminal window
- Review changes before applying them, checkout changes before pushing them
- Each task gets its own isolated git workspace, so no conflicts

<br />

https://github.com/user-attachments/assets/aef18253-e58f-4525-9032-f5a3d66c975a

<br />

### Installation

Both Homebrew and manual installation will install Claude Squad as `cs` on your system.

#### Homebrew

```bash
brew install claude-squad
ln -s "$(brew --prefix)/bin/claude-squad" "$(brew --prefix)/bin/cs"
```

#### Manual

Claude Squad can also be installed by running the following command:

```bash
curl -fsSL https://raw.githubusercontent.com/smtg-ai/claude-squad/main/install.sh | bash
```

This puts the `cs` binary in `~/.local/bin`.

To use a custom name for the binary:

```bash
curl -fsSL https://raw.githubusercontent.com/smtg-ai/claude-squad/main/install.sh | bash -s -- --name <your-binary-name>
```

### Prerequisites

- [tmux](https://github.com/tmux/tmux/wiki/Installing)
- [gh](https://cli.github.com/)

### Usage

```
Usage:
  cs [flags]
  cs [command]

Available Commands:
  completion  Generate the autocompletion script for the specified shell
  debug       Print debug information like config paths
  help        Help about any command
  reset       Reset all stored instances
  version     Print the version number of claude-squad

Flags:
  -y, --autoyes          [experimental] If enabled, all instances will automatically accept prompts for claude code & aider
  -h, --help             help for claude-squad
  -p, --program string   Program to run in new instances (e.g. 'aider --model ollama_chat/gemma3:1b')
```

Run the application from the project directory:

```bash
cs
```

Claude Squad detects the workspace mode automatically. Inside a Git repository it keeps the existing Git worktree and branch workflow. In any other directory it shows `Directory (isolated)` and gives each session an independent copy backed by data under `~/.claude-squad/shadow-projects/`; it does not create `.git` in your source directory.

In directory mode, agent edits stay isolated until you select the session and explicitly press `c` to **Apply**. Apply performs a three-way merge against the session's starting snapshot and the current source. If the same content changed incompatibly, Claude Squad reports the conflicting paths and leaves the source unchanged; resolve the source or session edit before retrying. In Git mode, `c` checks out the session branch and `p` commits and pushes it; push and branch checkout are unavailable in directory mode.

Directory Apply is supported on Darwin and Linux when the source directory and transaction storage under `~/.claude-squad/shadow-projects/` are on the same filesystem and that filesystem supplies the required atomic no-replace rename and durable directory operations. Unsupported platforms or filesystems fail before an Apply manifest is published or the source is mutated; there is no degraded rename fallback. Apply, recovery, resume, kill, acknowledgement, and reset serialize through a per-project lock. If another Claude Squad process holds it, the operation reports that another operation is in progress rather than proceeding concurrently. This coordination does not claim protection against arbitrary hostile processes that directly rewrite source or transaction storage.

An interrupted pre-commit Apply is automatically discovered and rolled back on restore; a durably committed Apply is completed and its private transaction material is cleaned. If recovery cannot prove that a path is transaction-owned, Claude Squad preserves the questionable data, reports the affected and retained paths, and restores the session read-only: Apply and resume remain blocked until the filesystem conflict is resolved and recovery succeeds. A successful Apply may likewise carry a cleanup warning after its source changes are committed; do not delete the listed retained paths blindly. Inspect them, remove or relocate only the external blocker, then restart or retry recovery. Complete transaction receipts may be acknowledged through the application once they are safe to remove.

Directory mode classifies files as included, excluded, shared, or generated. Included files are isolated and can be applied. Excluded and generated files are neither copied nor applied. Shared dependency directories are linked to the source to avoid expensive copies, so writes inside them are immediately visible to every session and are not isolated or applied. By default, `node_modules/`, `.venv/`, and `venv/` are shared; common build output, cache, editor, and OS-artifact paths are generated and omitted (`dist/`, `build/`, `target/`, `out/`, `.cache/`, Python caches, `.idea/`, `.vscode/`, `.DS_Store`, `Thumbs.db`, `Desktop.ini`, and `*.sw?`).

To customize directory mode, create `.claude-squad-ignore` in the source root. It uses gitignore-style glob patterns, `/` for root-relative paths, a trailing `/` for directories, `#` for comments, and `!` to include a later exception. Patterns before a section are excluded; `[shared]` and `[generated]` change the category for following patterns. Later matching rules win.

```gitignore
# Excluded: not copied or applied
.env
/private/
!/private/example.env

[shared]
vendor/

[generated]
coverage/
*.tmp
```

Killing a session removes its isolated worktree. `cs reset` removes stored sessions and cleans only unreferenced directory-mode shadow projects that pass the same locked recovery preflight; it preserves blocked, incomplete, or otherwise questionable transaction state for later recovery instead of deleting it. The startup fallback logo also displays a subtle `ygl` mark when the terminal is wide enough.

NOTE: The default program is `claude` and we recommend using the latest version.

<br />

<b>Using Claude Squad with other AI assistants:</b>
- For [Codex](https://github.com/openai/codex): Set your API key with `export OPENAI_API_KEY=<your_key>`
- Launch with specific assistants:
   - Codex: `cs -p "codex"`
   - Aider: `cs -p "aider ..."`
   - Gemini: `cs -p "gemini"`
- Make this the default, by modifying the config file (locate with `cs debug`)

<br />

#### Menu
The menu at the bottom of the screen shows available commands: 

##### Instance/Session Management
- `n` - Create a new session
- `N` - Create a new session with a prompt
- `D` - Kill (delete) the selected session
- `↑/j`, `↓/k` - Navigate between sessions

##### Actions
- `↵/o` - Attach to the selected session to reprompt
- `ctrl-q` - Detach from session
- `p` - Commit and push the branch to GitHub (Git mode only)
- `c` - Checkout: commit changes and pause the session (Git mode), or Apply changes to the source directory and pause (directory mode)
- `r` - Resume a paused session
- `?` - Show help menu

##### Navigation
- `tab` - Switch between preview tab and diff tab
- `q` - Quit the application
- `shift-↓/↑` - scroll in diff view

### Configuration

Claude Squad stores its configuration in `~/.claude-squad/config.json`. You can find the exact path by running `cs debug`.

#### Profiles

Profiles let you define multiple named program configurations and switch between them when creating a new session. When more than one profile is defined, the session creation overlay shows a profile picker that you can navigate with `←`/`→`.

To configure profiles, add a `profiles` array to your config file and set `default_program` to the name of the profile to select by default:

```json
{
  "default_program": "claude",
  "profiles": [
    { "name": "claude", "program": "claude" },
    { "name": "codex", "program": "codex" },
    { "name": "aider", "program": "aider --model ollama_chat/gemma3:1b" }
  ]
}
```

Each profile has two fields:

| Field     | Description                                              |
|-----------|----------------------------------------------------------|
| `name`    | Display name shown in the profile picker                 |
| `program` | Shell command used to launch the agent for that profile  |

If no profiles are defined, Claude Squad uses `default_program` directly as the launch command (the default is `claude`).

### FAQs

#### Failed to start new session

If you get an error like `failed to start new session: timed out waiting for tmux session`, update the
underlying program (ex. `claude`) to the latest version.

### How It Works

1. **tmux** to create isolated terminal sessions for each agent
2. **git worktrees** for Git repositories, or copied shadow workspaces for arbitrary directories
3. A simple TUI interface for easy navigation and management

### License

[AGPL-3.0](LICENSE.md)

### Star History

[![Star History Chart](https://api.star-history.com/svg?repos=smtg-ai/claude-squad&type=Date)](https://www.star-history.com/#smtg-ai/claude-squad&Date)
