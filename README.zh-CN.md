# Claude Squad — 在任意目录中运行隔离的 AI Agent

[English](README.md) | **简体中文**

[![CI](https://github.com/yangguol/claude-squad/actions/workflows/build.yml/badge.svg)](https://github.com/yangguol/claude-squad/actions/workflows/build.yml)
[![License: AGPL-3.0](https://img.shields.io/badge/license-AGPL--3.0-blue.svg)](LICENSE.md)

从**任意目录**并行运行多个相互隔离的 AI 编程 Agent——无需 Git 仓库。

- **Agent 相互隔离** — 每个 Agent 都在自己的私有工作区中运行。
- **保持源目录整洁** — 普通目录中不会出现 `.git` 目录。
- **安全应用变更** — 先审查，再通过冲突检测和崩溃恢复机制合并。

支持的 Agent 包括 [Claude Code](https://github.com/anthropics/claude-code)、[Codex](https://github.com/openai/codex)、[Gemini CLI](https://github.com/google-gemini/gemini-cli)、[Aider](https://github.com/Aider-AI/aider) 及其他终端程序。

![Claude Squad 截图](assets/screenshot.png)

## 为什么选择这个版本？

| 能力 | 上游 Claude Squad | 本项目 |
|---|---:|---:|
| 在 Git 仓库中运行 Agent | 支持 | 支持 |
| 在普通目录中运行 Agent | 不支持 | 支持 |
| 不向普通目录添加 `.git` | — | 支持 |
| 审查并显式应用目录变更 | — | 支持 |
| 三方合并与冲突检测 | — | 支持 |
| 崩溃安全的 Apply 与恢复 | — | 支持 |

在 Git 仓库中，Claude Squad 使用原生 Git worktree 和分支。在普通目录中，它会在 `~/.claude-squad/` 下创建内部影子 Git 项目。只有在你明确执行 Apply 后，Agent 的变更才会写入源目录。

本项目是 [smtg-ai/claude-squad](https://github.com/smtg-ai/claude-squad) 的独立延续版本。它保留了上游的多 Agent TUI、tmux 集成、Git 分支工作流、diff 预览、配置方案和自动确认模式，并新增：

- **从任意目录运行** — 自动识别 Git 仓库和普通目录。
- **每个 Agent 使用独立工作区** — 普通目录使用私有影子分支和 worktree。
- **源目录中不出现 `.git`** — 影子元数据保存在 `~/.claude-squad/shadow-projects/`。
- **显式 Apply** — 普通目录中的变更只有确认后才会写入源目录。
- **三方合并** — Apply 会比较会话基线、当前源目录和 Agent 结果。
- **冲突安全** — 不兼容的修改会报告排序后的冲突路径，且不会改动源目录。
- **崩溃安全事务** — 持久化预写日志支持回滚和重启恢复。
- **故障关闭式恢复** — 无法确认的文件会被保留，而不会被覆盖或删除。
- **暂停与恢复** — 可在保留会话分支的同时删除和重建 worktree。
- **能力感知 UI** — 目录会话中会隐藏仅适用于 Git 的操作，阻塞状态下的会话为只读。

## 环境要求

- [Git](https://git-scm.com/) — Git 和目录工作区都需要。
- [tmux](https://github.com/tmux/tmux/wiki/Installing) — 用于承载每个 Agent 的终端。
- [Go 1.23+](https://go.dev/doc/install)，并可使用 Go 1.25.8 工具链 — 从源码构建本项目时需要。
- 你要运行的 Agent 命令，例如 `claude`、`codex`、`gemini` 或 `aider`。
- [GitHub CLI（`gh`）](https://cli.github.com/) — 可选；Git 模式下推送到 GitHub 或打开 GitHub 工作流时需要。

### 平台支持

| 功能 | Darwin | Linux | Windows / 其他平台 |
|---|---:|---:|---:|
| Git 仓库工作区 | 支持 | 支持 | 现有 Git/tmux 工作流可运行的平台均可使用 |
| 普通目录工作区 | 支持 | 支持 | 暂不支持 |
| 事务式 Apply | 支持 | 支持 | 不支持 |

目录模式依赖项目锁和原子文件系统操作，目前仅在 Darwin 和 Linux 上实现。应用变更还要求源目录与 `~/.claude-squad/shadow-projects/` 中的事务存储位于同一文件系统。若后续文件系统检查失败，会话创建、编辑和 diff 仍可使用，但 Apply 会在发布事务清单或修改源目录之前失败。

## 安装

在本项目发布自己的 Release 构建产物之前，请从源码构建：

```bash
git clone https://github.com/yangguol/claude-squad.git
cd claude-squad
go build -o cs .
mkdir -p ~/.local/bin
install -m 0755 cs ~/.local/bin/cs
```

请确保 `~/.local/bin` 已加入 `PATH`。

上游 Homebrew formula 安装的是上游 Release，可能不包含本项目的任意目录功能。

## 快速开始

在希望 Agent 处理的目录中运行 Claude Squad：

```bash
cd /path/to/project
cs
```

按 `n` 创建会话；按 `N` 创建会话并同时提供初始提示词。

Claude Squad 会自动选择工作区模式：

- Git 仓库内的路径使用 **Git 模式**。
- Darwin/Linux 上其他任何可读目录使用**目录（隔离）模式**。

使用 `-p` 指定其他 Agent 命令：

```bash
cs -p "codex"
cs -p "gemini"
cs -p "aider --model <model>"
```

使用 `-y` 为新会话启用实验性的自动确认设置：

```bash
cs -y
```

## 工作区模式

### Git 模式

Git 模式保留 Claude Squad 原有工作流：

1. 每个会话获得一个原生 Git worktree 和分支。
2. Agent 的修改保留在该分支中。
3. `c` 提交变更、暂停会话，并使分支可供 checkout。
4. `p` 提交并将分支推送到 GitHub。
5. `r` 重建已暂停的 worktree 并恢复 Agent。

### 目录模式

目录模式会为源目录创建一个稳定的内部项目：

```text
~/.claude-squad/shadow-projects/<project-id>/
├── repo/                  # 内部 Git 仓库和提交
├── metadata.json          # 规范化的源目录标识
├── worktrees/<session-id>/
└── transactions/          # Apply WAL、暂存及回滚数据
```

对于每个会话，Claude Squad 会：

1. 扫描源目录并记录不可变基线。
2. 创建私有的 `session/<id>` 分支和 worktree。
3. 在该 worktree 中运行 Agent。
4. 展示 Agent 的 diff，但不修改源目录。
5. 仅在你按下 `c` 并确认后应用已接受的变更。

因此，在同一个普通目录中创建的多个 Agent 不会修改彼此的 worktree，也不会直接覆盖源目录。

## 应用目录会话的变更

目录 Apply 使用三方合并：

- **BASE** — 会话启动时不可变的初始快照。
- **OURS** — 当前源目录的最新快照。
- **THEIRS** — 所选 Agent 的工作区。

这样可以保留会话启动后对源目录所做的独立修改。如果 OURS 和 THEIRS 对同一内容做了不兼容的修改，Apply 会在修改源目录前报告冲突路径。处理源目录或会话中的冲突后即可重试。

一次成功的 Apply 会：

1. 获取项目级锁；
2. 校验平台、文件系统、源目录标识和事务状态；
3. 构建并持久发布事务清单；
4. 使用原子、禁止覆盖的文件系统操作执行每个计划操作；
5. 持久记录提交点；
6. 从持久事务记录中返回已应用的文件树和提交时间；
7. 尝试保存上述元数据、暂停会话并清理私有事务材料；提交后的持久化、暂停或清理问题会被报告，但不会回滚已经提交到源目录的变更。

不受支持的文件系统上不会使用不安全的 rename 降级方案。

## 崩溃恢复与阻塞状态

Claude Squad 启动时会先检查持久化的目录模式事务，再恢复会话。

- 中断的**提交前** Apply 会自动回滚。
- 已持久**提交**的 Apply 会继续完成并清理，且不会回滚源目录变更。
- 如果恢复过程无法证明某个路径属于该事务，项目会进入 `recovery-blocked` 状态。
- 如果源目录变更已提交，但私有数据无法安全清理，Apply 会成功并发出警告，同时事务进入 `cleanup-blocked` 状态。

阻塞恢复会刻意采用故障关闭策略：

- 保留存在疑问的文件和事务数据；
- 在可用时报告受影响及保留的路径；
- 恢复后的会话为只读；
- 在外部文件系统冲突解决且恢复成功前，Apply 和 Resume 始终不可用。

不要盲目删除保留的路径。请先检查报告的阻塞原因。一个常见情况是：外部文件占用了恢复过程需要原子还原的路径。

创建、还原、Apply、恢复、Resume 和 reset 使用非阻塞的项目级锁。如果另一个 Claude Squad 进程持有该锁，当前操作会立即失败，而不是并发修改。Kill/工作区清理不在此锁范围内；该锁也无法防止任意外部程序直接改写文件。

## 文件包含策略

目录模式将源路径分为：

- **included** — 复制到每个会话中，并可参与 Apply；
- **excluded** — 不复制，也不参与 Apply；
- **shared** — 以符号链接指向源目录，所有会话立即可见；
- **generated** — 不参与隔离和 Apply。

默认共享目录：

```text
node_modules/
.venv/
venv/
```

默认生成路径包括常见构建输出、缓存、编辑器状态和操作系统文件，例如 `dist/`、`build/`、`target/`、`out/`、`.cache/`、Python 缓存、`.idea/`、`.vscode/`、`.DS_Store`、`Thumbs.db`、`Desktop.ini` 和 `*.sw?`。

共享路径不会被隔离。其中的写入会立即对源目录及其他会话可见，并且永远不会通过 Apply 处理。

可在源目录根目录使用 `.claude-squad-ignore` 自定义策略。它使用 gitignore 风格的模式：`/` 锚定根目录，末尾 `/` 匹配目录，`#` 开始注释，`!` 添加后续包含例外，后出现的匹配规则优先。`[shared]` 和 `[generated]` 会更改其后模式的分类。

```gitignore
# 排除
.env
/private/
!/private/example.env

[shared]
vendor/

[generated]
coverage/
*.tmp
```

## 会话生命周期

### 暂停与恢复

暂停操作会提交会话中的未提交修改、断开 tmux 客户端、删除会话 worktree，并保留其分支。Resume 会重建或校验 worktree，然后重新连接或启动 tmux。如果重启或执行 `tmux kill-server` 后，已存储会话的 tmux 进程消失，该会话会以暂停状态恢复，之后可以 Resume。

### Kill

`D` 会关闭选中的 tmux 会话，并删除该会话的 worktree 和分支。随后直接删除持久化会话记录，不会尝试恢复已删除的工作区。如果其他会话或保留的恢复数据仍引用共享的目录模式项目，该项目可能继续保留。

### Reset

```bash
cs reset
```

Reset 是全局且故障关闭的操作。它会按规范顺序锁定所有已发现的影子项目，并首先执行无副作用的分类和可行性检查。如果任何项目格式异常、正忙、处于 `recovery-blocked` 或存在其他不安全状况，reset 会中止且不会删除 `state.json` 中的记录。所有项目通过分类后，reset 可以在持有这些锁时完成可恢复事务；只有这样，它才会删除已存储的会话，并清理 tmux 会话、原生 worktree、安全的影子项目和守护进程。

## 键盘快捷键

底部菜单会显示当前所选会话可用的操作。

### 会话

| 按键 | 操作 |
|---|---|
| `n` | 创建会话 |
| `N` | 创建带初始提示词的会话 |
| `D` | 终止所选会话 |
| `↑` / `k` | 选择上一个会话 |
| `↓` / `j` | 选择下一个会话 |
| `J` / `K` | 调整会话顺序 |
| `Enter` / `o` | 连接到所选会话 |
| `Ctrl-q` | 从已连接的 tmux 会话断开 |

### 交接

| 按键 | Git 模式 | 目录模式 |
|---|---|---|
| `c` | 提交、暂停并准备 checkout 分支 | 确认 Apply 到源目录，然后尝试保存状态并暂停 |
| `p` | 提交并推送分支 | 不可用 |
| `r` | 恢复符合条件的已暂停会话 | 恢复符合条件且未阻塞的已暂停会话 |

### 导航

| 按键 | 操作 |
|---|---|
| `Tab` | 在预览、diff 和终端标签页间切换 |
| `Shift-↑` / `Shift-↓` | 滚动预览、diff 或终端内容 |
| `?` | 打开帮助 |
| `q` | 退出 Claude Squad |

如果内嵌终端显示类似 `[0/31]` 的计数器并停止接收输入，说明 tmux 正处于复制模式。按 `q`（或 `Esc`）退出复制模式；除非确实要中断 Agent，否则请避免按 `Ctrl-c`。

## 配置与持久化数据

运行 `cs debug` 可输出当前配置文件路径及配置值。

```text
~/.claude-squad/config.json                  # 程序及配置方案
~/.claude-squad/state.json                   # 持久化会话和 UI 状态
~/.claude-squad/shadow-projects/             # 普通目录项目
~/.claude-squad/shadow-projects/.locks/      # 项目级操作锁
```

### 配置方案

配置方案（Profiles）用于定义命名的 Agent 命令。存在多个配置方案时，创建浮层允许使用 `←` 和 `→` 进行选择。

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

没有配置方案时，会直接使用 `default_program`。默认命令为 `claude`。

## CLI

```text
用法：
  cs [flags]
  cs [command]

命令：
  completion  生成 shell 补全
  debug       输出配置路径和值
  help        显示命令帮助
  reset       安全重置已存储的会话和工作区
  version     输出版本

选项：
  -p, --program string   在新会话中运行的程序
  -y, --autoyes          启用实验性的自动提示确认
  -h, --help             显示帮助
```

## 故障排查

### 会话启动失败

更新所选 Agent 命令，并确认已安装 Git 和 tmux。如果启动过程报告 tmux 超时，请确认该 Agent 命令在 Claude Squad 外部可以正常启动。

### 目录模式不可用

目录模式目前需要 Darwin 或 Linux。请检查 `~/.claude-squad/` 是否可写，以及是否有其他 Claude Squad 进程持有该项目的锁。

### Apply 报告跨设备或不支持的文件系统错误

源目录和 `~/.claude-squad/shadow-projects/` 必须位于同一文件系统，并且该文件系统必须提供所需的原子和持久化操作。请移动 Claude Squad 的配置存储或源目录，使二者使用同一个受支持的文件系统；不要绕过安全检查。

### Apply 报告冲突

会话创建后，源目录发生了与 Agent 结果冲突的修改。失败的 Apply 不会修改源目录。请检查列出的路径，协调其中一方的修改，然后重试。

### 启动时报告 `recovery-blocked` 或 `cleanup-blocked`

请保留事务目录。检查受影响及保留的路径，仅删除或移动外部阻塞项，然后重启，使恢复过程重新评估该事务。除非你明确接受丢失保留的恢复数据，否则不要删除整个影子项目。

## 工作原理

Claude Squad 结合了：

1. 使用 **tmux** 提供持久化的交互式 Agent 终端；
2. 对 Git 仓库使用**原生 Git worktree**；
3. 对普通目录使用**影子 Git 项目**；
4. 使用**三方 Git merge plumbing** 规划目录 Apply；
5. 使用**基于文件描述符的文件系统操作和持久化 WAL**，确保源目录修改具备崩溃安全性；
6. 使用 **Bubble Tea TUI** 管理会话、预览、diff 和终端访问。

已实现架构的详细说明位于 [`docs/superpowers/specs/2026-09-21-arbitrary-directory-workspaces-design.md`](docs/superpowers/specs/2026-09-21-arbitrary-directory-workspaces-design.md)。

当终端宽度足够时，启动/降级 Logo 中会包含一个细微的 `ygl` 标记。

## 许可证

[AGPL-3.0](LICENSE.md)
