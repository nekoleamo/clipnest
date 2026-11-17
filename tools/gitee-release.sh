#!/usr/bin/env bash
# 把一次发版镜像到 Gitee：建 Release（若不存在）+ 上传产物附件。
#
# 凭据约定与仓库里其它脚本一致：**只认环境变量 `GITEE_TOKEN`**，脚本自己不读任何
# secrets 文件（凭据引入放在你的 shell 里，不落盘、不入库、不打印）：
#
#   set -a; . ~/.pi/agent/local/secrets.env; set +a
#   GITEE_TOKEN="$GITEE_TOKEN" bash tools/gitee-release.sh v1.0.0
#
# 用法：
#   tools/gitee-release.sh <tag> [产物目录]     # 默认 dist/
#   GITEE_OWNER / GITEE_REPO 可覆盖（默认 null_593_5354/clipnest）
set -uo pipefail
cd "$(dirname "$0")/.."

TAG="${1:?用法: tools/gitee-release.sh <tag> [产物目录]}"
VER="${TAG#v}"
OUT="${2:-dist}"
OWNER="${GITEE_OWNER:-null_593_5354}"
REPO="${GITEE_REPO:-clipnest}"
API="https://gitee.com/api/v5/repos/$OWNER/$REPO"

[ -n "${GITEE_TOKEN:-}" ] || {
  echo "❌ 没有 GITEE_TOKEN。先在 shell 里注入（脚本自身不读 secrets 文件）：" >&2
  echo '   set -a; . ~/.pi/agent/local/secrets.env; set +a' >&2
  exit 1
}

FILES=(
  "$OUT/clipnest-$VER-macos.zip"
  "$OUT/clipnest-$VER-windows-x86_64.zip"
  "$OUT/clipnest-$VER-universal.dmg"
  "$OUT/SHA256SUMS"
)
for f in "${FILES[@]}"; do
  [ -f "$f" ] || { echo "❌ 缺产物：${f}（先跑 tools/release-go.sh ${VER}）" >&2; exit 1; }
  size=$(( $(wc -c <"$f") / 1024 / 1024 ))
  [ "$size" -gt 100 ] && { echo "❌ $f 有 ${size}MB，超过 Gitee 单文件 100MB 上限" >&2; exit 1; }
done

auth=(-H "Authorization: token $GITEE_TOKEN")

# 1) 已经有这个 tag 的 release 吗
echo "== 查 ${OWNER}/${REPO} 的 $TAG"
existing="$(curl -fsS --max-time 20 "${auth[@]}" "$API/releases/tags/$TAG" 2>/dev/null |
  python3 -c 'import json,sys;d=json.load(sys.stdin);print(d.get("id",""))' 2>/dev/null || true)"

if [ -n "$existing" ]; then
  echo "· 复用已有 release id=$existing"
  RID="$existing"
else
  echo "· 新建 release"
  notes="$(python3 - "$TAG" <<'PY'
import sys
tag = sys.argv[1]
print(f"镜像自 GitHub Releases（{tag}）。\n\n"
      "GitHub 上的发布页是权威来源，这里是国内下载镜像：产物与 SHA256SUMS 完全一致。\n\n"
      "装法见 https://github.com/nekoleamo/clipnest#安装 —— 或者直接用一键脚本：\n"
      "macOS   `curl -fsSL https://gitee.com/null_593_5354/clipnest/raw/main/install.sh | sh`\n"
      "Windows `irm https://gitee.com/null_593_5354/clipnest/raw/main/install.ps1 | iex`")
PY
)"
  resp="$(curl -fsS --max-time 30 "${auth[@]}" -X POST "$API/releases" \
    -H 'Content-Type: application/json' \
    -d "$(python3 -c '
import json,sys
print(json.dumps({"tag_name": sys.argv[1], "name": sys.argv[1], "body": sys.argv[2], "prerelease": False}, ensure_ascii=False))' "$TAG" "$notes")" 2>&1)" || {
    echo "❌ 建 release 失败：$resp" >&2
    echo "   常见原因：token 没 repo 权限 / tag 在 Gitee 上还不存在（先 git push gitee ${TAG}）" >&2
    exit 1
  }
  RID="$(printf '%s' "$resp" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("id",""))')"
  [ -n "$RID" ] || { echo "❌ 建 release 没返回 id：$resp" >&2; exit 1; }
  echo "· 已建 release id=$RID"
fi

# 2) 上传附件（已同名存在就跳过）
echo "== 上传附件"
have="$(curl -fsS --max-time 20 "${auth[@]}" "$API/releases/$RID" 2>/dev/null |
  python3 -c 'import json,sys;d=json.load(sys.stdin);print("\n".join(a["name"] for a in d.get("assets") or []))' 2>/dev/null || true)"
for f in "${FILES[@]}"; do
  name="$(basename "$f")"
  if printf '%s\n' "$have" | grep -qx "$name"; then
    echo "· $name 已存在，跳过"
    continue
  fi
  printf '· 上传 %s … ' "$name"
  if curl -fsS --max-time 300 "${auth[@]}" -X POST "$API/releases/$RID/attach_files" -F "file=@$f" >/dev/null 2>&1; then
    echo ok
  else
    echo "失败"
    echo "❌ $name 上传失败（可以稍后在 Gitee 网页上补传）" >&2
    exit 1
  fi
done

# 3) 匿名回查：附件能不能公开下到
echo "== 回查（不带 token）"
for f in "${FILES[@]}"; do
  name="$(basename "$f")"
  code="$(curl -s -o /dev/null -w '%{http_code}' -L --max-time 30 \
    "https://gitee.com/$OWNER/$REPO/releases/download/$TAG/$name")"
  case "$code" in
    200) echo "✅ $name 可匿名下载" ;;
    *) echo "⚠️ $name 返回 ${code}（Gitee 附件有时要等几秒生效，稍后重试）" ;;
  esac
done
echo "完成：https://gitee.com/$OWNER/$REPO/releases/tag/$TAG"
