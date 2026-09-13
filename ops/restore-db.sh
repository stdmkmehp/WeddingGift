#!/usr/bin/env sh
# WeddingGift：从本机备份还原数据库（备份由 ops/backup.sh / cron 每小时 :44 产出）
#
# 做的事（仅操作数据库，不停起服务）：
#   1. 前置检查：wedding-app 必须已停止（先 ops/stop-app.sh）；仍在运行则
#      报错退出——容器持有 SQLite 连接时替换数据库会留下悬空状态；
#   2. 选择备份：参数指定文件（绝对路径或 backups 目录下文件名），缺省取
#      /data/wedding/backups 下最新的 wedding-*.db；体检：存在、非空、
#      SQLite 文件头魔数正确；
#   3. 安全确认（--yes 或交互输入大写 YES）；
#   4. 还原前兜底备份当前库（backups/wedding-pre-restore-<UTC时间戳>.db；
#      备份失败如库已损坏，则把现有 wedding.db* 移入 data/quarantine-<TS>/
#      保留，不销毁任何文件）；
#   5. 替换 wedding.db（清理 -wal/-shm，属主 65534、0600）→ migrate
#      （无新 migration 为 no-op，可把旧 schema 备份前滚到当前版本）；
#   6. 任一环节失败自动回滚：恢复还原前数据库文件状态，退出码非零；
#   7. 成功后输出脱敏统计（status），供与预期数据人工核对。
#
# 不做的事：
#   - 不停服、不起服：停/起由 ops/stop-app.sh 与 ops/start-app.sh 负责，
#     完成后自行 ops/start-app.sh 起服；
#   - 不判断"备份里是不是你想要的那批数据"（用末尾 status 计数人工核对）；
#   - 不动 app.key：备份中的密钥摘要/礼品密文只能用生成备份时的同一把 key
#     验证与解密（本机 app.key 未更换则天然满足）；
#   - 不动 cron 与 nginx；不删除任何备份文件（含本次产出的 pre-restore 备份）；
#   - 不清空会话表：还原后旧会话在 30 天 TTL 内仍有效；如需强制全部重新登录，
#     按 docs/02-technical-architecture.md §17.3 人工清空 SESSION 表。
#
# 用法（在正式机器 /root/WeddingGift 仓库根执行）：
#   ops/stop-app.sh                                        # 先停服
#   ops/restore-db.sh                                      # 还原最新备份（交互确认）
#   ops/restore-db.sh wedding-20260912-034403.db           # 指定 backups 目录下文件名
#   ops/restore-db.sh /data/wedding/backups/wedding-pre-release-20260912-033000.db --yes
#   ops/start-app.sh                                       # 完成后起服

set -eu

REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
COMPOSE_FILE="${REPO_ROOT}/deploy/compose.yaml"
PROJECT="wedding"
CONTAINER="wedding-app"
DATA_DIR="/data/wedding/data"
BACKUP_DIR="/data/wedding/backups"
DB_FILE="${DATA_DIR}/wedding.db"

SRC=""
YES=0
while [ $# -gt 0 ]; do
  case "$1" in
    --yes) YES=1 ;;
    -*) echo "未知参数: $1"; exit 2 ;;
    *) SRC="$1" ;;
  esac
  shift
done

# 兼容 docker compose 插件与 standalone docker-compose。
if docker compose version >/dev/null 2>&1; then
  DC="docker compose"
else
  DC="docker-compose"
fi

# 只给文件名时补全到 backups 目录。
if [ -n "${SRC}" ] && [ ! -f "${SRC}" ] && [ -f "${BACKUP_DIR}/${SRC}" ]; then
  SRC="${BACKUP_DIR}/${SRC}"
fi

# 缺省取最新备份。
if [ -z "${SRC}" ]; then
  SRC="$(ls -1t "${BACKUP_DIR}"/wedding-*.db 2>/dev/null | head -n 1 || true)"
  [ -n "${SRC}" ] || { echo "错误：${BACKUP_DIR} 下没有可用备份（wedding-*.db）。"; exit 1; }
fi

echo "--- 备份文件体检 ---"
[ -f "${SRC}" ] || { echo "错误：备份文件不存在：${SRC}"; exit 1; }
[ -s "${SRC}" ] || { echo "错误：备份文件为空：${SRC}"; exit 1; }
[ "$(head -c 15 "${SRC}")" = "SQLite format 3" ] || { echo "错误：${SRC} 不是 SQLite 数据库文件"; exit 1; }
ls -l "${SRC}"

