# x-mem

给编程 agent 用的本地记忆，**Claude Code、Codex、Antigravity、Claude 桌面版共用一份**。笔记就是普通的 Markdown 文件，分个人层和项目层；x-mem 负责把和当前问题**直接相关**的那一两条找出来交给 agent，记忆里没有的时候老实说没有；你说过的决定可以记下来、纠正、撤回；规则能在动作执行前提醒或拦下；Obsidian 库、放 Word/PDF/Excel 的资料文件夹可以登记成知识库，能查、带出处（PDF 带页码），但不算你定过的事。

- 单个 Go 二进制，零外部依赖，不联网。x-mem 自己不调模型；需要提炼时借你已登录的 Codex（默认）或 Antigravity 命令行。
- Markdown 是唯一的真相来源。索引只是缓存，随时可以删；手改笔记后下一次查询自动生效。
- 中文友好：中文按双字切词、英文做词干，再按“问题的要点被覆盖了多少”分成 direct / related / none 三档。钩子只注入 direct，其余一律不出声。

## 安装

```bash
go build -o ~/.x-mem/bin/x-mem ./cmd/x-mem   # 或者用 bin/ 里的预编译版本
x-mem init                                   # 建 ~/.x-mem/personal，并在当前仓库建 .x-mem/
x-mem install all --apply                    # 本机装了哪个接哪个：Claude Code、Codex、Antigravity、Claude 桌面版
x-mem doctor                                 # 确认都接好了
```

不加 `--apply` 只列出要改什么，不改文件；改之前每个文件都先备份。重复运行会替换旧的 x-mem 条目，不会重复添加；`--remove` 拿掉；`--drop` 顺手把 旧记忆库 的钩子和 MCP 从这几个应用的配置里拿掉（只动配置，不删 旧记忆库 的文件）。

## 三端互通

几个应用用的是同一个程序、同一份 `~/.x-mem`，在哪个应用里记下或纠正的，其他应用马上就能查到。项目层按工作目录找：钩子从应用拿到当前目录；MCP 工具多了一个 `workspace` 参数，应用把工作区路径传进来。

| | Claude Code | Codex | Antigravity |
|---|---|---|---|
| 配置位置 | `~/.claude/settings.json` | `~/.codex/hooks.json`、`config.toml`、`AGENTS.md` | `~/.gemini/config/hooks.json`、`mcp_config.json`、`AGENTS.md` |
| 会话开始给目录 | SessionStart | SessionStart | SessionStart |
| 每条消息想起 | UserPromptSubmit | UserPromptSubmit | PreInvocation（它没有提交消息的钩子；每条消息只处理一次） |
| 动作前规则 | PreToolUse：提醒注入上下文，block 拒绝 | 同左（`apply_patch` 按 Edit 匹配） | PreToolUse：block 拒绝；它不能在动作前加上下文，所以提醒规则是“先拦一次、把规则给 agent，再执行就放行” |
| 会话采集 | Stop | Stop | Stop |
| MCP（recall / read / propose / inbox / status） | `claude mcp add` | `codex mcp add` | `mcp_config.json` |

- **Claude 桌面版**没有钩子，只接 MCP（`claude_desktop_config.json` 里的 `mcpServers.x-mem`）；怎么用记忆写在 MCP 服务连上时下发的说明里。桌面版开着的时候会把设置整份留在内存里、退出时写回，这时改配置会被覆盖，所以 `x-mem install claude-desktop --apply` 发现它开着就不改文件，在后台等它退出后再改，下次打开就接上了（想马上生效就 ⌘Q 退出再打开）。旧记忆库 如果是以扩展装进桌面版的，要在 设置 → 扩展 里卸载。桌面版的对话不存在本机，提案的原话核对不上，会进收件箱等你确认（见下文）。
- **Codex** 只运行你信任过的钩子：装好后在 Codex 里信任一次（CLI 输入 `/hooks`；App 在 设置 → Hooks）。信任记录按钩子在 `hooks.json` 里的位置算，位置变了（比如删掉了排在前面的 旧记忆库 钩子）要重新信任。`x-mem doctor` 会指出哪几个还没信任。没信任时 MCP 仍然可用（Codex 会按 AGENTS.md 主动 recall），只是每条消息的自动想起和动作前规则不生效。
- **Antigravity** 的 PreToolUse 必须回 `{"decision":"allow"}` 才放行，回 `{}` 等于拒绝；x-mem 0.4.1 起在没命中规则、出错、关闭时都明确回 allow（0.4.0 有这个问题，会拦下所有工具调用）。Antigravity 第一次调 x-mem 的 MCP 工具时会问你允不允许，选“always allow”以后就不问了。
- 规则的触发条件三端通用：`Bash(git push*)` 在 Codex 里匹配它的 shell，在 Antigravity 里匹配 `run_command`；`Edit(*.sql)` 也匹配 Codex 的 `apply_patch` 和 Antigravity 的改文件工具。
- 原话核对和会话采集读三家各自的会话文件（`~/.claude/projects`、`~/.codex/sessions`、`~/.gemini/antigravity*/brain`），自动跳过 `claude -p`、`codex exec`、`agy -p` 这类无头运行；x-mem 发给整理助手的提示带一个标记，那段会话永远不会被当成你说的话。

