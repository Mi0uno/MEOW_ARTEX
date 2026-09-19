#!/usr/bin/env bash
# ARTEX 本地部署构建器。
#
# 与 build.sh 的区别：build.sh 面向跨平台发布打包；本脚本面向"在本机编译并
# 部署到一个独立可运行目录"的日常使用——产物直接落到运行目录，二进制即换即用。
#
# 用法：
#   ./build-local.sh                 # 部署到默认目录（仓库同级 ../artex-app）
#   ./build-local.sh /d/AI/my-artex  # 部署到指定目录
#   ARTEX_LOCAL_DIR=... ./build-local.sh
#
# 行为约定：
#   - 只覆盖二进制和 start 脚本 / config.example.json / README，绝不触碰
#     运行目录里已有的 data/、pgdata/、pgsql/、config.json、jwt.key、skills/。
#   - 目标目录不存在时自动创建，并从仓库补齐 skills/（首次部署）。
#   - Windows 产出两个可执行：artex.exe（控制台服务版，配 start.bat 守护）与
#     ARTEX.exe（GUI 桌面版，双击直接开 WebView2 窗口）；其余平台只有 artex。
#
# 注意：cmd/artex/rsrc_windows_*.syso（exe 图标/版本资源，必须与 main 包同目录
# 才会被链接）由 winres/ 生成：
#   go run github.com/tc-hib/go-winres@latest make --out cmd/artex/rsrc
# 没有它也能编译，只是 exe 没有图标。
set -euo pipefail

cd "$(cd "$(dirname "$0")" && pwd)"

REPO_OS="$(go env GOOS)"
REPO_ARCH="$(go env GOARCH)"
TARGET="${1:-${ARTEX_LOCAL_DIR:-$(pwd)/../artex-app}}"
TARGET="$(cd "$(dirname "$TARGET")" && mkdir -p "$(basename "$TARGET")" && cd "$(basename "$TARGET")" && pwd)"

info() { printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok()   { printf '\033[32m[+]\033[0m %s\n' "$*"; }
die()  { printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }

command -v go >/dev/null 2>&1 || die "未检测到 Go"
command -v npm  >/dev/null 2>&1 || die "未检测到 npm"

# ---------- 前端 ----------
# build:static 里的 "NEXT_EXPORT=1 next build" 是 Unix 写法，Windows 的 cmd
# 跑不了；这里统一在 bash 里导出环境变量后直接调 next，两个平台行为一致。
info "构建前端静态资源"
if [ ! -d web/node_modules ]; then
  (cd web && npm install)
fi
(cd web && NEXT_EXPORT=1 npx next build)
info "同步前端资源到 server/webui/dist"
mkdir -p server/webui/dist
if command -v rsync >/dev/null 2>&1; then
  rsync -a --delete web/out/ server/webui/dist/
else
  rm -rf server/webui/dist
  cp -R web/out server/webui/dist
fi

# ---------- 版本号 ----------
if [ -z "${ARTEX_BUILD_VERSION:-}" ]; then
  if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    ARTEX_BUILD_VERSION="$(git describe --tags --always --dirty)"
  else
    ARTEX_BUILD_VERSION="dev"
  fi
fi
ARTEX_BUILD_VERSION="${ARTEX_BUILD_VERSION#v}"

build() {
  local out="$1"; shift
  info "编译 ${REPO_OS}/${REPO_ARCH} -> $out（版本 ${ARTEX_BUILD_VERSION}）"
  CGO_ENABLED=0 \
  go build \
    -tags embedui \
    -trimpath \
    -ldflags "-s -w -buildid= -X main.version=${ARTEX_BUILD_VERSION} $*" \
    -o "$out" \
    ./cmd/artex
}

# ---------- 后端 ----------
binary="artex"
[ "$REPO_OS" = "windows" ] && binary="artex.exe"
build "$TARGET/$binary"

# 桌面版：windowsgui 子系统（无控制台窗口）+ -desktop=auto 默认开窗。
# 目前桌面窗口只有 Windows（WebView2）实现，其他平台跳过。
if [ "$REPO_OS" = "windows" ]; then
  gui_binary="MEOWFUCK.exe"
  build "$TARGET/$gui_binary" "-H=windowsgui -X main.desktopDefault=1"
fi

# ---------- 部署伴随文件 ----------
cp start.bat "$TARGET/" 2>/dev/null || true
cp start.sh  "$TARGET/" 2>/dev/null && chmod +x "$TARGET/start.sh" || true
cp config.example.json "$TARGET/"
[ -f README.md ] && cp README.md "$TARGET/"
if [ ! -d "$TARGET/skills" ]; then
  info "目标目录缺少 skills/，从仓库补齐"
  cp -R skills "$TARGET/skills"
fi

ok "部署完成：$TARGET"
if [ "$REPO_OS" = "windows" ]; then
  echo "    桌面使用：双击 MEOWFUCK.exe（无需控制台）。"
  echo "    服务方式：先 pg-start.bat 再 start.bat，日志在控制台可见。"
else
  echo "    运行：cd $TARGET && ./start.sh"
fi
echo "    data/、pgdata/、config.json、jwt.key、skills/ 未被触碰。"
