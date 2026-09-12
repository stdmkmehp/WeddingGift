#!/usr/bin/env sh
# WeddingGift WG-604：canary 上线冒烟脚本
#
# 用数据包中预置的 canary 虚构组/密钥，对真实 URL 跑一遍完整鉴权与礼品链路，
# 覆盖 WG-604 冒烟清单的 API 部分：
#   首绑登录、同组多名称再登录、同组另一成员登录、跨组拒绝、组内第二密钥拒绝、
#   多礼品摘要与逐份 reveal、Cookie Path 收编（/wedding/）、退出与旧会话失效。
#
# 前提：真实数据包中含两个 canary 组与三把 canary 密钥（见 docs/06-m6-runbook.md），
# 默认取值与手册模板一致，可用环境变量覆盖。
#
# 注意：冒烟会在数据库留下 canary 组的绑定关系与会话记录（虚构数据，无敏感值）；
# 失败登录会产生少量限流计数（远低于阈值）。真实来宾数据不受影响。
#
# 用法：
#   ops/canary-smoke.sh [BASE_URL]
#   BASE_URL 默认 https://stg.costra.cloud/wedding/；也可 http://127.0.0.1:3000/（本地）

set -eu

BASE_URL="${1:-https://stg.costra.cloud/wedding/}"
PREFIX="${BASE_URL%/}"
SITE_ROOT="$(echo "${PREFIX}" | sed -E 's|(https?://[^/]*).*|\1|')"
# URL 路径部分（如 /wedding）；非空时 Cookie 应被 nginx 收编到该前缀下。
URL_PATH="$(echo "${PREFIX}" | sed -E 's|https?://[^/]*||')"

# canary 数据（环境变量可覆盖；默认与 docs/06-m6-runbook.md 模板一致）。
A_NAME="${CANARY_A_NAME:-金丝雀甲}"
A_NICK="${CANARY_A_NICK:-小雀甲}"
B_NAME="${CANARY_B_NAME:-金丝雀乙}"
A_SALUTATION="${CANARY_A_SALUTATION:-金丝雀甲测试组}"
KEY1="${CANARY_KEY_1:-20269001}"
KEY2="${CANARY_KEY_2:-20269002}"
KEY3="${CANARY_KEY_3:-20269003}"

PASS=0
FAIL=0
pass() { echo "PASS  $1"; PASS=$((PASS + 1)); }
fail() { echo "FAIL  $1"; FAIL=$((FAIL + 1)); }
check() {
  if [ "$2" = "$3" ]; then pass "$1（$2）"; else fail "$1：实际=$2 期望=$3"; fi
}

JAR_A="$(mktemp -t wg-canary-a.XXXXXX)"
JAR_B="$(mktemp -t wg-canary-b.XXXXXX)"
HDR="$(mktemp -t wg-canary-hdr.XXXXXX)"
trap 'rm -f "${JAR_A}" "${JAR_B}" "${HDR}"' EXIT

# call <方法> <路径> [JSON体] [jar] —— 执行请求，结果写入全局 BODY（响应体）与 CODE（状态码）。
# 注意：必须直接调用（不要放进 $() 命令替换），子 shell 会丢掉全局变量。
call() {
  M="$1"; P="$2"; D="${3:-}"; J="${4:-}"
  A="-s -w \n%{http_code} --max-time 20 -X ${M} -H Content-Type:application/json -H Origin:${SITE_ROOT}"
  if [ -n "${J}" ]; then A="${A} -b ${J} -c ${J}"; fi
  if [ -n "${D}" ]; then A="${A} -d ${D}"; fi
  # shellcheck disable=SC2086
  RESP="$(curl ${A} "${PREFIX}${P}" || true)"
  CODE="$(printf '%s' "${RESP}" | tail -n1)"
  BODY="$(printf '%s' "${RESP}" | sed '$d')"
}

echo "=== WeddingGift canary 冒烟：${BASE_URL} ==="
echo "（canary 组：${A_NAME} / ${B_NAME}；本脚本只触碰虚构 canary 数据）"

echo "--- 1. 初始化 ---"
call GET /api/bootstrap
check "bootstrap 状态码" "${CODE}" "200"
printf '%s' "${BODY}" | grep -q '"session":null' && pass "无会话 session null" || fail "bootstrap 未返回 session null：${BODY}"

echo "--- 2. 组 A 首绑（${A_NAME} + KEY1） ---"
call POST /api/auth/login "{\"loginName\":\"${A_NAME}\",\"keyCode\":\"${KEY1}\"}" "${JAR_A}"
check "组 A 登录状态码" "${CODE}" "200"
printf '%s' "${BODY}" | grep -q "\"salutation\":\"${A_SALUTATION}\"" \
  && pass "组 A 称呼正确" || fail "组 A 称呼不符：${BODY}"

