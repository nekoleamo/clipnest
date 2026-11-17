#!/usr/bin/env bash
# gitee-release.sh 的端到端测试：起一个假 Gitee API，跑真实发版流程。
#
# 覆盖：token 校验 → 仓库可读 → 建 release（含「已存在就复用」）→ 上传 4 个附件
#       （已存在则跳过）→ 匿名回查附件可下。
#
# 用法：bash tools/gitee-release_test.sh
set -uo pipefail
export LC_ALL=C
cd "$(dirname "$0")/.."

PASS=0
FAIL=0
SRV_PID=""
WORK="$(mktemp -d)"
cleanup() { [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null; rm -rf "$WORK"; }
trap cleanup EXIT
ok() { echo "✅ $*"; PASS=$((PASS + 1)); }
bad() { echo "❌ $*"; FAIL=$((FAIL + 1)); }

# 假 Gitee：只实现脚本会打的几个端点
cat >"$WORK/fake.py" <<'PY'
import json, os, sys, threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

STATE = {"release": None, "assets": []}
PORT_FILE = sys.argv[1]
CT = "application/json"

class H(BaseHTTPRequestHandler):
    def log_message(self, *a):  # 安静
        pass

    def _json(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", CT)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _auth_ok(self):
        if self.headers.get("Authorization", "").startswith("token good-token"):
            return True
        if "access_token=good-token" in self.path:
            return True
        return False

    def do_GET(self):
        path = self.path.split("?")[0]
        if path == "/api/v5/user":
            if not self._auth_ok():
                return self._json(401, {"message": "401 Unauthorized: Access token does not exist"})
            return self._json(200, {"login": "null_593_5354"})
        if path == "/api/v5/repos/null_593_5354/clipnest":
            return self._json(200, {"full_name": "null_593_5354/clipnest", "human_name": "nekoleamo/clipnest"})
        if path.endswith("/releases/tags/v1.0.0"):
            if STATE["release"] is None:
                return self._json(404, {"message": "Not Found"})
            return self._json(200, {"id": STATE["release"]})
        if path.endswith("/releases/42"):
            return self._json(200, {"id": 42, "assets": STATE["assets"]})
        # 匿名回查附件
        if "/releases/download/" in path:
            return self._json(200, {"ok": True})
        return self._json(404, {"message": "no route " + path})

    def do_POST(self):
        path = self.path.split("?")[0]
        n = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(n)
        if not self._auth_ok():
            return self._json(401, {"message": "401 Unauthorized"})
        if path.endswith("/releases"):
            STATE["release"] = 42
            return self._json(201, {"id": 42})
        if path.endswith("/attach_files"):
            names = [l.split(b'filename="')[1].split(b'"')[0].decode() for l in body.split(b"\r\n") if b'filename="' in l]
            for name in names:
                STATE["assets"].append({"name": name})
            return self._json(201, {"ok": True})
        return self._json(404, {"message": "no route " + path})

srv = ThreadingHTTPServer(("127.0.0.1", 0), H)
open(PORT_FILE, "w").write(str(srv.server_address[1]))
srv.serve_forever()
PY

python3 "$WORK/fake.py" "$WORK/port" &
SRV_PID=$!
for _ in $(seq 1 50); do [ -s "$WORK/port" ] && break; sleep 0.1; done
PORT="$(cat "$WORK/port")"
export GITEE_API_BASE="http://127.0.0.1:$PORT/api/v5"
export GITEE_ASSET_BASE="http://127.0.0.1:$PORT"
export GITEE_TOKEN="good-token"

# 第一次跑：应建 release + 传 4 个附件
OUT1="$(bash tools/gitee-release.sh v1.0.0 2>&1)"
echo "$OUT1" | grep -q "token 属于账号：null_593_5354" && ok "token 校验通过（打印账号名）" || bad "token 校验没打印账号：$(echo "$OUT1" | tail -2)"
echo "$OUT1" | grep -q "已建 release id=42" && ok "建 release" || bad "没建成 release"
UPLOADED="$(echo "$OUT1" | grep -c "· 上传.*ok")"
[ "$UPLOADED" = "4" ] && ok "4 个附件都传了" || bad "附件只传了 $UPLOADED 个"
echo "$OUT1" | grep -q "可匿名下载" && ok "匿名回查通过" || bad "匿名回查没跑"

# 第二次跑：release 与附件都已在，应「复用 + 跳过」
OUT2="$(bash tools/gitee-release.sh v1.0.0 2>&1)"
echo "$OUT2" | grep -q "复用已有 release id=42" && ok "第二次复用已有 release" || bad "没有复用 release"
SKIPPED="$(echo "$OUT2" | grep -c "已存在，跳过")"
[ "$SKIPPED" = "4" ] && ok "第二次 4 个附件都跳过" || bad "跳过逻辑不对（$SKIPPED 个）"

# token 不对：应给出可读的失败原因，且不是 404 糊过去
OUT3="$(GITEE_TOKEN=bad-token bash tools/gitee-release.sh v1.0.0 2>&1)"
echo "$OUT3" | grep -q "token 无效或已过期" && ok "错 token 的提示说得清" || bad "错 token 的提示不明确：$(echo "$OUT3" | tail -2)"

echo "--------"
echo "通过 $PASS 项，失败 $FAIL 项"
[ "$FAIL" -eq 0 ]