### 从 旧记忆库 换过来

```bash
sh scripts/install.sh ~/Documents/知识库
```

依次：安装到 `~/.x-mem/bin` → 建个人层 → 从 旧记忆库 库导入 → 库里没导入的部分（时间日志、会话摘录、资料）登记成知识库 → 接入三端 → 运行 旧记忆库 自带的卸载脚本，再用 `x-mem install all --apply --drop` 清掉卸载脚本漏下的 旧记忆库 钩子和 MCP（Claude Code、Codex、Antigravity、Claude 桌面版，改之前都备份）。旧记忆库 的库和钥匙文件都不动。已经迁移过、只想清 旧记忆库 的残留，单独跑这一条就行。加 `--keep-legacy` 只导入、不停用。

导入规则（也可单独运行 `x-mem import obsidian [库目录] --dry-run` 预演）：

- 用户偏好页按条拆开，一条一个规则，放进 `rules/imported/`；
- `01 项目`、`02 经验与方法` 等分区的其他页，一页一条笔记，放进 `notes/imported/`，保持原路径；
- 别名表里的别名带过来；从 Codex 会话自动提炼的页只保留五个汉字以上的别名（短的英文别名太泛，会误命中）；
- 时间日志、会话摘录、`90 系统文件`、目录页、没改过的 旧记忆库 示例页不导入；
- 已经存在的文件不覆盖（`--force` 才覆盖），个人层是 git 仓库时自动提交一次。

## 目录

```
~/.x-mem/
  bin/x-mem          程序
  config.json        可选设置（见下）
  personal/          个人层，自己的 git 仓库（推到私有远端即可多机同步）
    rules/           规则、偏好：会话开始时列出来
    notes/           其他事实、决定、教训
    inbox/           给你自己放草稿的收件箱，不算记忆，不会被检索
  state/             索引、提案、写入记录、会话游标、日志；不进 git

<你的仓库>/.x-mem/    项目层，随仓库提交，团队共享
```

环境变量：`X_MEM_HOME` 改位置（默认 `~/.x-mem`），`X_MEM_PROJECT` 指定项目层。

## 一条笔记

一条笔记只写一件事，正文不超过 2000 字。手写的笔记只要有正文就能用，其余字段都可选。

```markdown
---
id: 1NGGPGXSE7
title: 发版日（改）
type: decision            # decision / rule / preference / fact / lesson / howto
aliases: [发版, 发版时间]
valid_from: 2026-11-04
relations:
  - op: "@supersedes"
    target: release-day
    line: "我们每周五下午发版"   # 可选：只取代对方的这一行
source:
  quote: "发版改到周二上午吧，测试周一才跑完"
  session: claude:4f2a9c1e
---
发版改到每周二上午，不再周五，因为测试周一才跑完。
```

- 关系：`@supersedes` 取代、`@refines` 细化、`@conflicts_with` 冲突、`@deprecated` 作废、`@relates_to` 相关。写错（比如漏了 `@`）会被拒绝或在 `doctor` 里报出来，绝不静默忽略；成环也会报。
- 被取代或作废的笔记整条退出检索；带 `line` 只取代对方那一行，同页其他内容照旧有效。
- `status: draft`、`valid_until` 已过、`valid_from` 未到的笔记不生效。
- 规则可以带触发条件：`triggers: ["Bash(git push*)"]`，`enforcement: advise`（提醒，默认）或 `block`（拦下）。
- `sensitivity: confidential` 只进个人层，自动写进 `.gitignore`，不推远端。

