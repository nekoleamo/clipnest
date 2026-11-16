#!/usr/bin/env bash
# 生成本地自签代码签名身份（一台机器只做一次）。
#
# 为什么需要：ad-hoc 签名（`mygo build` 默认）的「指定要求」是 cdhash——二进制一变就换，
# 于是钥匙串里存的口令（clipnest-webdav / clipnest-sync）与辅助功能授权（TCC）都会在
# 重新打包后失效，需要重新点一遍。换成固定证书后指定要求是
#   identifier "com.clipnest.app" and certificate root = H"<证书>"
# 重打包不再变，授权一次就长期有效。
#
# 用法：tools/signing-identity.sh [身份名]     # 默认 clipnest-dev
# 之后 mygo.json 的 macos.signingIdentity 写这个名字（已写好）。
set -euo pipefail

NAME="${1:-clipnest-dev}"
DIR="$HOME/.clipnest-signing"

if security find-identity -v -p codesigning 2>/dev/null | grep -q "\"$NAME\""; then
  echo "✅ 已有可用身份：$NAME"
  exit 0
fi

mkdir -p "$DIR"
cd "$DIR"

cat > cs.cnf <<'CNF'
[req]
distinguished_name = dn
x509_extensions = ext
prompt = no
[dn]
CN = clipnest-dev
O = clipnest
[ext]
basicConstraints = critical,CA:false
keyUsage = critical,digitalSignature
extendedKeyUsage = critical,codeSigning
subjectKeyIdentifier = hash
CNF

# 1) 自签证书（10 年）+ 私钥
openssl req -x509 -newkey rsa:2048 -nodes -keyout signing.key -out signing.crt \
  -days 3650 -config cs.cnf >/dev/null 2>&1
# 2) 打包成 p12；必须走 legacy 算法，macOS 的导入器不认 OpenSSL 3 的默认 PBKDF2/AES
openssl pkcs12 -export -inkey signing.key -in signing.crt -out identity.p12 \
  -name "$NAME" -passout pass:clipnest -legacy \
  -keypbe PBE-SHA1-3DES -certpbe PBE-SHA1-3DES -macalg sha1 >/dev/null 2>&1
# 3) 导入登录钥匙串，只信任给 codesign 用
security import identity.p12 -k "$HOME/Library/Keychains/login.keychain-db" \
  -P clipnest -T /usr/bin/codesign >/dev/null
# 4) 只对「代码签名」用途授权信任（用户域，不需要 sudo）
security add-trusted-cert -r trustRoot -p codeSign \
  -k "$HOME/Library/Keychains/login.keychain-db" signing.crt >/dev/null

echo "✅ 已创建身份：$NAME"
security find-identity -v -p codesigning | grep "$NAME" || true
echo "证书与私钥在 $DIR（不要入库、不要外传）"
