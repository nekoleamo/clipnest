#!/usr/bin/env bash
# clipnest 一键安装（macOS）：下载 → 校验 SHA256 → 装进 /Applications（或 ~/Applications）。
#
# 用法：
#   curl -fsSL https://raw.githubusercontent.com/<owner>/<repo>/main/tools/install.sh | sh
#   curl -fsSL https://gitee.com/<owner>/<repo>/raw/main/tools/install.sh | sh     # 国内
#
# 可选参数（管道执行时用环境变量传）：
#   --from github|gitee|auto   下载源（默认 auto：先试 GitHub，3 秒不通就换 Gitee）
#   --version vX.Y.Z           装指定版本（默认取最新发行版）
#   --dir DIR                  装到哪（默认 /Applications，不可写就用 ~/Applications）
#   --no-open                  装完不自动启动
#   CLIPNEST_REPO=owner/repo   仓库（脚本模板里的占位符没被发布脚本填过时才需要）
#   CLIPNEST_INSTALL_DIR=DIR   同 --dir，管道执行时更好用
set -euo pipefail

GITHUB_REPO="nekoleamo/clipnest"
GITEE_REPO="null_593_5354/clipnest"
# 测试/自建镜像用：把这两个前缀换掉即可（见 tools/install_test.sh）
GITHUB_BASE="${CLIPNEST_BASE_GITHUB:-https://github.com}"
GITEE_BASE="${CLIPNEST_BASE_GITEE:-https://gitee.com}"
GITHUB_RAW="${CLIPNEST_RAW_GITHUB:-https://raw.githubusercontent.com}"
GITEE_RAW="${CLIPNEST_RAW_GITEE:-https://gitee.com}"

# 人看的信息一律走 stderr：stdout 只留给「被 $() 捕获」的机器值（HOST/REPO/TAG），
# 否则像「· github 连不上，换下一个」这种提示会被拼进 HOST 里（踩过）。
say() { printf '%s\n' "$*" >&2; }
die() { printf '❌ %s\n' "$*" >&2; exit 1; }

# 管道执行（curl … | sh）时传不了参数，就用环境变量；显式参数优先
FROM="${CLIPNEST_FROM:-auto}"
VERSION="${CLIPNEST_VERSION:-}"
TARGET_DIR="${CLIPNEST_INSTALL_DIR:-${CLIPNEST_DIR:-/Applications}}"
OPEN_AFTER=1

while [ $# -gt 0 ]; do
  case "$1" in
    --from) FROM="${2:?需要 github|gitee|auto}"; shift 2 ;;
    --version) VERSION="${2:?需要 vX.Y.Z}"; shift 2 ;;
    --dir) TARGET_DIR="${2:?需要目录}"; shift 2 ;;
    --no-open) OPEN_AFTER=0; shift ;;
    -h|--help) sed -n '2,18p' "$0" 2>/dev/null || true; exit 0 ;;
    *) die "不认识的参数：$1" ;;
  esac
done

[ "$(uname -s)" = "Darwin" ] || die "这个脚本只装 macOS 版；Windows 用 tools/install.ps1"

# 模板占位符没被填过（直接跑 tools/install.sh）时，用 CLIPNEST_REPO 兜底
repo_for() {
  case "$1" in
    github) [ -n "${CLIPNEST_REPO:-}" ] && printf '%s' "$CLIPNEST_REPO" || printf '%s' "$GITHUB_REPO" ;;
    gitee) [ -n "${CLIPNEST_REPO_GITEE:-${CLIPNEST_REPO:-}}" ] && printf '%s' "${CLIPNEST_REPO_GITEE:-$CLIPNEST_REPO}" || printf '%s' "$GITEE_REPO" ;;
  esac
}

base_for() { [ "$1" = github ] && printf '%s' "$GITHUB_BASE" || printf '%s' "$GITEE_BASE"; }
# 注意：CLIPNEST_API_* 给的是「API 根」，路径由这里拼（GitHub 是 /repos，Gitee 是 /api/v5/repos）
api_for() {
  if [ "$1" = github ]; then
    printf '%s/repos' "${CLIPNEST_API_GITHUB:-https://api.github.com}"
  else
    printf '%s/api/v5/repos' "${CLIPNEST_API_GITEE:-https://gitee.com}"
  fi
}

have_repo() {
  local r; r="$(repo_for "$1")"
  [ -n "$r" ] && [ "${r#__}" = "$r" ]
}

# 探一次 host：能不能拿到 JSON（3 秒超时），用来做 auto 选择
reachable() {
  local api repo
  api="$(api_for "$1")"; repo="$(repo_for "$1")"
  curl -fsS --max-time 3 --range 0-0 "$api/$repo/releases/latest" -o /dev/null 2>/dev/null
}