## 写入、纠正、撤回

所有写入走同一条路：**先成提案，过了检查才由写入进程落盘**。

```bash
x-mem remember --kind decision "订单服务的数据库用 PostgreSQL 16"
x-mem remember --kind rule --trigger 'Bash(git push*)' "推送前先跑 make test，红了不许推"
x-mem remember --supersedes 1NGGPGXSE7 "订单服务数据库改用 MySQL 8"              # 纠正
x-mem remember --supersedes team --line "发版放在每周五下午" "发版改到每周二上午"  # 只改一行
x-mem inbox                    # 待你确认的提案
x-mem inbox review             # 逐条过：y 写入 / n 不要 / s 跳过（像已有笔记的，r 取代它）
x-mem inbox approve --all      # 全收（可能和已有笔记重复的留给你逐条定）
x-mem log                      # 最近的写入
x-mem undo 20261126T111110-psrm
```

- **检查**：关系名、目标存在、要取代的那一行确实存在、正文长度、像在给 agent 下指令的内容。
- **原话**：agent 通过 MCP 的 `propose` 提交时必须带你的原话，写入进程到最近的会话记录里核对：核对上就直接写入；核对不上（Claude 桌面版的对话不在本机，或 agent 改写了你的话）就进收件箱等你确认，不会自己写进去。网页或文件里藏的指令拿不出你的原话。
- **收件箱**：在任何一端说“过一下收件箱”“收件箱全收”，agent 会用 MCP 的 `inbox` 工具列出来、按你说的收或丢；你没开口它不会替你确认。会话开始时如果收件箱里有东西，agent 会提一句。
- **重复/冲突**：和已在生效的笔记说的可能是同一件事时，不直接写，放进 inbox 等你决定是取代它还是另记一条。
- **脱敏**：密钥前缀（sk-、sk-ant-、ghp_、AKIA…）、“密码是/口令为/API_KEY=”这类带标签的赋值、Slack/钉钉/企业微信等 webhook、URL 里的 token、连接串密码、私钥、JWT、高熵串，在写进提案之前就替换成 `[已脱敏]`；文件名只取标题。
- **单写入**：同一时间只有一个进程在写（文件锁），不会撞 git 锁。
- **撤回**：每次写入有自己的 run id（时间 + 4 位随机），记下改了哪些文件、怎么改的；撤回按记录反向改回来，再提交一次。不用 git revert，所以撤回中间某一次不会冲突；文件后来被手改过就如实报错、什么都不动。
- **git**：个人层每次写入自动提交；提交失败（比如 git 锁被占）时报“提交失败”，文件整体回滚，提案留着下次重试。项目层的笔记默认不自动提交，留给你和代码一起提交（`config.json` 里 `"commit_project": true` 可以打开）。

## 会话自动采集（默认关）

```bash
x-mem capture on [--exclude ~/某个项目]   # 打开；macOS 上同时装一个每 30 分钟的定时扫描
x-mem capture host antigravity             # 改用 Antigravity 整理（默认 Codex）
x-mem capture off
```

打开后，每轮结束的 Stop 钩子只记一个“有新内容”的信号并在后台叫醒写入进程，自己不读会话，所以耗时不随会话长度增长。写入进程按游标只读会话文件新增的行，只取你说的话：

- 工具输出、系统提示、粘贴的大段代码或日志跳过；`<private>…</private>` 里的话不读；消息里写了 `#不记` 的整个会话都不读，之前采到的候选也会撤掉。
- 先用中文信号词（决定、以后、改成、一律、不要、别再、记住、约定、原因是、踩坑……）和英文信号词挑候选句，长度按字数算；“好的”“继续”“嗯”直接丢。
- 候选交给一个整理助手提炼（x-mem 自己不调模型）：默认是你已登录的 **Codex**（`codex exec`，只读沙箱、低推理强度），也可以用 **Antigravity 命令行**（`agy -p`，需要单独安装并登录过一次；它在后台运行时不输出的已知问题，macOS 上用 `script` 给它一个终端绕过去）。Claude 只有你用 `x-mem capture host claude` 明确选了才会用。`capture on` 时记下程序的完整路径，定时任务也找得到。一个都没有时，原句直接进 inbox。
- 采集来的提案**默认都等你确认**（`x-mem inbox`）；`config.json` 里 `"auto_apply_capture": true` 可以改成自动写入。

