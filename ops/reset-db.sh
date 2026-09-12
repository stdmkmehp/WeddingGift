#!/usr/bin/env sh
# WeddingGift：清库重置脚本（开发测试循环：清库 → 重新导入）
#
# 背景：应用不提供清库子命令；data import 要求空库（六张业务表均无数据，
# 见 internal/store IsEmpty），因此"重新导入"前必须先把数据库清回空库。
# 本脚本把这一流程固化为安全可重复的操作。
#
# 做的事：
#   1. 安全确认（--yes 或交互输入大写 YES），先打印当前脱敏统计供留档；
#   2. 清库前自动备份当前库到 backups/wedding-pre-reset-<UTC时间戳>.db；
#      备份失败（如库已损坏）则把现有 wedding.db* 移入 data/quarantine-<TS>/
#      保留，不销毁任何文件；
#   3. 停止 wedding-app → 删除 wedding.db 及 -wal/-shm → 重新 migrate 建 schema →
#      启动并等待健康（此时为空库，serve 正常对外，登录必然失败属预期）；
#   4. 打印重新导入的下一步命令。
#
# 不做的事：
#   - 不动 app.key：密钥摘要与礼品密文均由它派生，重新导入须用同一把 key
#     （换 key 后旧备份全部不可解密；本脚本不负责换 key）；
#   - 不动 /data/wedding/backups、/data/wedding/import、nginx 与 cron；
#   - 不自动导入数据（导入是显式独立步骤，见 docs/06-m6-runbook.md §2.7）。
#
# 用法（在正式机器 /root/WeddingGift 仓库根执行）：
#   ops/reset-db.sh          # 交互确认
#   ops/reset-db.sh --yes    # 非交互确认（脚本内调用用）

set -eu

REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
COMPOSE_FILE="${REPO_ROOT}/deploy/compose.yaml"
PROJECT="wedding"
CONTAINER="wedding-app"
DATA_DIR="/data/wedding/data"
BACKUP_DIR="/data/wedding/backups"
DB_FILE="${DATA_DIR}/wedding.db"

YES=0
for a in "$@"; do
  case "${a}" in
    --yes) YES=1 ;;
    *) echo "未知参数: ${a}"; echo "用法: ops/reset-db.sh [--yes]"; exit 2 ;;
  esac
done

# 兼容 docker compose 插件与 standalone docker-compose。
if docker compose version >/dev/null 2>&1; then
  DC="docker compose"
else
  DC="docker-compose"
fi

echo "--- 安全确认 ---"
echo "本操作将清空 ${DB_FILE} 的全部业务数据（组/成员/登录名称/密钥/礼品/绑定/会话）。"
echo "清库前会自动备份当前库；cron 每小时 :44 的例行备份不受影响。"
if [ "${YES}" != "1" ]; then
  printf '确认清库请输入大写 YES：'
  read -r ANS || true
  [ "${ANS:-}" = "YES" ] || { echo "已取消。"; exit 1; }
fi

TS="$(date -u +%Y%m%d-%H%M%S)"

echo "--- 1/5 当前脱敏统计（留档核对） ---"
if [ -f "${DB_FILE}" ]; then
  $DC -p "${PROJECT}" -f "${COMPOSE_FILE}" run --rm -T app status \
    || echo "（status 读取失败，库可能已损坏，继续清库流程）"
else
  echo "（数据库文件不存在，将直接新建空库）"
fi

echo "--- 2/5 清库前备份当前库 ---"
if [ -f "${DB_FILE}" ]; then
  PRE="${BACKUP_DIR}/wedding-pre-reset-${TS}.db"
  if $DC -p "${PROJECT}" -f "${COMPOSE_FILE}" run --rm -T app backup create --output "${PRE}"; then
    echo "已备份：${PRE}"
  else
    Q="${DATA_DIR}/quarantine-${TS}"
    echo "备份失败（库可能已损坏），现有文件移入 ${Q} 保留，不销毁："
    mkdir -p "${Q}"
    for f in "${DB_FILE}" "${DB_FILE}-wal" "${DB_FILE}-shm"; do
      [ -f "${f}" ] && mv "${f}" "${Q}/"
    done
    ls -la "${Q}"
  fi
fi

echo "--- 3/5 停止 ${CONTAINER} ---"
if docker inspect "${CONTAINER}" >/dev/null 2>&1; then
  $DC -p "${PROJECT}" -f "${COMPOSE_FILE}" stop app
else
  echo "（容器不存在，跳过停止）"
fi

echo "--- 4/5 删除数据库文件并重建 schema ---"
rm -f "${DB_FILE}" "${DB_FILE}-wal" "${DB_FILE}-shm"
$DC -p "${PROJECT}" -f "${COMPOSE_FILE}" run --rm -T app migrate

echo "--- 5/5 启动并等待健康 ---"
$DC -p "${PROJECT}" -f "${COMPOSE_FILE}" up -d
i=0
until [ "$(docker inspect -f '{{.State.Health.Status}}' "${CONTAINER}" 2>/dev/null)" = "healthy" ]; do
  i=$((i + 1))
  if [ "$i" -gt 30 ]; then
    echo "健康检查超时，最近日志："
    docker logs --tail 50 "${CONTAINER}" || true
    exit 1
  fi
  sleep 2
done
echo "容器健康，清库完成：当前为空库 + 最新 schema，无任何业务数据。"

echo ""
echo "下一步（重新导入，详见 docs/06-m6-runbook.md §2.7）："
echo "  docker compose -p wedding -f deploy/compose.yaml run --rm -T app data validate --dir /data/wedding/import"
echo "  docker compose -p wedding -f deploy/compose.yaml run --rm -T app data import --dir /data/wedding/import"
echo "  docker compose -p wedding -f deploy/compose.yaml run --rm -T app status"
