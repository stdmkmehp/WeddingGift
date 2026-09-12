#!/usr/bin/env sh
# WeddingGift WG-305：本机一致性备份脚本（由宿主机 cron 调用）
#
# 做的事：
#   1. 通过 docker compose exec 调用容器内同版本二进制，用 SQLite online
#      backup API 把数据库一致性备份到 /data/wedding/backups/wedding-<UTC时间戳>.db
#      （应用侧完成完整性检查；宿主机不额外安装应用二进制）；
#   2. 轮转：只保留最近 72 份（≈3 天，每小时一份）。
#
# 不做的事：不写 /data/slave/backups 或其他 Costra 目录；不接 COS、不做异机备份。
#
# cron 安装见 deploy/wedding.cron（独立单元 /etc/cron.d/wedding，每小时 :44）。

set -eu

REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
COMPOSE_FILE="${REPO_ROOT}/deploy/compose.yaml"
PROJECT="wedding"
BACKUP_DIR="/data/wedding/backups"
KEEP=72

TS="$(date -u +%Y%m%d-%H%M%S)"
OUT="${BACKUP_DIR}/wedding-${TS}.db"

# 兼容 docker compose 插件与 standalone docker-compose。
if docker compose version >/dev/null 2>&1; then
  DC="docker compose"
else
  DC="docker-compose"
fi

mkdir -p "${BACKUP_DIR}"

$DC -p "${PROJECT}" -f "${COMPOSE_FILE}" exec -T app /app/wedding backup create --output "${OUT}"
echo "$(date -u +%FT%TZ) backup ok: ${OUT}"

# 轮转：按修改时间保留最新 KEEP 份，删除更老的备份。
ls -1t "${BACKUP_DIR}"/wedding-*.db 2>/dev/null | tail -n +"$((KEEP + 1))" | while read -r old; do
  rm -f "${old}"
  echo "$(date -u +%FT%TZ) rotated out: ${old}"
done
