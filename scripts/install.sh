#!/bin/sh
# 一键安装并接入 x-mem（macOS / Linux）：
#   1. 解压仓库自带的预编译二进制到 ~/.x-mem/bin/x-mem（无预编译包时用 Go 编译）
#   2. 初始化个人层 ~/.x-mem/personal
#   3. 自动接入本机已装的 Claude Code、Codex、Antigravity、Claude 桌面版（修改前自动备份）
#   4. 运行 x-mem doctor 自检
# 用法：sh scripts/install.sh
set -eu

HERE=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
step() { printf '\n== %s\n' "$*"; }

step "安装 x-mem"
BIN_DIR="$HOME/.x-mem/bin"
X="$BIN_DIR/x-mem"
mkdir -p "$BIN_DIR"
OS=$(uname -s | tr 'A-Z' 'a-z')
ARCH=$(uname -m)
case "$ARCH" in x86_64) ARCH=amd64 ;; aarch64) ARCH=arm64 ;; esac
GZ="$HERE/plugin/libexec/x-mem-$OS-$ARCH.gz"
PREBUILT="$HERE/bin/x-mem-$OS-$ARCH"

if [ -f "$GZ" ]; then
  gzip -dc "$GZ" > "$X.new" && mv "$X.new" "$X"
  [ -f "$HERE/plugin/libexec/VERSION" ] && cp "$HERE/plugin/libexec/VERSION" "$BIN_DIR/.version"
  echo "已安装预编译版本：$X"
elif [ -f "$PREBUILT" ]; then
  cp "$PREBUILT" "$X.new" && mv "$X.new" "$X"
  echo "已安装本地编译版本：$X"
elif command -v go >/dev/null 2>&1; then
  (cd "$HERE" && go build -o "$X.new" ./cmd/x-mem) && mv "$X.new" "$X"
  echo "已从源码编译：$X"
else
  echo "未找到预编译包 $GZ 且本机未安装 Go。" >&2
  exit 1
fi
chmod +x "$X"
[ "$OS" = darwin ] && xattr -d com.apple.quarantine "$X" 2>/dev/null || true
"$X" version

step "初始化个人层"
(cd "$HOME" && "$X" init --personal)

step "接入本机已安装的客户端"
"$X" install all --apply

step "运行自检"
(cd "$HOME" && "$X" doctor) || true

printf '\n完成。重开客户端后生效。\n'
printf '会话自动采集默认关闭；如需开启自动采集：~/.x-mem/bin/x-mem capture on\n'
printf '如需在终端直接使用 x-mem 命令，可将以下行加入 ~/.zshrc：\n'
printf '  export PATH="$HOME/.x-mem/bin:$PATH"\n'
