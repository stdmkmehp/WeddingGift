#!/usr/bin/env sh
# WeddingGift 真实 URL 公网验证 + Costra 回归
#
# 可在任意能访问公网的机器执行；若在 staging 本机执行，附加 docker 容器与资源回归。
#
# 检查项：
#   1. /wedding/ 200，且为 M5 真实单页应用（含应用标识与相对资源引用）；
#   2. /wedding 301 → /wedding/；
#   3. 相对静态资源（assets/app.css、assets/app.js）200，无根路径 404；
#   4. /wedding/healthz 200 且 status/database 均 ok；
#   5. 页面 HTML 不含域名根路径绝对引用（href="/、src="/）；
#   6. bootstrap API：200、无会话时 session 为 null、响应 no-store；
#   7. API 防护：reveal 无会话 401、login 缺 Origin 403、非法 Host 404；
#   8. 主页安全响应头（CSP / nosniff / DENY）；
#   9. Costra 回归：/healthz 仍 200，根路径 / 仍兜底 404，未占用 /v1/；
#  10. （staging 本机）costra-* 容器在线、wedding-app 健康、整机资源占用复核。
#
# 历史：M2 版本曾检查占位页「链路验证」字样；M5 起主页为真实单页应用，
# 标识改为页面标题与登录表单结构。
#
# 用法：
#   ops/verify-public.sh [BASE_URL]
#   默认 BASE_URL=https://stg.costra.cloud/wedding/

set -eu

BASE_URL="${1:-https://stg.costra.cloud/wedding/}"
# 去掉尾斜杠得到前缀，便于拼接。
PREFIX="${BASE_URL%/}"
# 站点根（用于 Costra 回归）：从 https://host/wedding 提取 https://host。
SITE_ROOT="$(echo "${PREFIX}" | sed -E 's|(https?://[^/]*).*|\1|')"

PASS=0
FAIL=0

pass() { echo "PASS  $1"; PASS=$((PASS + 1)); }
fail() { echo "FAIL  $1"; FAIL=$((FAIL + 1)); }

check() {
  # check <描述> <实际值> <期望值>
  if [ "$2" = "$3" ]; then
    pass "$1（$2）"
  else
    fail "$1：实际=$2 期望=$3"
  fi
}

http_code() {
  curl -s -o /dev/null -w '%{http_code}' --max-time 20 "$1" || echo "curl失败"
}

echo "=== WeddingGift 真实 URL 验证：${BASE_URL} ==="

echo "--- 1. 主页（M5 真实单页应用） ---"
HTML="$(curl -s --max-time 20 "${PREFIX}/" || true)"
check "GET ${PREFIX}/ 状态码" "$(http_code "${PREFIX}/")" "200"
if echo "${HTML}" | grep -q '<title>婚礼回礼</title>' && echo "${HTML}" | grep -q 'id="login-form"'; then
  pass "主页为 WeddingGift 真实单页应用"
else
  fail "主页缺少应用标识（title/登录表单），可能未命中 WeddingGift"
fi

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
if echo "${HEALTH_BODY}" | grep -q '"status":"ok"' && echo "${HEALTH_BODY}" | grep -q '"database":"ok"'; then
  pass "healthz 返回 status/database 均 ok"
else
  fail "healthz 响应异常：${HEALTH_BODY}"
fi

echo "--- 5. 页面无域名根路径绝对引用 ---"
# href="/ 或 src="/ 会被浏览器解析到域名根，命中 Costra 兜底 404（协议相对 // 除外，本项目不用）。
if echo "${HTML}" | grep -qE '(href|src)="/[^/]'; then
  fail "页面存在根路径绝对引用：$(echo "${HTML}" | grep -oE '(href|src)="/[^"]*' | head -3)"
else
  pass "页面全部为相对路径引用"
fi

echo "--- 6. bootstrap API ---"
BOOT_HEADERS="$(curl -s -D - -o /tmp/wg-verify-boot.$$.json --max-time 20 "${PREFIX}/api/bootstrap" || true)"
BOOT_BODY="$(cat /tmp/wg-verify-boot.$$.json 2>/dev/null || true)"; rm -f /tmp/wg-verify-boot.$$.json
check "GET ${PREFIX}/api/bootstrap 状态码" "$(http_code "${PREFIX}/api/bootstrap")" "200"
if echo "${BOOT_BODY}" | grep -q '"session":null'; then
  pass "无会话时 bootstrap 返回 session null"
else
  fail "bootstrap 响应缺少 session null：${BOOT_BODY}"
fi
if echo "${BOOT_HEADERS}" | grep -qi '^Cache-Control: no-store'; then
  pass "bootstrap 响应 no-store"
else
  fail "bootstrap 响应缺少 no-store"
fi
if echo "${BOOT_BODY}" | grep -q '"icpFilingText"' && echo "${BOOT_BODY}" | grep -q '"maintenanceText"'; then
  pass "bootstrap 携带文案配置（备案/维护文案）"
else
  fail "bootstrap 文案字段不完整：${BOOT_BODY}"
fi

echo "--- 7. API 防护 ---"
check "reveal 无会话 401" \
  "$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 -X POST "${PREFIX}/api/gifts/reveal" \
     -H 'Content-Type: application/json' -H "Origin: ${SITE_ROOT}" -d '{"giftId":"abc"}' || true)" "401"
check "login 缺 Origin 403" \
  "$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 -X POST "${PREFIX}/api/auth/login" \
     -H 'Content-Type: application/json' -d '{"loginName":"x","keyCode":"00000000"}' || true)" "403"
check "login 非法 Host 404" \
  "$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 -X POST "${PREFIX}/api/auth/login" \
     -H 'Host: invalid.example.org' -H 'Content-Type: application/json' -H "Origin: ${SITE_ROOT}" \
     -d '{"loginName":"x","keyCode":"00000000"}' || true)" "404"
check "login 非 JSON 415" \
  "$(curl -s -o /dev/null -w '%{http_code}' --max-time 20 -X POST "${PREFIX}/api/auth/login" \
     -H 'Content-Type: text/plain' -H "Origin: ${SITE_ROOT}" -d 'x' || true)" "415"

echo "--- 8. 主页安全响应头 ---"
INDEX_HEADERS="$(curl -s -D - -o /dev/null --max-time 20 "${PREFIX}/" || true)"
for H in 'Content-Security-Policy:' 'X-Content-Type-Options: nosniff' 'X-Frame-Options: DENY' 'Referrer-Policy: no-referrer'; do
  if echo "${INDEX_HEADERS}" | grep -qi "^${H}"; then
    pass "响应头 ${H}"
  else
    fail "响应头缺少 ${H}"
  fi
done

echo "--- 9. Costra 原路径回归 ---"
check "Costra 健康检查 ${SITE_ROOT}/healthz" "$(http_code "${SITE_ROOT}/healthz")" "200"
check "Costra 兜底 ${SITE_ROOT}/ 返回 404" "$(http_code "${SITE_ROOT}/")" "404"
check "未占用 Costra /v1/ 前缀（期望非 WeddingGift 内容）" \
  "$(curl -s --max-time 20 "${SITE_ROOT}/v1/" | grep -c '婚礼回礼' || true)" "0"

echo "--- 10. staging 本机回归（检测到 docker 与 wedding-app 时执行） ---"
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
echo "真实 URL 验证全部通过。"
