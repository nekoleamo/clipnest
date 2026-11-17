#!/usr/bin/env bash
# 打发布包（Go + mygo 版）：测试/静态检查 → 双平台产物 → zip → SHA256SUMS → cask。
#
# 用法：
#   tools/release-go.sh                 # 版本号取自 mygo.json
#   tools/release-go.sh 1.0.1           # 临时指定版本号
#   tools/release-go.sh                        # 默认推到 nekoleamo/clipnest（GitHub）
#   GITEE_OWNER=null_593_5354 tools/release-go.sh
#
# 产物在 dist/：
#   clipnest-<ver>-macos.zip         （.app，ditto 打包保留符号链接）
#   clipnest-<ver>-windows-x86_64.zip（clipnest.exe）
#   clipnest-<ver>-<arch>.dmg        （mygo 生成，方便直装）
#   SHA256SUMS、clipnest.cask.rb
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${1:-$(python3 -c "import json;print(json.load(open('mygo.json'))['version'])")}"
OWNER="${OWNER:-nekoleamo}"
REPO="${REPO:-clipnest}"
# Gitee 镜像的仓库名/账号可以不同（Gitee 的账号 id 是注册时自动生成的）
GITEE_OWNER="${GITEE_OWNER:-null_593_5354}"
GITEE_REPO="${GITEE_REPO:-$REPO}"
OUT="dist"
PLATFORMS="${PLATFORMS:-darwin/universal,windows/amd64}"

echo "== 版本 ${VERSION}（${OWNER}/${REPO}）平台 ${PLATFORMS}"

echo "== 测试与静态检查"
go test ./...
go vet ./...
GOOS=windows go build ./...
GOOS=linux go build ./...

echo "== 打包（mygo build）"
go tool mygo build -platform "$PLATFORMS" -skip-installer

echo "== 归档"
MAC_APP="$OUT/darwin-universal/clipnest.app"
[ -d "$MAC_APP" ] || MAC_APP="$(ls -d "$OUT"/darwin-*/clipnest.app | head -1)"
rm -f "$OUT/clipnest-${VERSION}-macos.zip" "$OUT/clipnest-${VERSION}-windows-x86_64.zip"
ditto -c -k --sequesterRsrc --keepParent "$MAC_APP" "$OUT/clipnest-${VERSION}-macos.zip"
if [ -f "$OUT/windows-amd64/clipnest.exe" ]; then
  (cd "$OUT/windows-amd64" && zip -q -X "../clipnest-${VERSION}-windows-x86_64.zip" clipnest.exe)
fi

echo "== 校验和"
(cd "$OUT" && shasum -a 256 clipnest-"${VERSION}"-*.zip */*.dmg > SHA256SUMS)
cat "$OUT/SHA256SUMS"

echo "== 生成 cask 片段"
MAC_SHA=$(grep "macos.zip" "$OUT/SHA256SUMS" | awk '{print $1}')
cat > "$OUT/clipnest.cask.rb" <<RUBY
cask "clipnest" do
  version "${VERSION}"
  sha256 "${MAC_SHA}"

  url "https://github.com/${OWNER}/${REPO}/releases/download/v#{version}/clipnest-#{version}-macos.zip"
  name "clipnest"
  desc "剪贴板历史管理器，自带 NAS 同步"
  homepage "https://github.com/${OWNER}/${REPO}"

  app "clipnest.app"

  # 本项目没有购买 Apple Developer Program，因此没有公证（notarization）。
  # 下载来的 zip 带 com.apple.quarantine 属性，Gatekeeper 会把未公证的 app 拦下来。
  # cask 装完后删掉这个属性即可正常启动；官方 homebrew-cask 要求已公证，
  # 所以本 cask 只能放在自建 tap 里。
  postflight do
    system_command "/usr/bin/xattr",
                   args: ["-dr", "com.apple.quarantine", "#{appdir}/clipnest.app"]
  end

  # brew uninstall --zap：数据与可选的登录项一起清掉
  zap trash: [
    "~/Library/Application Support/clipnest",
    "~/Library/LaunchAgents/com.clipnest.app.plist",
  ]
end
RUBY

echo "== 生成 Windows 安装脚本（把 OWNER/REPO 烤进去）"
WIN_SHA=$(grep "windows-x86_64.zip" "$OUT/SHA256SUMS" | awk '{print $1}')
for f in install.sh install.ps1; do
  sed -e "s|__CLIPNEST_REPO__|${OWNER}/${REPO}|g" \
      -e "s|__CLIPNEST_GITEE_REPO__|${GITEE_OWNER}/${GITEE_REPO}|g" \
      "tools/$f" >"$OUT/$f"
  chmod +x "$OUT/$f"
done

echo "== 生成 Gitee 用的变体（URL 指向 gitee 的发行版，供国内 tap/bucket 用）"
sed -e "s|https://github.com/${OWNER}/${REPO}|https://gitee.com/${GITEE_OWNER}/${GITEE_REPO}|g" \
  "$OUT/clipnest.cask.rb" >"$OUT/clipnest.gitee.cask.rb"
sed -e "s|https://github.com/${OWNER}/${REPO}|https://gitee.com/${GITEE_OWNER}/${GITEE_REPO}|g" \
  "$OUT/clipnest.scoop.json" >"$OUT/clipnest.gitee.scoop.json"

echo "== 生成 Scoop manifest"
cat >"$OUT/clipnest.scoop.json" <<JSON
{
  "version": "${VERSION}",
  "description": "剪贴板历史管理器，自带 NAS 同步",
  "homepage": "https://github.com/${OWNER}/${REPO}",
  "license": "MIT",
  "url": "https://github.com/${OWNER}/${REPO}/releases/download/v${VERSION}/clipnest-${VERSION}-windows-x86_64.zip",
  "hash": "${WIN_SHA}",
  "shortcuts": [
    ["clipnest.exe", "clipnest"]
  ],
  "notes": [
    "数据在 %APPDATA% 下的 clipnest 目录；卸载不会删除，要清干净请手动删掉",
    "首次启动托盘出现 ❐ 图标，Ctrl+Shift+V 唤出面板"
  ],
  "checkver": {
    "github": "https://github.com/${OWNER}/${REPO}"
  },
  "autoupdate": {
    "architecture": {
      "64bit": {
        "url": "https://github.com/${OWNER}/${REPO}/releases/download/v\$version/clipnest-\$version-windows-x86_64.zip"
      }
    }
  }
}
JSON
python3 -m json.tool "$OUT/clipnest.scoop.json" >/dev/null || { echo "❌ scoop manifest 不是合法 JSON"; exit 1; }

echo "== 完成"
echo "   $OUT/clipnest.cask.rb      （放自建 tap: Casks/clipnest.rb）"
echo "   $OUT/clipnest.scoop.json   （放自建 bucket: bucket/clipnest.json）"
echo "   $OUT/install.sh / install.ps1（拷到仓库根目录，供一键安装用）"
echo ""
echo "对外可以宣传的安装方式："
echo "  brew tap ${OWNER}/clipnest && brew install --cask clipnest"
echo "  scoop bucket add clipnest https://github.com/${OWNER}/scoop-clipnest && scoop install clipnest"
echo "  curl -fsSL https://raw.githubusercontent.com/${OWNER}/${REPO}/main/install.sh | sh"
echo "  irm https://raw.githubusercontent.com/${OWNER}/${REPO}/main/install.ps1 | iex"
echo "  （国内把 raw.githubusercontent.com 换成 gitee.com/${GITEE_OWNER}/${GITEE_REPO}/raw/main）"