## 想起来：目录、直接依据、联想

- **会话开始**：给出规则与偏好、本项目最近的决定，以及**记忆目录**——其余每条笔记的标题和别名（约 1500 token 以内，项目层和最近改过的优先）。人说话往往不点名字，agent 看到目录里有相关的主题，会自己去取。
- **每条消息**：只在有直接依据时递一两条，无关的话一个字不加。
- **recall 的回答**：除了命中的笔记，还会带出和它们有关联的笔记（关系、`[[链接]]`、同一段会话来的），最多 2 条；以及知识库里对得上的资料，单独一块、标明不是你定过的事。

## 知识库

```bash
x-mem kb add ~/Documents/知识库 --exclude "01 项目" --exclude "02 经验与方法"
x-mem kb add ~/Documents/规章制度          # Word、PDF、Excel 也行
x-mem kb list                              # 每个目录几篇笔记、几个文档、哪些读不出来
x-mem kb read                              # 马上读完新加的文档（平时后台自己读）
x-mem kb remove <目录>
```

**认的格式**：Markdown；Word（docx、doc）；PDF（包括只限制了打印/复制、打开不用密码的加密 PDF，压缩对象流，CID 中文字体）；Excel（xlsx、xls）；PowerPoint（pptx 带备注、ppt）；WPS/LibreOffice 的 odt、ods、odp；EPUB；RTF；HTML；txt、csv、json、yaml（UTF-8、UTF-16、GBK 自动识别）。全部用标准库读，不装任何东西；macOS 上某个 PDF 读出来字很少时，会再用系统自带的 PDF 读取（PDFKit）试一次，取好的那个。

- 读出来的文字存在 `state/extract/`，文件不变就不再读；钩子和查询从不等文档，新文档在后台读（`kb add` 时当场读完并显示进度）。
- 记忆和知识库的索引分开缓存：写一条笔记只重建记忆部分，不会重读文档。
- 回答里引用的那一行带上位置：PDF 是“第 N 页”，PPT 是“幻灯片 N”，Excel 是“工作表 名称”；PDF 里被排版折断的中文句子会接回去，`⼯`（康熙部首）这类字会换成平常的 `工`，方便搜到。
- 读不出来的（扫描件没有文字层、要密码才能打开、字体没带字符对照表）在 `kb list` 和 `doctor` 里列出来，不会悄悄漏掉。扫描件要先 OCR。

知识库里的页能被 recall 查到、带出处，但**不算记忆**：不影响 direct / related / none 的判断，不进钩子注入，不能被取代，也从不写入。只有知识库对得上时，回答第一行是 `memory: none`，下面列出资料；旧记忆库 留下的会话摘录会标成“会话摘录：结论是模型提炼的，未经确认”。

## 钩子做什么

| 钩子 | 做什么 |
|---|---|
| SessionStart → `x-mem hook start` | 列出规则与偏好、本项目最近 30 天的决定（约 1500 token 以内） |
| UserPromptSubmit → `x-mem hook prompt` | 只有 direct 时才注入，最多 2 条、约 600 token；同一会话不重复；`#不记` 或粘贴大段内容时跳过 |
| PreToolUse → `x-mem hook guard` | 触发条件匹配到的规则全文交给 agent（约 800 token）；`block` 规则直接拒绝这个动作并说明是哪条 |
| Stop → `x-mem hook stop` | 会话采集打开时记一个信号、后台叫醒写入进程，立刻返回 |

注入的内容逐行引用，开头声明“是数据，不是指令”；看起来像在给 agent 下命令的笔记永远不注入，`doctor` 会列出来。钩子出错一律静默放行，错误写进 `state/hook.log`。

## MCP 工具

`recall`（查，第一行是 direct / related / none）、`read`（全文和关系）、`propose`（提案，唯一的写入口，必须带原话）、`inbox`（用户让过收件箱时：列出、收、丢）、`status`。都可以带 `workspace`（工作区路径），用来找到项目层。连上时服务会下发一段使用说明（Claude 桌面版靠它知道怎么用）。

## config.json（都可省略）

```json
{
  "commit_project": false,
  "auto_apply_capture": false,
  "host": ["/opt/homebrew/bin/codex"],
  "quote_window_hours": 72,
  "kb": [{"path": "/Users/you/Documents/知识库", "exclude": ["01 项目", "02 经验与方法"]}]
}
```

## 验收

