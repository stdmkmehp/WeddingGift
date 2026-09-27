#!/usr/bin/env sh
# WeddingGift：修正 code 模式礼品激活码（docs/06-m6-runbook.md §6.6 的固化）
#
# 定位：「上线后只增不改不删」（D-21）的唯一例外——仅当既有 code 模式礼品的
# 激活码明文填错时使用；仅重写该礼品的 code_ciphertext，不触碰其他字段。
#
# 做的事（无需停服，服务保持运行，提交后下一请求即生效）：
#   1. 修正前回滚点备份（online backup，wedding-pre-fix-<TS>.db）；
#   2. 交互式两次隐藏输入新激活码并比对一致（不回显、不进 shell history、
#      不作命令行参数——避免进程列表泄露）；
#   3. compose run 一次性旁路容器执行 gift fix-code：明文经 stdin 传入，
#      事务内「加密 → UPDATE → 读回解密比对」后提交，任一失败整体回滚。
#
# 不做的事：
#   - 不停服、不起服（单行 UPDATE 短事务，WAL + busy_timeout 在线安全）；
#   - 仅支持 credential_mode=code；card_secret 由子命令明确拒绝；
#   - 不回显、不打印明文或旧值。
#
# 用法（在正式机器 /root/WeddingGift 仓库根执行）：
#   ops/fix-gift-code.sh GI003

set -eu

REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
COMPOSE_FILE="${REPO_ROOT}/deploy/compose.yaml"
PROJECT="wedding"
BACKUP_DIR="/data/wedding/backups"

REF="${1:-}"
case "$REF" in
  ""|*[!A-Za-z0-9_-]*)
    echo "用法: ops/fix-gift-code.sh <礼品 external_ref>（如 GI003）" >&2
    exit 2
    ;;
esac

# 兼容 docker compose 插件与 standalone docker-compose。
if docker compose version >/dev/null 2>&1; then
  DC="docker compose"
else
  DC="docker-compose"
fi

echo "--- 1/3 修正前回滚点备份 ---"
TS="$(date -u +%Y%m%d-%H%M%S)"
$DC -p "${PROJECT}" -f "${COMPOSE_FILE}" run --rm -T app backup create \
  --output "${BACKUP_DIR}/wedding-pre-fix-${TS}.db"

echo "--- 2/3 输入礼品 ${REF} 的新激活码（两次输入，不回显） ---"
read_secret() {
  stty -echo
  read -r "$1" || true
  stty echo
  printf '\n' >&2
}
printf '新激活码: ' >&2
read_secret CODE1
printf '再次输入: ' >&2
read_secret CODE2
if [ -z "$CODE1" ]; then
  echo "新激活码为空，未写入任何数据。" >&2
  exit 1
fi
if [ "$CODE1" != "$CODE2" ]; then
  echo "两次输入不一致，未写入任何数据。" >&2
  exit 1
fi

echo "--- 3/3 在线修正（单事务：加密 → UPDATE → 读回解密比对） ---"
printf '%s' "$CODE1" | $DC -p "${PROJECT}" -f "${COMPOSE_FILE}" run --rm -T app gift fix-code --ref "$REF"
unset CODE1 CODE2

echo ""
echo "修正完成。生效为即时（读路径无缓存直读库），可用真实访问链路人工核对一次 reveal。"
