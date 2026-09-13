#!/usr/bin/env sh
# WeddingGift WG-601：一条命令可运行的核心自动化回归
#
# 覆盖（全部通过才算 G6 前的本地回归绿）：
#   1. gofmt 格式净检查；
#   2. go vet 静态检查；
#   3. go test -race -count=1 ./...（单元 / 数据校验 / repository / 绑定一致性 /
#      HTTP / 安全回归 / 来宾旅程 E2E；不依赖真实数据与测试顺序）；
#   4. CGO_ENABLED=0 GOOS=linux 静态构建（与发布镜像同一形态）；
#   5. fixtures 数据契约自检（data validate，虚构数据包始终合法）；
#   6. 前端静态检查（路径前缀纪律 / 危险 API / 外部资源白名单）；
#   7. 秘密扫描（敏感文件、私钥材料、虚构数据标记）。
#
# 用法（仓库根目录）：
#   ops/test.sh

set -eu

REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "${REPO_ROOT}"

fail() {
  echo "FAIL  $1"
  exit 1
}

echo "=== 1/7 gofmt 格式检查 ==="
UNFORMATTED="$(gofmt -l cmd internal migrations web 2>/dev/null)"
[ -z "${UNFORMATTED}" ] || { echo "${UNFORMATTED}"; fail "存在未格式化文件"; }
echo "PASS  gofmt 净"

echo "=== 2/7 go vet 静态检查 ==="
go vet ./... || fail "go vet"
echo "PASS  go vet"

echo "=== 3/7 go test -race（全量回归） ==="
go test -race -count=1 ./... || fail "go test -race"
echo "PASS  全部测试"

echo "=== 4/7 发布形态静态构建（CGO_ENABLED=0 GOOS=linux） ==="
TMPBIN="$(mktemp -t wedding-build.XXXXXX)"
trap 'rm -f "${TMPBIN}"' EXIT
CGO_ENABLED=0 GOOS=linux go build -trimpath -o "${TMPBIN}" ./cmd/wedding || fail "静态构建"
echo "PASS  静态构建"

echo "=== 5/7 fixtures 数据契约自检 ==="
go run ./cmd/wedding data validate --dir testdata/fixtures >/dev/null || fail "fixtures 契约校验"
echo "PASS  虚构数据包契约合法"

echo "=== 6/7 前端静态检查 ==="
# 路径前缀纪律：禁止域名根路径绝对引用（href="/、src="/；协议相对 // 本项目不用）。
if grep -rnE '(href|src)="/[^/]' web/ ; then
  fail "存在根路径绝对引用（/wedding/ 前缀下会 404）"
fi
# 危险 API：形态化匹配，避免命中注释中的说明文字。
if grep -rnE '\.(innerHTML|outerHTML)[[:space:]]*=|insertAdjacentHTML[[:space:]]*\(|document\.write[[:space:]]*\(|eval[[:space:]]*\(|(window\.)?(localStorage|sessionStorage)\.|indexedDB\.|navigator\.serviceWorker' web/assets/*.js web/index.html ; then
  fail "前端存在危险 API（动态内容必须 textContent，禁用持久存储）"
fi
# 外部资源白名单：仅允许备案链接；CSS url() 不允许任何 http 引用。
EXTERNAL="$(grep -rnE '(href|src)="https?://' web/ | grep -v 'beian\.miit\.gov\.cn' || true)"
[ -z "${EXTERNAL}" ] || { echo "${EXTERNAL}"; fail "存在白名单外的外部资源引用"; }
if grep -rnE 'url\([[:space:]]*["'"'"']?https?://' web/ ; then
  fail "CSS 存在外部 url() 引用"
fi
echo "PASS  前端路径/API/外部资源"

echo "=== 7/7 秘密扫描 ==="
# 敏感文件绝不进 Git（密钥、数据库、私钥、证书）。
TRACKED="$(git ls-files | grep -iE '(^|/)(app\.key|secrets/)|\.(key|db|db-wal|db-shm|pem|p12|pfx)$' || true)"
[ -z "${TRACKED}" ] || { echo "${TRACKED}"; fail "Git 中存在敏感文件"; }
# 私钥材料扫描（模式拼接书写，避免命中本脚本自身）。
# .git 与 .codebuddy（AI 工具状态目录，可能合法引用该字样）不属于项目交付物，不扫描。
if grep -rnI --exclude-dir=.git --exclude-dir=.codebuddy 'PRIVATE'' KEY' . ; then
  fail "仓库中疑似存在私钥材料"
fi
# 虚构数据标记：testdata 礼品码必须 FAKE 前缀，卡片密钥必须 2026 虚构号段，
# 防止真实礼品码/密钥误混入虚构包。
if awk -F, 'NR>1 && $4 !~ /^FAKE/ {print; bad=1} END {exit bad}' testdata/fixtures/gift_items.csv ; then
  :;
else
  fail "testdata 礼品码缺少 FAKE 前缀（疑似真实数据混入）"
fi
if awk -F, 'NR>1 && $2 !~ /^2026[0-9]{4}$/ {print; bad=1} END {exit bad}' testdata/fixtures/access_keys.csv ; then
  :;
else
  fail "testdata 卡片密钥不在 2026 虚构号段（疑似真实密钥混入）"
fi
echo "PASS  秘密扫描"

echo ""
echo "=== WG-601 本地自动化回归全部通过 ==="
