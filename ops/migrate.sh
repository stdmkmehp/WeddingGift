#!/usr/bin/env sh
# WeddingGift：数据库 schema 迁移脚本（应用内嵌 migrations 到 wedding.db）
#
# 做的事（仅操作数据库，不停起服务）：
#   1. 前置检查：wedding-app 必须已停止（先 ops/stop-app.sh）；仍在运行则
#      报错退出——容器持有 SQLite 连接时变更 schema 会留下悬空状态；
#   2. 执行 wedding migrate：按 migrations/NNNN_*.sql 文件名字典序应用待执行
#      版本，已应用版本由 SQLite user_version 记录，重复执行为 no-op（幂等）；
#      数据库版本高于二进制自带版本时报错，拒绝降级执行。
#
# 不做的事：
#   - 不停服、不起服：停/起由 ops/stop-app.sh 与 ops/start-app.sh 负责；
#   - 不备份数据库：发布流程的回滚点备份由操作者按 docs/06-m6-runbook.md
#     §5 显式执行；
#   - 不导入业务数据：导入见 ops/import-data.sh。
#
# 用法（在正式机器 /root/WeddingGift 仓库根执行）：
#   ops/migrate.sh
#
# 典型场景：
#   - 首次部署建 schema（docs/06-m6-runbook.md §2.6，容器尚不存在，直接执行）；
#   - 日常发布：ops/stop-app.sh → ops/migrate.sh → ops/start-app.sh（§5）；
#   - 被 ops/reset-db.sh / ops/restore-db.sh 内部复用（清库重建 / 备份前滚）。

set -eu

REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
COMPOSE_FILE="${REPO_ROOT}/deploy/compose.yaml"
PROJECT="wedding"
CONTAINER="wedding-app"

# 兼容 docker compose 插件与 standalone docker-compose。
if docker compose version >/dev/null 2>&1; then
  DC="docker compose"
else
  DC="docker-compose"
fi

echo "--- 前置检查：服务必须已停止 ---"
if [ "$(docker inspect -f '{{.State.Running}}' "${CONTAINER}" 2>/dev/null)" = "true" ]; then
  echo "错误：${CONTAINER} 仍在运行。本脚本仅操作数据库，请先停服："
  echo "  ops/stop-app.sh"
  exit 1
fi
echo "容器已停止（或不存在），继续。"

echo "--- 应用数据库 migration ---"
$DC -p "${PROJECT}" -f "${COMPOSE_FILE}" run --rm -T app migrate
