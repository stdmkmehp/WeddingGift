#!/usr/bin/env sh
# WeddingGift WG-202：在共享机器部署最小主页容器
#
# 做的事：
#   1. 前置检查：docker 可用、costra_default 网络存在、不处于避峰窗口（仅提示）；
#   2. 创建 /data/wedding/ 自有目录树（目录收束约定；顶层/import 0755 root，data/backups/secrets 0700:65534，nginx 0700 root）；
#   3. 注入版本信息并用 deploy/compose.yaml 构建启动 wedding-app；
#   4. 等待容器健康，复核资源占用。
#
# 不做的事：不触碰 nginx、不创建/修改 cron、不导入任何数据（M2 占位阶段）。
#
# 用法（在 staging 机器 /root/WeddingGift 仓库根执行）：
#   ops/deploy-app.sh

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

echo "--- 前置检查 ---"
if ! docker network inspect costra_default >/dev/null 2>&1; then
  echo "错误：external network costra_default 不存在，请先完成只读自检并确认 Costra staging 栈在线。"
  exit 1
fi
echo "costra_default 网络存在"

# 避峰提醒（不强制阻断，由操作者确认；见 staging-machine-brief §4）。
MIN="$(date -u +%M)"
HOUR="$(date -u +%H)"
case "${HOUR}" in 00|06|12|18)
  if [ "${MIN}" -lt 10 ]; then
    echo "警告：当前接近整点从库 pg_dump 窗口（${HOUR}:00），建议稍后再执行。"
  fi
  ;;
esac

echo "--- 创建 WeddingGift 自有目录（/data/wedding） ---"
# 目录收束：运行时数据全部在 /data/wedding 下；secrets 后续放 app.key（只读挂载）。
# 权限模型（bind mount 保留宿主机属主与权限位，容器内进程以 65534 运行）：
#   - 顶层与 import/append 为 root:root 0755——65534 必须能穿越顶层、并穿越+读取
#     import/append 内的 CSV（文件由 root 放置，0644）；若为 root:root 0700，
#     65534 连顶层都进不去，validate/import/append/serve 全链路 EACCES（2026-09-12 staging 实测）；
#   - data/backups/secrets 为 65534:65534 0700——应用以非 root 写库、写备份、读 app.key；
#   - nginx 为 root:root 0700——不挂载进容器，仅 root 人工操作（改前备份与片段副本）。
mkdir -p /data/wedding/data /data/wedding/backups /data/wedding/secrets /data/wedding/import /data/wedding/append /data/wedding/nginx
chmod 755 /data/wedding /data/wedding/import /data/wedding/append
chmod 700 /data/wedding/data /data/wedding/backups /data/wedding/secrets /data/wedding/nginx
chown 65534:65534 /data/wedding/data /data/wedding/backups /data/wedding/secrets
ls -la /data/wedding

echo "--- 日报 SMTP 配置提示（可选，缺失不影响部署） ---"
if [ ! -f /data/wedding/secrets/smtp.env ]; then
  echo "提示：/data/wedding/secrets/smtp.env 不存在，每日统计日报与登录失败告警将不启用"
  echo "（服务照常运行）。需要时按 docs/06-m6-runbook.md §2.4 创建（0600、属主 65534）后重建容器。"
else
  echo "smtp.env 存在（应用运行时主动读取）"
fi

echo "--- 构建并启动 ${CONTAINER}（compose project: ${PROJECT}） ---"
# 注入版本信息（对应 compose.yaml 的 build.args），供占位页展示构建版本。
VERSION="$(git -C "${REPO_ROOT}" describe --tags --always --dirty 2>/dev/null || echo dev)"
COMMIT="$(git -C "${REPO_ROOT}" rev-parse --short HEAD 2>/dev/null || echo unknown)"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
export WEDDING_VERSION="${VERSION}" WEDDING_COMMIT="${COMMIT}" WEDDING_BUILD_TIME="${BUILD_TIME}"
echo "version=${VERSION} commit=${COMMIT} build_time=${BUILD_TIME}"

$DC -p "${PROJECT}" -f "${COMPOSE_FILE}" up -d --build

echo "--- 等待健康检查 ---"
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
echo "容器健康"

echo "--- 内网验证（从 costra-nginx 视角解析容器名） ---"
if docker exec costra-nginx wget -qO- "http://${CONTAINER}:3000/healthz" >/dev/null 2>&1; then
  echo "costra-nginx 可经 costra_default 访问 http://${CONTAINER}:3000/healthz"
else
  echo "警告：costra-nginx 暂无法访问应用（若 nginx 未 reload，容器名解析稍后生效属正常）"
fi

echo "--- 整机资源占用复核（峰值叠加时应用须低于 256M/0.5CPU） ---"
docker stats --no-stream

echo ""
echo "WG-202 完成。下一步（WG-203，人工操作）：按 docs/05-m2-runbook.md 第 4 节插入 /wedding/ location 并热加载，片段见 deploy/nginx-wedding.conf。"
