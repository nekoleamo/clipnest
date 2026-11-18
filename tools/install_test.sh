#!/usr/bin/env bash
# install.sh 的端到端测试：起一个假的「发行版服务器」，跑真实安装流程。
#
# 覆盖：正常安装（含 SHA256 校验与 quarantine 清理）、哈希不符必须中止、
#       auto 源在 GitHub 不通时切到 Gitee、auto 源首选源坏掉时换源重试。
#
# 只看结果，不碰真实数据：安装目录是临时目录，且 CLIPNEST_NO_KILL=1 不会去关真实实例。
set -uo pipefail
export LC_ALL=C
cd "$(dirname "$0")/.."

PASS=0
FAIL=0
ok() { echo "✅ $*"; PASS=$((PASS + 1)); }
bad() { echo "❌ $*"; FAIL=$((FAIL + 1)); }

WORK="$(mktemp -d)"
SRV_PID=""
cleanup() {
  [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null
  rm -rf "$WORK"
}
trap cleanup EXIT

# start_server <根目录> <端口文件>：静态服务器，端口由系统分配后写进文件
start_server() {
  python3 - "$1" "$2" <<'PY' &
import http.server, socketserver, sys, os
os.chdir(sys.argv[1])
srv = socketserver.TCPServer(("127.0.0.1", 0), http.server.SimpleHTTPRequestHandler)
open(sys.argv[2], "w").write(str(srv.server_address[1]))
srv.serve_forever()
PY
  SRV_PID=$!
  for _ in $(seq 1 50); do
    [ -s "$2" ] && return 0
    sleep 0.1
  done
  return 1
}

# make_release <根目录> [corrupt]：按真实 URL 形状造 releases/ 与 api/
make_release() {
  local root="$1" corrupt="${2:-no}" repo="fake/repo"
  local rel="$root/$repo/releases/download/v1.2.3"
  # 两种 API 形状都造一份：GitHub 是 <api>/repos/...，Gitee 是 <api>/v5/repos/...
  mkdir -p "$rel" "$root/$repo/api/v5/repos/$repo/releases" "$root/$repo/api/repos/$repo/releases"
  printf '{"tag_name":"v1.2.3"}' >"$root/$repo/api/v5/repos/$repo/releases/latest"
  cp "$root/$repo/api/v5/repos/$repo/releases/latest" "$root/$repo/api/repos/$repo/releases/latest"

  mkdir -p "$root/pkg/clipnest.app/Contents/MacOS"
  printf '#!/bin/sh\ntrue\n' >"$root/pkg/clipnest.app/Contents/MacOS/clipnest"
  chmod +x "$root/pkg/clipnest.app/Contents/MacOS/clipnest"
  printf '<plist><dict/></plist>' >"$root/pkg/clipnest.app/Contents/Info.plist"
  (cd "$root/pkg" && zip -qr "$rel/clipnest-1.2.3-macos.zip" clipnest.app)

  local sum
  sum="$(shasum -a 256 "$rel/clipnest-1.2.3-macos.zip" | awk '{print $1}')"
  [ "$corrupt" = "corrupt" ] && sum="0000000000000000000000000000000000000000000000000000000000000000"
  printf '%s  clipnest-1.2.3-macos.zip\n' "$sum" >"$rel/SHA256SUMS"
}

# ---------- 1) 正常安装 ----------
GH="$WORK/gh"
make_release "$GH"
start_server "$GH" "$WORK/gh.port" || { echo "假服务器起不来"; exit 1; }
PORT_GH="$(cat "$WORK/gh.port")"

env CLIPNEST_NO_KILL=1 CLIPNEST_REPO="fake/repo" CLIPNEST_BASE_GITHUB="http://127.0.0.1:$PORT_GH" \
  bash tools/install.sh --from github --version v1.2.3 --dir "$WORK/apps1" --no-open >"$WORK/run1.log" 2>&1
if [ $? -eq 0 ] && [ -x "$WORK/apps1/clipnest.app/Contents/MacOS/clipnest" ]; then
  ok "正常安装：校验通过、解包落到目标目录"
else
  bad "正常安装失败：$(tail -3 "$WORK/run1.log" | tr '\n' ' ')"
fi
if xattr -l "$WORK/apps1/clipnest.app" 2>/dev/null | grep -q quarantine; then
  bad "quarantine 没清掉"
else
  ok "quarantine 已清（curl 下载的包本来没有，兜底也生效）"
fi

# ---------- 2) 哈希不符必须中止 ----------
GH2="$WORK/gh2"
make_release "$GH2" corrupt
kill "$SRV_PID" 2>/dev/null
start_server "$GH2" "$WORK/gh2.port" || { echo "假服务器起不来"; exit 1; }
PORT_GH2="$(cat "$WORK/gh2.port")"

env CLIPNEST_NO_KILL=1 CLIPNEST_REPO="fake/repo" CLIPNEST_BASE_GITHUB="http://127.0.0.1:$PORT_GH2" \
  bash tools/install.sh --from github --version v1.2.3 --dir "$WORK/apps2" --no-open >"$WORK/run2.log" 2>&1
RC=$?
if [ $RC -ne 0 ] && [ ! -d "$WORK/apps2/clipnest.app" ]; then
  ok "哈希不符：中止且没装机"
else
  bad "哈希不符时没有正确中止（退出码 $RC）"
fi
grep -q "校验失败" "$WORK/run2.log" && ok "哈希不符的提示说得清" || bad "哈希不符的提示不明确"

# ---------- 3) auto：GitHub 不通时切 Gitee ----------
GH3="$WORK/gh3"
make_release "$GH3"
kill "$SRV_PID" 2>/dev/null
start_server "$GH3" "$WORK/gh3.port" || { echo "假服务器起不来"; exit 1; }
PORT_GH3="$(cat "$WORK/gh3.port")"

env CLIPNEST_NO_KILL=1 CLIPNEST_REPO="fake/repo" CLIPNEST_REPO_GITEE="fake/repo" \
  CLIPNEST_BASE_GITHUB="http://127.0.0.1:1" CLIPNEST_BASE_GITEE="http://127.0.0.1:$PORT_GH3" \
  CLIPNEST_API_GITHUB="http://127.0.0.1:1" \
  CLIPNEST_API_GITEE="http://127.0.0.1:$PORT_GH3/fake/repo" \
  bash tools/install.sh --from auto --version v1.2.3 --dir "$WORK/apps3" --no-open >"$WORK/run3.log" 2>&1
if [ $? -eq 0 ] && [ -x "$WORK/apps3/clipnest.app/Contents/MacOS/clipnest" ]; then
  ok "auto 源：GitHub 不通时自动换 Gitee"
else
  bad "auto 源切换失败：$(tail -3 "$WORK/run3.log" | tr '\n' ' ')"
fi
grep -qi "gitee" "$WORK/run3.log" && ok "日志里写明了用的哪个源" || bad "日志没说用的哪个源"

# ---------- 4) --from gitee 直连 ----------
env CLIPNEST_NO_KILL=1 CLIPNEST_REPO_GITEE="fake/repo" \
  CLIPNEST_BASE_GITEE="http://127.0.0.1:$PORT_GH3" \
  bash tools/install.sh --from gitee --version v1.2.3 --dir "$WORK/apps4" --no-open >"$WORK/run4.log" 2>&1
if [ $? -eq 0 ] && [ -x "$WORK/apps4/clipnest.app/Contents/MacOS/clipnest" ]; then
  ok "--from gitee 直连可用"
else
  bad "--from gitee 直连失败：$(tail -3 "$WORK/run4.log" | tr '\n' ' ')"
fi

# ---------- 5) 管道风格：只用环境变量选源 ----------
env CLIPNEST_NO_KILL=1 CLIPNEST_FROM=gitee CLIPNEST_REPO_GITEE="fake/repo" \
  CLIPNEST_BASE_GITEE="http://127.0.0.1:$PORT_GH3" \
  bash tools/install.sh --version v1.2.3 --dir "$WORK/apps5" --no-open >"$WORK/run5.log" 2>&1
if [ $? -eq 0 ] && grep -q "下载源 gitee" "$WORK/run5.log"; then
  ok "CLIPNEST_FROM=gitee 环境变量生效（管道执行时用）"
else
  bad "CLIPNEST_FROM 没生效：$(head -2 "$WORK/run5.log" | tr '\n' ' ')"
fi

# ---------- 6) auto：首选源资产坏掉（哈希不符）时换另一个源 ----------
BAD3="$WORK/gh3b"
GOOD3="$WORK/gh3g"
make_release "$BAD3" corrupt
make_release "$GOOD3"
kill "$SRV_PID" 2>/dev/null
start_server "$BAD3" "$WORK/gh3b.port" || { echo "假服务器起不来"; exit 1; }
PORT_BAD3="$(cat "$WORK/gh3b.port")"
SRV_BAD3="$SRV_PID"
start_server "$GOOD3" "$WORK/gh3g.port" || { echo "假服务器起不来"; exit 1; }
PORT_GOOD3="$(cat "$WORK/gh3g.port")"

# 探针：gitee 不通 → 选 github（包是坏的）→ 哈希不符 → 自当换回 gitee
env CLIPNEST_NO_KILL=1 CLIPNEST_REPO="fake/repo" CLIPNEST_REPO_GITEE="fake/repo" \
  CLIPNEST_BASE_GITHUB="http://127.0.0.1:$PORT_BAD3" CLIPNEST_BASE_GITEE="http://127.0.0.1:$PORT_GOOD3" \
  CLIPNEST_API_GITHUB="http://127.0.0.1:$PORT_BAD3/fake/repo/api" \
  CLIPNEST_API_GITEE="http://127.0.0.1:1" \
  bash tools/install.sh --from auto --version v1.2.3 --dir "$WORK/apps6" --no-open >"$WORK/run6.log" 2>&1
if [ $? -eq 0 ] && [ -x "$WORK/apps6/clipnest.app/Contents/MacOS/clipnest" ]; then
  ok "auto 源：首选源哈希不符时换另一个源装上"
else
  bad "换源重试没用：$(tail -3 "$WORK/run6.log" | tr '\n' ' ')"
fi
grep -q "校验失败" "$WORK/run6.log" && ok "换源日志写明了失败原因" || bad "换源日志没说原因"
kill "$SRV_BAD3" 2>/dev/null

echo "--------"
echo "通过 $PASS 项，失败 $FAIL 项"
[ "$FAIL" -eq 0 ]
