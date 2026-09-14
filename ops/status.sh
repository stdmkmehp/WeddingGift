#!/usr/bin/env sh
# WeddingGift：输出数据库脱敏统计（内部编号与数量，不含明文姓名/密钥/礼品码）
#
# 做的事：执行 wedding status，打印组/成员/登录名称/密钥/礼品/绑定/会话计数，
#   供与离线事实源逐项比对或日常巡检。只读操作，容器运行或停止时均可执行。
#
# 不做的事：不写数据库、不停起服务、不触碰备份与 nginx。
#
# 用法（在正式机器 /root/WeddingGift 仓库根执行）：
#   ops/status.sh

set -eu

REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
COMPOSE_FILE="${REPO_ROOT}/deploy/compose.yaml"
PROJECT="wedding"

# 兼容 docker compose 插件与 standalone docker-compose。
if docker compose version >/dev/null 2>&1; then
  DC="docker compose"
else
  DC="docker-compose"
fi

$DC -p "${PROJECT}" -f "${COMPOSE_FILE}" run --rm -T app status