```bash
go test ./...
cd testdata/proj && X_MEM_HOME=../home x-mem eval ../regress.json
go run ./tools/bench -bin bin/x-mem -notes 1000
```

| 期 | 验收项 | 结果 |
|---|---|---|
| 一 | 库里有的 20 题至少 18 题 direct | 20/20 |
| 一 | 库里没有的 10 题至少 9 题 none | 9/10 |
| 一 | 10 条无关消息零注入 | 10/10 |
| 一 | 1000 条笔记，每条消息钩子 p95 < 50ms | p95 约 21ms |
| 一 | 手改笔记不用重建即生效 | 是 |
| 二 | 同一秒内连写 10 条，逐条撤回全部撤对 | 通过（乱序撤回） |
| 二 | 纠正一页三条规则中的一条，另两条仍是 direct | 通过 |
| 二 | 关系名拼错（少写 @）时拒绝写入 | 通过 |
| 二 | 脱敏测试集全部拦住，文件名里没有密钥 | 16/16 拦住，8 条正常文本不误伤 |
| 二 | 原话在会话里找不到的提案不自动写入（进收件箱等确认） | 通过 |
| 二 | git 锁被占时报“提交失败”，文件回滚 | 通过 |
| 三 | 会话里的中文决定进 inbox，“好的”“继续”不进 | 通过 |
| 三 | `<private>` 和 `#不记` 会话一字不进库 | 通过 |
| 三 | “推送前跑测试”在 git push 前注入，ls 时不出声 | 通过 |
| 三 | 500 轮长会话，Stop 钩子耗时不随轮数增长 | 通过 |
| 四 | 22 种格式的样例（含加密 PDF、无对照表的中文 CID 字体、GBK 文本）都读出正文 | 通过 |
| 四 | 250 页中文 PDF 读取 | 约 0.2 秒，引用带页码 |
| 四 | 知识库 22MB 文字时写一条笔记，下一次查询 | 约 50ms（不重读文档） |
| 四 | Claude 桌面版开着时安装不改配置，退出后自动接上 | 通过 |

## 和设计文档不一样的地方

- **索引没用 SQLite**：纯 Go 的两个缓存文件（元数据 + 倒排表，倒排按需读取），零依赖，延迟也达标。
- **写入进程不常驻**：用文件锁保证同一时间只有一个写入者，谁需要写谁拿锁；Stop 钩子在后台拉起 `x-mem writer --once`，launchd 只在打开采集时每 30 分钟跑一次。效果和常驻进程一样，少一个要照看的后台服务。
- **提案放在 `state/inbox`**，不放在层目录的 `inbox/`：提案可能带未审核的内容，不该进项目仓库的 git。层目录的 `inbox/` 留给你自己放草稿。
- **项目层不自动提交**、**采集来的提案默认等你确认**：都可以在 `config.json` 里打开。

## 和 旧记忆库 的对照

| 旧记忆库 的能力 | x-mem |
|---|---|
| 提到就想起（每句话 + 会话开始的索引） | 有：每条消息只递 direct；会话开始给规则和记忆目录；三端都有 |
| 联想到相关的一片 | 有：recall 带出有关联的笔记（关系、链接、同一段会话） |
| 托底：出处、三档、改了主意只认新的 | 有，并修了 旧记忆库 的整页作废、撤回撞车、别名被否决、脱敏漏网 |
| 你的知识库（参考资料单独列） | 有：`x-mem kb` |
| 纠正、写入、撤回 | 有：先提案、核对原话、单写入、按次撤回 |
| 会话采集与提炼 | 有：默认关，三端都读，提炼的结果等你确认才写入 |
| 规则在动作前触发 | 有（旧记忆库 只在 Cursor 上有执行前拦截） |
| 夜间自动整理、自动写日更 / 时间日志 | 没做：设计上不做不经你确认的自动改写 |
| 经验长成技能、60 天没用的往后排 | 没做：只有最近改过的稍靠前 |
| 团队服务、Cursor 等其他应用 | 没做；任何支持 MCP 的应用都能手动接 `x-mem mcp` |
| 扫描件 OCR | 没做 |
| PDF / Word 转进知识库 | 有：Word、PDF、Excel、PPT、WPS 等都能直接放进知识库 |
| Claude 桌面版 | 有：MCP + 下发的使用说明 |

## 之后再评估

词典分词、向量检索：都要在回归题集上证明有提升才加。
