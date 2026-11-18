#!/bin/sh
# 把 site/ 渲染成 site/dist/：只做一件事 —— 注入版本号，不引任何前端工具链。
#
#   sh tools/build-site.sh                 # 版本号取自 mygo.json（唯一事实源）
#   VERSION=1.4.0 sh tools/build-site.sh   # 手动覆盖（预演下一版时用）
#
# 为什么注入而不是写死在 HTML 里：发版后忘了改站点，是最常见的静默腐化。
# mygo.json 的 version 就是打包用的版本号，所以它天然跟着发版走，站点不用再记一遍。
set -eu
cd "$(dirname "$0")/.."

VER="${VERSION:-}"
if [ -z "$VER" ]; then
  VER=$(grep -o '"version"[[:space:]]*:[[:space:]]*"[^"]*"' mygo.json 2>/dev/null \
        | head -1 | sed 's/.*"\([^"]*\)"$/\1/')
fi
# 拿不到版本号就硬失败：宁可让 CI 红，也不要发一个 vdev 上去
[ -n "$VER" ] || { echo "❌ 读不到 mygo.json 里的 version" >&2; exit 1; }

OUT=site/dist
rm -rf "$OUT"
mkdir -p "$OUT"

# 逐层复制（保留 en/ 子目录），文本文件统一做版本号替换
find site -type f ! -path 'site/dist/*' | while read -r f; do
  rel=${f#site/}
  mkdir -p "$OUT/$(dirname "$rel")"
  case "$rel" in
    *.html|*.css|*.svg)
      sed "s/__CLIPNEST_VERSION__/${VER}/g" "$f" > "$OUT/$rel" ;;
    *)
      cp "$f" "$OUT/$rel" ;;
  esac
done

echo "site/dist 已生成（v${VER}）：$(cd "$OUT" && find . -type f | sed 's|^\./||' | sort | tr '\n' ' ')"
