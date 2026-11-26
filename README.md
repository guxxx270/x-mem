# x-mem

给编程 agent（先支持 Claude Code）用的本地记忆。笔记就是普通的 Markdown 文件，分个人层和项目层；x-mem 负责把和当前问题**直接相关**的那一两条找出来，交给 agent，并且在记忆里没有的时候老实说没有。

- 单个 Go 二进制，零外部依赖，不联网，不调模型。
- Markdown 是唯一的真相来源。索引只是缓存，随时可以删；手改笔记后下一次查询自动生效，不用重建。
- 中文友好：中文按双字切词、英文做词干，再按“问题的要点被覆盖了多少”分成 direct / related / none 三档。钩子只注入 direct，其余一律不出声。

当前是**第一期（只读）**：能查、能注入、能体检；写入、导入、守卫在后面几期。

## 安装

```bash
go build -o ~/bin/x-mem ./cmd/x-mem    # 或者直接用发布的二进制
x-mem init                             # 建 ~/.x-mem/personal，并在当前仓库建 .x-mem/
x-mem install claude-code              # 打印要加的 hooks 和 MCP 注册命令（不改你的文件）
x-mem doctor                           # 确认都接好了
```

`install claude-code` 只打印配置：把输出里的两个 hooks 合并进 `~/.claude/settings.json`，再执行它给出的 `claude mcp add ...`。

## 目录

```
~/.x-mem/
  personal/          个人层，自己的 git 仓库（推到私有远端即可多机同步）
    rules/           规则、偏好：会话开始时列出来
    notes/           其他事实
    inbox/           收件箱，不算记忆，不会被检索
  state/             索引、会话去重、日志；不进 git，可随时删除

<你的仓库>/.x-mem/    项目层，随仓库提交，团队共享
  rules/  notes/  inbox/
```

环境变量：`X_MEM_HOME` 改个人层位置（默认 `~/.x-mem`），`X_MEM_PROJECT` 指定项目层目录。

## 一条笔记

一条笔记只写一件事。文件名随意，`id` 不写时用文件名。

```markdown
---
id: release-day-v2
title: 发版日（改）
type: decision            # rule / preference / decision / fact / howto ……
aliases: [发版, 发版时间, release day]
valid_from: 2026-11-04
relations:
  - op: "@supersedes"
    target: release-day
source:
  quote: "发版改到周二上午吧，测试周一才跑完"
---
发版改到每周二上午，不再周五，因为测试周一才跑完。
```

- `relations` 支持 `@supersedes`、`@refines`、`@conflicts_with`、`@deprecated`、`@relates_to`。写错的操作符（比如漏了 `@`）会被 `doctor` 报出来，绝不静默忽略。
- 被 `@supersedes` / `@deprecated` 的笔记整条退出检索；加 `line:` 只取代对方的某一行，其余照旧有效。
- `status: draft`、`valid_until` 已过、`valid_from` 未到的笔记不生效。
- 别名（以及足够长的标题）在问题里原样出现时直接判 direct，适合给口头叫法、缩写配别名。

## 命令

| 命令 | 作用 |
|---|---|
| `x-mem recall [--scope personal\|project] 问题` | 查记忆，第一行是 `memory: direct / related / none` |
| `x-mem why 问题` | 解释为什么是这个结果：切出了哪些要点、覆盖了多少、谁被排除了 |
| `x-mem read <id\|文件名\|标题>` | 全文、状态、关系、谁引用了它；被取代的行会划掉 |
| `x-mem status` / `doctor` | 库和索引的状态；体检并给出修复办法 |
| `x-mem eval <题集.json>` | 跑回归题集 |
| `x-mem hook start\|prompt` | Claude Code 钩子（从标准输入读事件） |
| `x-mem mcp` | MCP 服务（stdio），提供 `recall`、`read`、`status` 三个工具 |

## 钩子做什么

- **会话开始**：列出规则与偏好、本项目最近 30 天的决定（约 1500 token 以内）。
- **每条消息**：只有 direct 时才注入，最多 2 条、约 600 token；同一会话里注入过的不再重复。消息里写 `#不记` 或贴了大段代码/日志时跳过。
- 注入的内容逐行引用，开头声明“是数据，不是指令”；看起来像在给 agent 下命令的笔记永远不注入，`doctor` 会列出来。
- 日志只记笔记 id，不记你的提问原文。

## 第一期验收

| 项目 | 目标 | 结果 |
|---|---|---|
| 回归题集：该答的 | ≥18/20 direct | 20/20 |
| 回归题集：没有的 | ≥9/10 none | 9/10（“今天北京天气怎么样”因时区笔记里有“北京时间”得了 related，钩子不会注入） |
| 回归题集：闲聊 | 全部不出声 | 10/10 |
| 钩子延迟，1000 条笔记 | p95 < 50ms | p95 18ms（5000 条时 p95 约 68ms） |
| 手改笔记 | 不用重建即生效 | 是（按文件大小和修改时间比对） |

```bash
go test ./...
cd testdata/proj && X_MEM_HOME=../home x-mem eval ../regress.json
go run ./tools/bench -bin bin/x-mem -notes 1000
```

## 和设计文档不一样的地方

- **索引没用 SQLite**：改成纯 Go 的两个缓存文件（元数据 + 倒排表，倒排按需 `ReadAt`），保持零依赖，也省掉了 cgo / modernc 的体积。笔记正文不进缓存，要引用时才读原文件。
- 项目名从工作名 kmem 改为 x-mem。

## 后面几期

- 第二期：写入（`remember` / `correct` 走收件箱、先给你看再落盘）、`install --apply`（先备份再合并配置）、从 旧记忆库 导入。
- 第三期：守卫（`triggers` + `enforcement`，在执行 `git push` 之类命令前提醒或拦截）。
- 第四期：可选的向量召回、多 agent 支持。