echo "--- 前置检查：服务必须已停止 ---"
if [ "$(docker inspect -f '{{.State.Running}}' "${CONTAINER}" 2>/dev/null)" = "true" ]; then
  echo "错误：${CONTAINER} 仍在运行。本脚本仅操作数据库，请先停服："
  echo "  ops/stop-app.sh"
  exit 1
fi
echo "容器已停止（或不存在），继续。"

echo "--- 安全确认 ---"
echo "本操作将用上述备份整体替换当前数据库 ${DB_FILE}（绑定/会话一并回到备份时点）。"
echo "替换前会自动兜底备份当前库；还原失败会自动回滚。"
if [ "${YES}" != "1" ]; then
  printf '确认还原请输入大写 YES：'
  read -r ANS || true
  [ "${ANS:-}" = "YES" ] || { echo "已取消。"; exit 1; }
fi

TS="$(date -u +%Y%m%d-%H%M%S)"
PRE=""
Q=""

echo "--- 1/4 兜底备份当前库 ---"
if [ -f "${DB_FILE}" ]; then
  PRE="${BACKUP_DIR}/wedding-pre-restore-${TS}.db"
  if $DC -p "${PROJECT}" -f "${COMPOSE_FILE}" run --rm -T app backup create --output "${PRE}"; then
    echo "当前库已备份：${PRE}"
  else
    PRE=""
    Q="${DATA_DIR}/quarantine-${TS}"
    echo "当前库备份失败（可能已损坏），移入 ${Q} 保留，不销毁："
    mkdir -p "${Q}"
    for f in "${DB_FILE}" "${DB_FILE}-wal" "${DB_FILE}-shm"; do
      [ -f "${f}" ] && mv "${f}" "${Q}/"
    done
    ls -la "${Q}"
  fi
else
  echo "（当前无数据库文件，跳过兜底备份）"
fi

# 回滚：恢复还原前数据库文件状态（兜底备份优先，其次 quarantine 原始文件，
# 最后重建空 schema）；不起服，起服由操作者确认后显式执行。
rollback() {
  echo "--- 自动回滚：恢复还原前数据库 ---"
  rm -f "${DB_FILE}" "${DB_FILE}-wal" "${DB_FILE}-shm"
  if [ -n "${PRE}" ] && [ -f "${PRE}" ]; then
    cp -p "${PRE}" "${DB_FILE}"
    chown 65534:65534 "${DB_FILE}"
    chmod 600 "${DB_FILE}"
  elif [ -n "${Q}" ] && [ -d "${Q}" ]; then
    mv "${Q}"/* "${DATA_DIR}/" 2>/dev/null || true
    rmdir "${Q}" 2>/dev/null || true
  else
    $DC -p "${PROJECT}" -f "${COMPOSE_FILE}" run --rm -T app migrate || true
  fi
  echo "已恢复还原前数据库文件状态（服务仍处于停止状态），请人工核查后 ops/start-app.sh 起服。"
}

echo "--- 2/4 替换数据库 ---"
rm -f "${DB_FILE}" "${DB_FILE}-wal" "${DB_FILE}-shm"
cp -p "${SRC}" "${DB_FILE}"
chown 65534:65534 "${DB_FILE}"
chmod 600 "${DB_FILE}"

echo "--- 3/4 migrate（旧 schema 备份前滚；无新 migration 为 no-op） ---"
if ! $DC -p "${PROJECT}" -f "${COMPOSE_FILE}" run --rm -T app migrate; then
  echo "migrate 失败，备份文件可能损坏或与当前版本不兼容。"
  rollback
  exit 1
fi

echo "--- 4/4 输出脱敏统计（请与预期数据逐项核对） ---"
if ! $DC -p "${PROJECT}" -f "${COMPOSE_FILE}" run --rm -T app status; then
  echo "status 读取失败，还原结果可能异常。"
  rollback
  exit 1
fi

echo ""
echo "完成。服务仍处于停止状态，下一步："
echo "  ops/start-app.sh            # 起服并等待健康"
echo "  ops/verify-public.sh        # 真实 URL 全量验证"
echo "  ops/canary-smoke.sh         # canary 链路冒烟（幂等）"
[ -z "${PRE}" ] || echo "兜底备份保留于：${PRE}（确认还原无误后可人工删除）"
