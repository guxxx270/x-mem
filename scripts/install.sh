#!/bin/sh
# 从 旧记忆库 换到 x-mem（macOS / Linux）。按顺序做五件事：
#   1. 把 x-mem 装到 ~/.x-mem/bin
#   2. 建个人层 ~/.x-mem/personal
#   3. 从 旧记忆库 库导入（只读库，不改库）；没导入的时间日志、会话摘录、资料登记成知识库，照样能查
#   4. 接入 Claude Code、Codex、Antigravity（钩子 + MCP + 说明，改之前先备份）
#   5. 运行 旧记忆库 自带的卸载脚本：停定时任务、删钩子/插件/MCP 登记；库和钥匙文件不动
# 用法：sh scripts/install.sh [旧记忆库 库目录] [--keep-legacy]
set -eu

HERE=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
KEEP=0
VAULT=""
for a in "$@"; do
  case "$a" in
    --keep-legacy) KEEP=1 ;;
    -h|--help) sed -n '2,8p' "$0"; exit 0 ;;
    *) VAULT=$a ;;
  esac
done

step() { printf '\n== %s\n' "$*"; }

# 1. 安装
step "安装 x-mem"
BIN_DIR="$HOME/.x-mem/bin"
X="$BIN_DIR/x-mem"
mkdir -p "$BIN_DIR"
OS=$(uname -s | tr 'A-Z' 'a-z')
ARCH=$(uname -m)
case "$ARCH" in x86_64) ARCH=amd64 ;; aarch64) ARCH=arm64 ;; esac
PREBUILT="$HERE/bin/x-mem-$OS-$ARCH"
if [ -f "$PREBUILT" ]; then
  cp "$PREBUILT" "$X.new" && mv "$X.new" "$X"
  echo "已安装预编译版本：$X"
elif command -v go >/dev/null 2>&1; then
  (cd "$HERE" && go build -o "$X.new" ./cmd/x-mem) && mv "$X.new" "$X"
  echo "已从源码编译：$X"
else
  echo "没有 Go，也没找到 $PREBUILT；装好 Go（brew install go）后重跑。" >&2
  exit 1
fi
chmod +x "$X"
[ "$OS" = darwin ] && xattr -d com.apple.quarantine "$X" 2>/dev/null || true
"$X" version

# 2. 个人层
step "建个人层"
(cd "$HOME" && "$X" init --personal)

# 3. 导入（要在卸载 旧记忆库 之前：卸载会删掉它记录的库位置）
step "从 旧记忆库 导入"
if [ -n "$VAULT" ]; then
  "$X" import obsidian "$VAULT"
else
  "$X" import obsidian
fi

# 3b. 库里没导入的部分（时间日志、会话摘录、资料）登记成知识库：照样能查、带出处，但不算你定过的事
step "把 旧记忆库 库登记成知识库"
KB_DIR=${VAULT:-$(head -n 1 "${XDG_CONFIG_HOME:-$HOME/.config}/old-mem/vault" 2>/dev/null || true)}
if [ -n "$KB_DIR" ] && [ -d "$KB_DIR" ]; then
  "$X" kb add "$KB_DIR" --exclude "01 项目" --exclude "02 经验与方法" --exclude "90 系统文件"
else
  echo "没找到库目录，跳过；以后可以运行：$X kb add <库目录>"
fi

# 4. 接入（Claude Code、Codex、Antigravity 里装了哪个接哪个，三端共用同一份记忆）
step "接入 Claude Code / Codex / Antigravity"
"$X" install all --apply

# 5. 停用 旧记忆库
if [ "$KEEP" -eq 1 ]; then
  step "保留 旧记忆库（--keep-legacy）"
else
  step "停用 旧记忆库"
  UN=""
  for f in "$HOME"/.old-mem/*/scripts/uninstall.sh "$HOME"/.old-mem/scripts/uninstall.sh; do
    [ -f "$f" ] && UN=$f
  done
  if [ -z "$UN" ] && command -v claude >/dev/null 2>&1; then
    SRV=$(claude mcp get old-mem 2>/dev/null | grep -o '/[^ ]*mcp_server\.py' | head -n 1 || true)
    D=${SRV%/*}
    while [ -n "$D" ] && [ "$D" != "/" ]; do
      if [ -f "$D/scripts/uninstall.sh" ]; then UN="$D/scripts/uninstall.sh"; break; fi
      D=${D%/*}
    done
  fi
  if [ -n "$UN" ]; then
    echo "运行 $UN（不加 --purge：缓存和下载包保留，库不动）"
    sh "$UN" || echo "旧记忆库 卸载脚本报错了，见上面的输出；x-mem 这边已经装好。"
  else
    echo "没找到 旧记忆库 的 uninstall.sh，旧记忆库 没有停用。找到它的安装目录后运行：sh <目录>/scripts/uninstall.sh"
  fi
  # 旧记忆库 的卸载脚本不管 Antigravity：它的 MCP 和钩子条目由 x-mem 删掉（先备份）
  "$X" install antigravity --apply --drop >/dev/null && echo "Antigravity 里 旧记忆库 的条目已清理"
fi

step "检查"
(cd "$HOME" && "$X" doctor) || true
printf '\n完成。重开 Claude Code（和 Claude 桌面 App）后生效。\n'
printf '会话自动采集默认关；想让它从对话里自动记决定，运行：%s capture on\n' "$X"
printf '想在终端直接用 x-mem 命令，把这一行加进 ~/.zshrc：export PATH="$HOME/.x-mem/bin:$PATH"\n'
