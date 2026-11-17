#!/usr/bin/env bash
# pre-push 校验：push 之前把「每轮收尾」那三件事跑一遍，别把红着的代码推到远端。
#
# 装上（仓库内一次性）：
#   git config core.hooksPath .githooks
#
# 临时跳过（确实要推一个中间状态）：
#   SKIP_PREPUSH=1 git push
#
# 设计取舍：只跑 vet + test + Windows 交叉编译（约 20 秒），
# 不跑 gofmt/静态审查那些更慢的东西——它们放在日常流程里，不该卡在 push 上。
set -u
cd "$(dirname "$0")/.." || exit 1

if [ "${SKIP_PREPUSH:-}" = "1" ]; then
  echo "⏭  SKIP_PREPUSH=1，跳过校验"
  exit 0
fi

fail() { echo "❌ $*"; exit 1; }

echo "== go vet"
go vet ./... || fail "go vet 有告警/报错"

echo "== go test"
go test ./... || fail "测试没过"

echo "== 交叉编译 windows/amd64"
GOOS=windows GOARCH=amd64 go build ./... || fail "Windows 交叉编译失败"

echo "✅ pre-push 通过"
