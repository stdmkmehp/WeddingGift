#!/usr/bin/env sh
# WeddingGift：在线追加数据脚本（docs/06-m6-runbook.md §6.4 的固化）
#
# 做的事（无需停服，服务保持运行）：
#   1. dry-run 复核：data append --dir /data/wedding/append --dry-run
#      （开放世界校验：库内现状 ∪ 追加包；任一问题零写入，直接中止）；
#   2. 单事务追加：data append（纯 INSERT 短事务，任一错误整体回滚；
#      WAL + busy_timeout，与在线流量并发安全）；
#   3. 脱敏数量核对：status 输出，由操作者与离线事实源逐项比对。
#
# 不做的事：
#   - 不停服、不起服：追加为在线操作，与 import 的停服纪律相反；
#   - 不做追加前回滚点备份：按 docs/06-m6-runbook.md §6.4 显式执行 backup create；
#   - 不清理 /data/wedding/append 明文源文件：核对无误后按 §6.4 第 4 步人工清理；
#   - 不支持新增密钥：追加包含 access_keys.csv 数据行会被 data append 拒绝；
#     新朋友使用尚未绑定的既有密钥首登绑定。
#
# 用法（在正式机器 /root/WeddingGift 仓库根执行）：
#   # 1. 上传追加数据包（五类实体契约 CSV，允许缺失/仅表头；文件 0644 root:root）
#   scp -r <追加数据目录>/ root@106.54.173.55:/data/wedding/append/
#   # 2. 追加前回滚点备份（见 §6.4）
#   # 3. 在线追加（服务保持运行）
#   ops/append-data.sh

set -eu

REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
COMPOSE_FILE="${REPO_ROOT}/deploy/compose.yaml"
PROJECT="wedding"
APPEND_DIR="/data/wedding/append"

# 兼容 docker compose 插件与 standalone docker-compose。
if docker compose version >/dev/null 2>&1; then
  DC="docker compose"
else
  DC="docker-compose"
fi

echo "--- 1/3 dry-run 复核（开放世界校验：库内现状 ∪ 追加包，任一问题零写入） ---"
if ! $DC -p "${PROJECT}" -f "${COMPOSE_FILE}" run --rm -T app data append --dir "${APPEND_DIR}" --dry-run; then
  echo "校验未通过，未写入任何数据。修正离线数据后重新执行本脚本。"
  exit 1
fi

echo "--- 2/3 单事务追加（纯 INSERT，任一错误整体回滚） ---"
$DC -p "${PROJECT}" -f "${COMPOSE_FILE}" run --rm -T app data append --dir "${APPEND_DIR}"

echo "--- 3/3 脱敏数量核对（与离线事实源逐项比对：组/成员/登录名称/礼品类型/礼品） ---"
$DC -p "${PROJECT}" -f "${COMPOSE_FILE}" run --rm -T app status

echo ""
echo "追加完成。下一步："
echo "  # 核对无误后清理明文源文件：rm -rf /data/wedding/append/*"
