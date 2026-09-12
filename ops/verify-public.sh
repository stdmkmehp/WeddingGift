#!/usr/bin/env sh
# WeddingGift WG-203 验证：真实 URL 公网检查 + Costra 回归
#
# 可在任意能访问公网的机器执行；若在 staging 本机执行，附加 docker 容器与资源回归。
#
# 检查项（对应 G2 门禁）：
#   1. /wedding/ 200，且为 WeddingGift 链路验证页；
#   2. /wedding 301 → /wedding/；
#   3. 相对静态资源（assets/app.css、assets/app.js）200，无根路径 404；
#   4. /wedding/healthz 200 且 status ok；
#   5. 页面 HTML 不含域名根路径绝对引用（href="/、src="/）；
#   6. Costra 回归：/healthz（Costra 健康检查）仍 200，根路径 / 仍为兜底 404；
#   7. （staging 本机）costra-* 容器在线、wedding-app 健康、整机资源占用复核。
#
# 用法：
#   ops/verify-public.sh [BASE_URL]
#   默认 BASE_URL=https://stg.costra.cloud/wedding/

set -eu

BASE_URL="${1:-https://stg.costra.cloud/wedding/}"
# 去掉尾斜杠得到前缀，便于拼接。
PREFIX="${BASE_URL%/}"
# 站点根（用于 Costra 回归）：从 https://host/wedding 提取 https://host。
SITE_ROOT="$(echo "${PREFIX}" | sed 's|\(https\?://[^/]*\).*|\1|')"

PASS=0
FAIL=0

check() {
  # check <描述> <实际值> <期望值>
  if [ "$2" = "$3" ]; then
    echo "PASS  $1（$2）"
    PASS=$((PASS + 1))
  else
    echo "FAIL  $1：实际=$2 期望=$3"
    FAIL=$((FAIL + 1))
  fi
}

http_code() {
  curl -s -o /dev/null -w '%{http_code}' --max-time 20 "$1" || echo "curl失败"
}

echo "=== WeddingGift 真实 URL 验证：${BASE_URL} ==="

echo "--- 1. 主页 ---"
HTML="$(curl -s --max-time 20 "${PREFIX}/" || true)"
check "GET ${PREFIX}/ 状态码" "$(http_code "${PREFIX}/")" "200"
echo "${HTML}" | grep -q "链路验证" \
  && { echo "PASS  主页内容为 WeddingGift 链路验证页"; PASS=$((PASS + 1)); } \
  || { echo "FAIL  主页内容不含『链路验证』标识，可能未命中 WeddingGift"; FAIL=$((FAIL + 1)); }

echo "--- 2. 无尾斜杠重定向 ---"
REDIR_CODE="$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 "${PREFIX}" || true)"
REDIR_LOC="$(curl -s -o /dev/null -w '%{redirect_url}' --max-time 20 "${PREFIX}" || true)"
check "GET ${PREFIX} 状态码" "${REDIR_CODE}" "301"
check "Location 指向 /wedding/" "${REDIR_LOC}" "${PREFIX}/"

echo "--- 3. 相对静态资源（无根路径 404） ---"
check "GET ${PREFIX}/assets/app.css" "$(http_code "${PREFIX}/assets/app.css")" "200"
check "GET ${PREFIX}/assets/app.js" "$(http_code "${PREFIX}/assets/app.js")" "200"

echo "--- 4. 健康检查 ---"
HEALTH_BODY="$(curl -s --max-time 20 "${PREFIX}/healthz" || true)"
check "GET ${PREFIX}/healthz 状态码" "$(http_code "${PREFIX}/healthz")" "200"
echo "${HEALTH_BODY}" | grep -q '"status":"ok"' \
  && { echo "PASS  healthz 返回 status ok"; PASS=$((PASS + 1)); } \
  || { echo "FAIL  healthz 响应异常：${HEALTH_BODY}"; FAIL=$((FAIL + 1)); }

echo "--- 5. 页面无域名根路径绝对引用 ---"
# href="/ 或 src="/ 会被浏览器解析到域名根，命中 Costra 兜底 404（协议相对 // 除外，本项目不用）。
if echo "${HTML}" | grep -qE '(href|src)="/[^/]'; then
  echo "FAIL  页面存在根路径绝对引用："
  echo "${HTML}" | grep -nE '(href|src)="/[^/]'
  FAIL=$((FAIL + 1))
else
  echo "PASS  页面全部为相对路径引用"
  PASS=$((PASS + 1))
fi

echo "--- 6. Costra 原路径回归 ---"
check "Costra 健康检查 ${SITE_ROOT}/healthz" "$(http_code "${SITE_ROOT}/healthz")" "200"
check "Costra 兜底 ${SITE_ROOT}/ 返回 404" "$(http_code "${SITE_ROOT}/")" "404"
check "未占用 Costra /v1/ 前缀（期望非 WeddingGift 内容）" \
  "$(curl -s --max-time 20 "${SITE_ROOT}/v1/" | grep -c '链路验证' || true)" "0"

echo "--- 7. staging 本机回归（检测到 docker 与 wedding-app 时执行） ---"
if command -v docker >/dev/null 2>&1 && docker inspect wedding-app >/dev/null 2>&1; then
  check "wedding-app 健康状态" "$(docker inspect -f '{{.State.Health.Status}}' wedding-app)" "healthy"
  for c in costra-nginx costra-server costra-postgres costra-redis costra-standby-pg; do
    STATE="$(docker inspect -f '{{.State.Status}}' "$c" 2>/dev/null || echo 'missing')"
    check "Costra 容器 $c 在线" "${STATE}" "running"
  done
  echo "--- 整机资源占用（复核峰值叠加余量） ---"
  docker stats --no-stream || true
else
  echo "（非 staging 本机或无 docker，跳过容器回归）"
fi

echo ""
echo "=== 结果：${PASS} 通过，${FAIL} 失败 ==="
[ "${FAIL}" -eq 0 ] || exit 1
echo "G2 验证全部通过，请将结果记录到 docs/05-m2-runbook.md 第 5 节。"
