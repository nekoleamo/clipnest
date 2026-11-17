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
API_BASE="${GITEE_API_BASE:-https://gitee.com/api/v5}"
ASSET_BASE="${GITEE_ASSET_BASE:-https://gitee.com}"
API="$API_BASE/repos/$OWNER/$REPO"

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

# Gitee 对鉴权的支持不统一：优先用 Authorization 头；不行再退回 access_token 查询参数。
# 这个变量记住哪种方式通了，后续请求沿用。
AUTH_MODE="header"

# do_curl <METHOD> <url> [curl 额外参数…]
# 成功时把响应体打到 stdout；失败时把状态码与 Gitee 的 message 打到 stderr 并返回 1。
do_curl() {
  local method="$1" url="$2"; shift 2
  local out code
  local -a args=(-sS --max-time 60 -X "$method" "$url")
  if [ "${AUTH_MODE}" = "header" ]; then
    args+=(-H "Authorization: token ${GITEE_TOKEN}")
  else
    args+=(--get --data-urlencode "access_token=${GITEE_TOKEN}")
  fi
  out="$(curl "${args[@]}" "$@" -w '\n__CODE__%{http_code}')" || { echo "curl 失败" >&2; return 1; }
  code="${out##*__CODE__}"
  out="${out%__CODE__*}"
  printf '%s' "$out"
  if [ "${code}" -lt 200 ] || [ "${code}" -ge 300 ]; then
    echo "HTTP ${code}：$(printf '%s' "${out}" | head -c 300)" >&2
    return 1
  fi
  return 0
}

# api：仓库内接口（/releases、/releases/<id> …）
api() { local method="$1" path="$2"; shift 2; do_curl "$method" "$API${path}" "$@"; }
# api_root：全局接口（/user）
api_root() { local method="$1" path="$2"; shift 2; do_curl "$method" "$API_BASE${path}" "$@"; }

# 0) 先确认 token 是谁、能不能看到这个仓库 —— 404 时最需要的信息就是这两条
echo "== 校验 token"
me="$(api_root GET /user 2>&1)" || {
  if [ "$AUTH_MODE" = "header" ]; then
    echo "· Authorization 头方式失败，改用 access_token 查询参数重试" >&2
    AUTH_MODE="query"
    me="$(api_root GET /user 2>&1)" || { echo "❌ token 无效或已过期：$me" >&2; exit 1; }
  else
    echo "❌ token 无效或已过期：${me}" >&2
    exit 1
  fi
}
login="$(printf '%s' "$me" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("login",""))' 2>/dev/null)"
echo "· token 属于账号：${login:-（取不到 login）}（鉴权方式：${AUTH_MODE}）"
if [ -n "$login" ] && [ "$login" != "$OWNER" ]; then
  echo "⚠️ 仓库属于 ${OWNER}，token 属于 ${login}。"
  echo "   如果两者不是同一个账号下的路径/别名，建 release 会 404（Gitee 对无权访问的返回 404）。"
  echo "   可在 Gitee 网页确认仓库地址：https://gitee.com/$OWNER/$REPO"
fi

repo_json="$(do_curl GET "$API" 2>&1)" || { echo "❌ 读不到仓库 $OWNER/${REPO}：$repo_json" >&2; exit 1; }
human="$(printf '%s' "$repo_json" | python3 -c 'import json,sys;d=json.load(sys.stdin);print(d.get("human_name") or d.get("full_name",""))' 2>/dev/null)"
echo "· 仓库可读：${human:-$OWNER/$REPO}"

# 1) 已经有这个 tag 的 release 吗
echo "== 查 ${OWNER}/${REPO} 的 $TAG"
existing="$(api GET "/releases/tags/$TAG" 2>/dev/null |
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
  payload="$(python3 -c '
import json,sys
print(json.dumps({"tag_name": sys.argv[1], "name": sys.argv[1], "body": sys.argv[2], "prerelease": False}, ensure_ascii=False))' "$TAG" "$notes")"
  if ! resp="$(api POST "/releases" -H 'Content-Type: application/json' -d "$payload" 2>/dev/null)"; then
    echo "❌ 建 release 失败。" >&2
    echo "   排查顺序：①token 要有 projects 权限 ②**这个仓库要在 token 的授权仓库列表里**" >&2
    echo "   （Gitee 对「已登录但没纳管这个仓库」返回 404，不是 403——2026-11 实测踩过）" >&2
    echo "   ③tag 要已推到 Gitee（git push gitee ${TAG}）④组织仓库可能要管理员权限" >&2
    exit 1
  fi
  RID="$(printf '%s' "$resp" | python3 -c 'import json,sys;print(json.load(sys.stdin).get("id",""))')"
  [ -n "$RID" ] || { echo "❌ 建 release 没返回 id：$resp" >&2; exit 1; }
  echo "· 已建 release id=$RID"
fi

# 2) 上传附件（已同名存在就跳过）
echo "== 上传附件"
have="$(api GET "/releases/$RID" 2>/dev/null |
  python3 -c 'import json,sys;d=json.load(sys.stdin);print("\n".join(a["name"] for a in d.get("assets") or []))' 2>/dev/null || true)"
for f in "${FILES[@]}"; do
  name="$(basename "$f")"
  if printf '%s\n' "$have" | grep -qx "$name"; then
    echo "· $name 已存在，跳过"
    continue
  fi
  printf '· 上传 %s … ' "$name"
  if api POST "/releases/$RID/attach_files" -F "file=@$f" >/dev/null 2>&1; then
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
    "$ASSET_BASE/$OWNER/$REPO/releases/download/$TAG/$name")"
  case "$code" in
    200) echo "✅ $name 可匿名下载" ;;
    *) echo "⚠️ $name 返回 ${code}（Gitee 附件有时要等几秒生效，稍后重试）" ;;
  esac
done
echo "完成：$ASSET_BASE/$OWNER/$REPO/releases/tag/$TAG"