echo "--- 3. 昵称同组再登录 + Cookie 属性与 Path 收编 ---"
NICK_CODE="$(curl -s -D "${HDR}" -o /dev/null -w '%{http_code}' --max-time 20 -X POST "${PREFIX}/api/auth/login" \
  -H 'Content-Type: application/json' -H "Origin: ${SITE_ROOT}" \
  -d "{\"loginName\":\"${A_NICK}\",\"keyCode\":\"${KEY1}\"}" -c "${JAR_A}" -b "${JAR_A}" || true)"
check "昵称 ${A_NICK} 同组再登录状态码" "${NICK_CODE}" "200"
SETCOOKIE="$(grep -i '^Set-Cookie:' "${HDR}" || true)"
printf '%s' "${SETCOOKIE}" | grep -qi 'wedding_session=' && pass "Set-Cookie 含 wedding_session" || fail "缺少会话 Cookie：${SETCOOKIE}"
printf '%s' "${SETCOOKIE}" | grep -qi 'HttpOnly' && pass "Cookie HttpOnly" || fail "Cookie 缺 HttpOnly"
printf '%s' "${SETCOOKIE}" | grep -qi 'Secure' && pass "Cookie Secure" || fail "Cookie 缺 Secure"
if [ -n "${URL_PATH}" ]; then
  printf '%s' "${SETCOOKIE}" | grep -qi "Path=${URL_PATH}/" \
    && pass "Cookie Path 已收编到 ${URL_PATH}/（nginx proxy_cookie_path 生效）" \
    || fail "Cookie Path 未收编到 ${URL_PATH}/：${SETCOOKIE}"
fi

echo "--- 4. 会话内 bootstrap 与多礼品摘要 ---"
call GET /api/bootstrap '' "${JAR_A}"
check "bootstrap 状态码" "${CODE}" "200"
printf '%s' "${BODY}" | grep -q '"gifts":\[{"id":"' && pass "礼品摘要非空" || fail "无礼品摘要：${BODY}"
GID1="$(printf '%s' "${BODY}" | grep -o '"gifts":\[{"id":"[^"]*' | sed 's/.*"id":"//')"
GIFT_N="$(printf '%s' "${BODY}" | grep -o '"id":"[^"]*"' | wc -l | tr -d ' ')"
[ "${GIFT_N}" -ge 1 ] && pass "礼品份数 ${GIFT_N}" || fail "礼品份数异常"

echo "--- 5. 逐份 reveal（第一份） ---"
call POST /api/gifts/reveal "{\"giftId\":\"${GID1}\"}" "${JAR_A}"
check "reveal 状态码" "${CODE}" "200"
printf '%s' "${BODY}" | grep -q '"code":"' && pass "reveal 返回激活码" || fail "reveal 无激活码：${BODY}"

echo "--- 6. 跨组拒绝（${B_NAME} + KEY1） ---"
call POST /api/auth/login "{\"loginName\":\"${B_NAME}\",\"keyCode\":\"${KEY1}\"}"
check "跨组登录状态码" "${CODE}" "401"
printf '%s' "${BODY}" | grep -q 'auth_failed' && pass "跨组失败为统一错误" || fail "跨组错误体异常：${BODY}"

echo "--- 7. 组 B 首绑（${B_NAME} + KEY2） ---"
call POST /api/auth/login "{\"loginName\":\"${B_NAME}\",\"keyCode\":\"${KEY2}\"}" "${JAR_B}"
check "组 B 登录状态码" "${CODE}" "200"

echo "--- 8. 已绑密钥跨组（${A_NAME} + KEY2） ---"
call POST /api/auth/login "{\"loginName\":\"${A_NAME}\",\"keyCode\":\"${KEY2}\"}"
check "已绑密钥跨组状态码" "${CODE}" "401"

echo "--- 9. 组内第二密钥拒绝（${A_NAME} + KEY3，组 A 已有 KEY1） ---"
call POST /api/auth/login "{\"loginName\":\"${A_NAME}\",\"keyCode\":\"${KEY3}\"}"
check "第二密钥拒绝状态码" "${CODE}" "401"

echo "--- 10. 退出与旧会话失效 ---"
call POST /api/auth/logout '' "${JAR_A}"
check "logout 状态码" "${CODE}" "200"
call GET /api/bootstrap '' "${JAR_A}"
printf '%s' "${BODY}" | grep -q '"session":null' && pass "退出后 session null" || fail "退出后会话仍有效：${BODY}"
call POST /api/gifts/reveal "{\"giftId\":\"${GID1}\"}" "${JAR_A}"
check "退出后 reveal 状态码" "${CODE}" "401"

echo "--- 11. 组 B 会话保持有效（不受组 A 退出影响） ---"
call GET /api/bootstrap '' "${JAR_B}"
printf '%s' "${BODY}" | grep -q '"gifts"' && pass "组 B 会话仍有效" || fail "组 B 会话意外失效：${BODY}"

echo ""
echo "=== canary 冒烟结果：${PASS} 通过，${FAIL} 失败 ==="
[ "${FAIL}" -eq 0 ] || exit 1
echo "canary 冒烟全部通过。后续：浏览器人工冒烟（复制/剪贴板降级/微信 WebView）与 Costra 回归见 docs/06-m6-runbook.md。"
