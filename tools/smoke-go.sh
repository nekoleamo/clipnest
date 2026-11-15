#!/usr/bin/env bash
# Go 版端到端冒烟：起真实进程 → 写剪贴板 → 校验库/文件/单实例/退出。
#
# 用法: tools/smoke-go.sh [二进制路径]   （默认 /tmp/clipnest-go）
# 依赖: tools/pb.swift 编译出的 /tmp/pb（脚本会自己编）
set -u
cd "$(dirname "$0")/.." || exit 1

BIN="${1:-/tmp/clipnest-go}"
DATA="$(mktemp -d)/clipnest"
LOG="$DATA.log"
FAIL=0
ok()   { echo "✅ $*"; }
bad()  { echo "❌ $*"; FAIL=$((FAIL+1)); }

[ -x "$BIN" ] || { echo "先 go build -o $BIN ."; exit 1; }

# 剪贴板写入工具
if [ ! -x /tmp/pb ]; then
  swiftc -O tools/pb.swift -o /tmp/pb >/dev/null 2>&1 || { echo "pb.swift 编译失败"; exit 1; }
fi

# 单实例锁按应用名（不是数据目录）生效：先清掉同名的旧进程，否则新进程会直接退出
pkill -9 -f "$BIN" 2>/dev/null
sleep 1

echo "== 1. 启动（4 秒后自动弹面板，20 秒自动退出）"
CLIPNEST_DATA_DIR="$DATA" CLIPNEST_AUTOOPEN=1 CLIPNEST_AUTOQUIT=20 "$BIN" >"$LOG" 2>&1 &
APP_PID=$!
for _ in 1 2 3 4 5 6; do
  grep -q "BOOT" "$LOG" && break
  sleep 1
done
grep -q "BOOT" "$LOG" && ok "进程启动并打出 BOOT" || { bad "没有 BOOT 日志"; sed -n '1,5p' "$LOG"; }

echo "== 2. 面板是真浮窗（layer=3）"
swiftc -O tools/wins.swift -o /tmp/wins >/dev/null 2>&1
if /tmp/wins clipnest 2>/dev/null | grep -q "layer=3"; then ok "存在 layer=3 的浮窗"; else bad "没找到浮窗"; fi

echo "== 3. 采集：同一段文本复制两次 → 1 行、use_count=2"
/tmp/pb text "冒烟文本 银河系" >/dev/null 2>&1; sleep 1
/tmp/pb text "冒烟文本 银河系" >/dev/null 2>&1; sleep 1
grep -q "CAPTURE" "$LOG" && ok "有 CAPTURE 日志" || bad "没有 CAPTURE 日志"

echo "== 4. 采集：图片（PNG）落盘 + 缩略图"
/tmp/pb image >/dev/null 2>&1 || echo "（pb image 不可用，跳过）"
sleep 2
IMG_COUNT=$(find "$DATA/images/origin" -name '*.png' 2>/dev/null | wc -l | tr -d ' ')
THUMB_COUNT=$(find "$DATA/images/thumbnails" -name '*.png' 2>/dev/null | wc -l | tr -d ' ')
[ "$IMG_COUNT" -ge 1 ] && ok "原图落盘 $IMG_COUNT 个" || bad "没有原图"
[ "$THUMB_COUNT" -ge 1 ] && ok "缩略图 $THUMB_COUNT 个" || bad "没有缩略图"

echo "== 5. 单实例：第二个进程立刻退出"
CLIPNEST_DATA_DIR="$DATA" timeout -s KILL 5 "$BIN" >/tmp/smoke-second.log 2>&1
[ ! -s /tmp/smoke-second.log ] && ok "第二实例没有打日志（已转交并退出）" || bad "第二实例真的跑起来了"

echo "== 6. 收尾：等自动退出"
wait $APP_PID 2>/dev/null
sleep 1
[ -z "$(pgrep -f "$BIN" | head -1)" ] && ok "进程已退出" || bad "进程还在"

echo "== 7. 库内容校验"
python3 - "$DATA" <<'PY' || FAIL=$((FAIL+1))
import sqlite3, sys
db = sqlite3.connect(sys.argv[1] + "/clipnest.db")
rows = db.execute("select kind, content, use_count from items order by id").fetchall()
texts = [r for r in rows if r[0] == "text"]
images = [r for r in rows if r[0] == "image"]
print("   行:", rows)
assert len(texts) == 1, f"文本条目应为 1 行（去重），实际 {len(texts)}"
assert texts[0][2] >= 2, f"use_count 应 >=2，实际 {texts[0][2]}"
assert len(images) <= 1, "图片条目应只有 1 行"
print("✅ 库内容符合预期")
PY

echo
if [ "$FAIL" -eq 0 ]; then echo "全部通过"; else echo "失败 $FAIL 项"; fi
exit $FAIL
