#!/usr/bin/env bash
# 旧库迁移校验（T6）：把一个已有的数据目录复制到临时位置，用当前二进制打开，
# 再比对条目数 / FTS / 图片引用 / 搜索 / 完整性，全程不碰真实数据、不推 NAS。
#
# 用法: tools/migrate-check.sh [旧数据目录] [二进制]
#   默认旧目录 = ~/Library/Application Support/clipnest.old-*（取最新的那个）
#   默认二进制 = /Applications/clipnest.app/Contents/MacOS/clipnest
set -u
cd "$(dirname "$0")/.." || exit 1

OLD="${1:-$(ls -d "$HOME/Library/Application Support/clipnest.old-"* 2>/dev/null | tail -1)}"
BIN="${2:-/Applications/clipnest.app/Contents/MacOS/clipnest}"
FAIL=0
ok()  { echo "✅ $*"; }
bad() { echo "❌ $*"; FAIL=$((FAIL+1)); }

[ -n "${OLD:-}" ] && [ -f "$OLD/clipnest.db" ] || { echo "找不到旧库：${OLD:-（空）}"; exit 1; }
[ -x "$BIN" ] || { echo "找不到二进制：$BIN"; exit 1; }
echo "== 旧库：$OLD"
echo "== 二进制：$BIN"

WORK="$(mktemp -d)/clipnest"
cp -R "$OLD" "$WORK" || { echo "复制失败"; exit 1; }
LOG="$WORK.log"

# 关掉同步：校验过程不该往 NAS 推东西（也不该去碰钥匙串）
if [ -f "$WORK/sync.json" ]; then
  python3 - "$WORK/sync.json" <<'PY'
import json, sys
p = sys.argv[1]
d = json.load(open(p))
d["enabled"] = False
json.dump(d, open(p, "w"), ensure_ascii=False, indent=2)
PY
fi

old_items=$(sqlite3 "$OLD/clipnest.db" "select count(*) from items;")
old_ver=$(sqlite3 "$OLD/clipnest.db" "pragma user_version;")
old_imgs=$(sqlite3 "$OLD/clipnest.db" "select count(*) from items where kind='image';")
echo "== 打开前：条目 ${old_items}（图片 ${old_imgs}），schema 版本 $old_ver"

# 同样的单实例锁名字，先把正在跑的剪贴板管理器停掉，否则新进程会直接退出
pkill -f "$BIN" 2>/dev/null
sleep 1
CLIPNEST_DATA_DIR="$WORK" CLIPNEST_AUTOQUIT=8 "$BIN" >"$LOG" 2>&1
sleep 1

if grep -q "^.*BOOT " "$LOG"; then
  ok "进程正常启动（BOOT 日志在）"
  # BOOT 里的「历史=N」是应用自己查库得到的，对得上才算真的读出来了
  boot_items=$(grep -o "历史=[0-9]*" "$LOG" | tail -1 | cut -d= -f2)
  [ "${boot_items:-0}" = "$old_items" ] && ok "应用读到同样的条目数（历史=${boot_items}）" || bad "应用读到的条目数不对：历史=${boot_items:-?}，应为 ${old_items}"
else
  bad "没有 BOOT 日志"; sed -n '1,5p' "$LOG"
fi

new_items=$(sqlite3 "$WORK/clipnest.db" "select count(*) from items;" 2>/dev/null)
new_ver=$(sqlite3 "$WORK/clipnest.db" "pragma user_version;" 2>/dev/null)
[ "$new_items" = "$old_items" ] && ok "条目数一致（${new_items}）" || bad "条目数变了：$old_items → $new_items"
[ "$new_ver" -ge "$old_ver" ] && ok "schema 版本没回退（$old_ver → ${new_ver}）" || bad "schema 版本回退：$old_ver → $new_ver"

integrity=$(sqlite3 "$WORK/clipnest.db" "pragma integrity_check;" 2>/dev/null | head -1)
[ "$integrity" = "ok" ] && ok "integrity_check = ok" || bad "integrity_check = $integrity"

fts=$(sqlite3 "$WORK/clipnest.db" "select count(*) from items_fts;" 2>/dev/null)
[ "$fts" = "$new_items" ] && ok "FTS 索引与条目对齐（${fts}）" || bad "FTS 索引 $fts ≠ 条目 $new_items"

# 图片：库里的每条图片都要有原图文件
missing=0
while IFS= read -r content; do
  hash="${content%.png}"
  [ -f "$WORK/images/origin/${hash:0:2}/$content" ] || missing=$((missing+1))
done < <(sqlite3 "$WORK/clipnest.db" "select content from items where kind='image';")
[ "$missing" -eq 0 ] && ok "图片原图都在本地（$old_imgs 条）" || bad "$missing 条图片原图缺失"

# 搜索：拿旧库里第一段非空文本的片段查 FTS，能被召回就说明索引还能用
probe=$(sqlite3 "$WORK/clipnest.db" "select substr(content,1,4) from items where kind='text' and length(content)>=4 limit 1;" 2>/dev/null)
if [ -n "${probe:-}" ]; then
  hits=$(sqlite3 "$WORK/clipnest.db" "select count(*) from items_fts where items_fts match '$probe';" 2>/dev/null)
  [ "${hits:-0}" -ge 1 ] && ok "FTS 检索可用（'$probe' 命中 $hits 条）" || bad "FTS 检索不到 '$probe'"
else
  echo "（旧库里没有够长的文本条目，跳过 FTS 检索断言）"
fi

# 设置与同步配置应当被读出来而不是被清掉
[ -f "$WORK/sync.json" ] && ok "sync.json 还在（未被覆盖删除）" || bad "sync.json 不见了"
[ -f "$WORK/settings.json" ] && ok "settings.json 生成/保留" || echo "（旧库没有 settings.json，走默认值，正常）"

echo
if [ "$FAIL" -eq 0 ]; then
  echo "全部通过（临时目录 ${WORK}，可删除）"
else
  echo "有 $FAIL 项未通过，日志：$LOG"
  exit 1
fi
