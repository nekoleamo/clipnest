#!/usr/bin/env bash
# 打发布包（Go + mygo 版）：测试/静态检查 → 双平台产物 → zip → SHA256SUMS → cask。
#
# 用法：
#   tools/release-go.sh                 # 版本号取自 mygo.json
#   tools/release-go.sh 1.0.1           # 临时指定版本号
#   OWNER=xxx REPO=clipnest tools/release-go.sh
#
# 产物在 dist/：
#   clipnest-<ver>-macos.zip         （.app，ditto 打包保留符号链接）
#   clipnest-<ver>-windows-x86_64.zip（clipnest.exe）
#   clipnest-<ver>-<arch>.dmg        （mygo 生成，方便直装）
#   SHA256SUMS、clipnest.cask.rb
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${1:-$(python3 -c "import json;print(json.load(open('mygo.json'))['version'])")}"
OWNER="${OWNER:-OWNER}"
REPO="${REPO:-clipnest}"
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

echo "== 完成：$OUT/clipnest.cask.rb"
