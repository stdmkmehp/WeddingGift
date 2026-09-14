#!/usr/bin/env sh
# WeddingGift：真实数据导入脚本（docs/06-m6-runbook.md §2.7 第 3–5 步的固化）
#
# 做的事（仅操作数据库，不停起服务）：
#   1. 前置检查：wedding-app 必须已停止（先 ops/stop-app.sh）；仍在运行则
#      报错退出——容器持有 SQLite 连接时整库写入会留下悬空状态；
#   2. 上机复核（dry-run）：data validate --dir /data/wedding/import，
#      任一错误零写入，校验不过直接中止、不进入导入；
#   3. 单事务导入：data import（要求空库，任一错误全部回滚）；
#   4. 脱敏数量核对：status 输出组/成员/登录名称/礼品/密钥计数，
#      由操作者与离线事实源逐项比对（脚本不做自动比对）。
#
# 不做的事：
#   - 不停服、不起服：停/起由 ops/stop-app.sh 与 ops/start-app.sh 负责，
#     完成后自行 ops/start-app.sh 起服；
#   - 不做导入后回滚点备份：按 docs/06-m6-runbook.md §2.7 第 6 步显式执行
#     backup create；
#   - 不清理 /data/wedding/import 明文源文件：核对无误后按 §2.11 人工清理；
#   - 不动 app.key：密钥摘要与礼品密文均由它派生，重复导入须用同一把 key。
#
# 用法（在正式机器 /root/WeddingGift 仓库根执行）：
#   # 首次部署（容器尚不存在，直接执行）：
#   ops/import-data.sh
#   # 清库重导（开发测试循环）：
#   ops/stop-app.sh && ops/reset-db.sh && ops/import-data.sh && ops/start-app.sh

set -eu

REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
COMPOSE_FILE="${REPO_ROOT}/deploy/compose.yaml"
PROJECT="wedding"
CONTAINER="wedding-app"
IMPORT_DIR="/data/wedding/import"

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

echo "--- 1/3 上机复核（dry-run，任一错误零写入） ---"
if ! $DC -p "${PROJECT}" -f "${COMPOSE_FILE}" run --rm -T app data validate --dir "${IMPORT_DIR}"; then
  echo "校验未通过，未写入任何数据。修正离线数据后重新执行本脚本。"
  exit 1
fi

echo "--- 2/3 单事务导入（要求空库，任一错误全部回滚） ---"
$DC -p "${PROJECT}" -f "${COMPOSE_FILE}" run --rm -T app data import --dir "${IMPORT_DIR}"

echo "--- 3/3 脱敏数量核对（与离线事实源逐项比对：组/成员/登录名称/礼品/密钥） ---"
$DC -p "${PROJECT}" -f "${COMPOSE_FILE}" run --rm -T app status

echo ""
echo "导入完成。下一步："
echo "  # 回滚点备份（docs/06-m6-runbook.md §2.7 第 6 步）："
echo "  docker compose -p wedding -f deploy/compose.yaml run --rm -T app backup create \\"
echo "    --output \"/data/wedding/backups/wedding-pre-release-\$(date -u +%Y%m%d-%H%M%S).db\""
echo "  ops/start-app.sh        # 起服并等待健康"
echo "  # 核对无误后清理明文源文件（§2.11）：rm -rf /data/wedding/import/*"