resolve_host() {
  case "$FROM" in
    github|gitee) have_repo "$FROM" || die "仓库没配置：用 CLIPNEST_REPO=owner/repo 指定"; printf '%s' "$FROM" ;;
    auto)
      local h
      for h in github gitee; do
        have_repo "$h" || continue
        if reachable "$h"; then printf '%s' "$h"; return 0; fi
        say "· $h 连不上，换下一个"
      done
      die "GitHub 和 Gitee 都连不上；可以用 --from 指定，或先检查网络/代理" ;;
    *) die "--from 只能是 github|gitee|auto" ;;
  esac
}

resolve_version() {
  local host="$1" api repo tag
  [ -n "$VERSION" ] && { printf '%s' "$VERSION"; return 0; }
  api="$(api_for "$host")"; repo="$(repo_for "$host")"
  tag="$(curl -fsSL --max-time 10 "$api/$repo/releases/latest" \
    | tr ',' '\n' | grep -m1 '"tag_name"' | sed 's/.*"tag_name" *: *"\([^"]*\)".*/\1/')"
  [ -n "$tag" ] || die "拿不到最新版本号（${host}）"
  printf '%s' "$tag"
}

sha256_of() {
  if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print $1}'
  else sha256sum "$1" | awk '{print $1}'; fi
}

HOST="$(resolve_host)"
REPO="$(repo_for "$HOST")"
TAG="$(resolve_version "$HOST")"
VER="${TAG#v}"
say "== 下载源 ${HOST}（${REPO}），版本 $TAG"

ZIP="clipnest-${VER}-macos.zip"
DL="$(base_for "$HOST")/$REPO/releases/download/$TAG"

TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

say "· 取校验和"
curl -fsSL --max-time 20 "$DL/SHA256SUMS" -o "$TMP/SHA256SUMS" || die "拿不到 SHA256SUMS（发行版里要一起传）"
EXPECT="$(grep -F "$ZIP" "$TMP/SHA256SUMS" | awk '{print $1}' | head -1)"
[ -n "$EXPECT" ] || die "SHA256SUMS 里没有 $ZIP"

say "· 下载 $ZIP"
curl -fL --progress-bar --max-time 300 "$DL/$ZIP" -o "$TMP/$ZIP" || die "下载失败：$DL/$ZIP"

say "· 校验 SHA256"
GOT="$(sha256_of "$TMP/$ZIP")"
[ "$GOT" = "$EXPECT" ] || die "校验失败（期望 ${EXPECT}，实际 ${GOT}），已中止，什么都没装"

say "· 解包"
ditto -x -k "$TMP/$ZIP" "$TMP/unpack" || die "解包失败"
APP="$TMP/unpack/clipnest.app"
[ -d "$APP" ] || die "压缩包里没有 clipnest.app"

# 目标目录不可写就退到 ~/Applications（不需要 sudo）
if [ ! -w "$TARGET_DIR" ] && ! mkdir -p "$TARGET_DIR" 2>/dev/null; then
  TARGET_DIR="$HOME/Applications"
  mkdir -p "$TARGET_DIR"
  say "· $TARGET_DIR 不可写，改装到 ~/Applications"
fi
mkdir -p "$TARGET_DIR"

DEST="$TARGET_DIR/clipnest.app"
# 只在「要覆盖已装的同一份」时才去关正在跑的实例；全新安装不碰用户已经开着的进程
if [ -z "${CLIPNEST_NO_KILL:-}" ] && [ -d "$DEST" ] && pgrep -f "clipnest.app/Contents/MacOS/clipnest" >/dev/null 2>&1; then
  say "· 先退出正在运行的 clipnest"
  pkill -f "clipnest.app/Contents/MacOS/clipnest" || true
  sleep 1
fi
rm -rf "$DEST"
cp -R "$APP" "$DEST" || die "复制到 $DEST 失败"

# curl 下下来的文件本来没有 quarantine，这里兜底（浏览器下过的包会带）
xattr -dr com.apple.quarantine "$DEST" 2>/dev/null || true

say "✅ 已装到 ${DEST}（版本 ${TAG}）"
say ""
say "下一步："
say "  1) 打开 clipnest（菜单栏出现 ❐ 图标）"
say "  2) 首次启动按提示授予「辅助功能」权限（自动粘贴要用）"
say "  3) 面板里 ⌘⇧V 唤出；设置窗可配同步/开机启动"
if [ "$OPEN_AFTER" = "1" ]; then
  open "$DEST" || true
fi
