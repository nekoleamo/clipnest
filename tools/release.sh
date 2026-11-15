#!/bin/bash
# 打发布包：macOS（.app → zip）与 Windows（nsh 的 zip），并生成 SHA256SUMS 与 cask 片段。
#
# 用法：
#   tools/release.sh                 # 版本号取自 nsh.json
#   tools/release.sh 1.0.1           # 临时指定版本号
#   OWNER=xxx REPO=clipnest tools/release.sh   # 生成 cask 时用真实仓库地址
#
# 产物在 dist/：clipnest-<ver>-macos.zip、clipnest-<ver>-windows.zip、SHA256SUMS、clipnest.cask.rb
set -euo pipefail

cd "$(dirname "$0")/.."
source "$HOME/.cargo/env"

VERSION="${1:-$(python3 -c "import json;print(json.load(open('nsh.json'))['version'])")}"
OWNER="${OWNER:-OWNER}"
REPO="${REPO:-clipnest}"
OUT="dist"
mkdir -p "$OUT"

echo "== 版本 ${VERSION}（${OWNER}/${REPO}）"

echo "== 测试与静态检查"
cargo test --quiet
cargo clippy --all-targets --quiet -- -D warnings
cargo xwin clippy --target x86_64-pc-windows-msvc --all-targets --quiet -- -D warnings

echo "== 打包 macOS"
nsh package --target macos
# ditto 保留符号链接与扩展属性；zip 里的 .app 才能直接跑
rm -f "${OUT}/clipnest-${VERSION}-macos.zip"
ditto -c -k --sequesterRsrc --keepParent .nsh/out/clipnest.app "${OUT}/clipnest-${VERSION}-macos.zip"

echo "== 打包 Windows"
nsh package --target windows
cp .nsh/out/clipnest.zip "${OUT}/clipnest-${VERSION}-windows.zip"

echo "== 校验和"
(cd "$OUT" && shasum -a 256 "clipnest-${VERSION}-macos.zip" "clipnest-${VERSION}-windows.zip" > SHA256SUMS)
cat "$OUT/SHA256SUMS"

echo "== 生成 cask 片段"
MAC_SHA=$(grep "macos.zip" "$OUT/SHA256SUMS" | awk '{print $1}')
sed -e "s/@VERSION@/${VERSION}/g" \
    -e "s/@SHA256@/${MAC_SHA}/g" \
    -e "s/@OWNER@/${OWNER}/g" \
    -e "s/@REPO@/${REPO}/g" \
    packaging/homebrew/clipnest.rb.template > "${OUT}/clipnest.cask.rb"

cat <<EOF

== 下一步（手动，需要仓库已建好）
1. 上传到 GitHub Releases：
   gh release create "v${VERSION}" "${OUT}/clipnest-${VERSION}-macos.zip" "${OUT}/clipnest-${VERSION}-windows.zip" \\
      --title "clipnest ${VERSION}" --notes-file "docs/发布说明-${VERSION}.md"
2. 把 ${OUT}/clipnest.cask.rb 放到 tap 仓库（<owner>/homebrew-<repo>）的 Casks/clipnest.rb
3. Windows 用户装完后会被 SmartScreen 拦一次：更多信息 → 仍要运行（未签名 exe 的正常提示）

未公证说明：macOS 首次打开需右键 → 打开；macOS 15+ 在「系统设置 → 隐私与安全性 → 仍要打开」。
用 brew 装的话 cask 的 postflight 已删掉 quarantine，不会有这一步。
EOF
