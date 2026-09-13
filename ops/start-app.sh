#!/usr/bin/env sh
# WeddingGift：起服脚本（启动 wedding-app 容器并等待健康）
#
# 做的事：
#   1. 容器已运行则幂等跳过；
#   2. 容器存在（停止状态）则 docker compose up -d 启动（镜像有更新时
#      自动重建为最新镜像）；
#   3. 容器不存在时先核对本地已有镜像 wedding-app:latest，缺失则报错引导
#      ops/load-image.sh——避免在正式机触发 compose build（正式机无法拉取
#      Go 模块与基础镜像，构建必失败）；
#   4. 等待健康检查通过（healthy 前 serve 不对外响应）。
#
# 不做的事：不做 schema 迁移（migrate 属发布/清库流程的显式步骤）、不碰
#   nginx、不碰数据库文件。
#
# 用法（在正式机器 /root/WeddingGift 仓库根执行）：
#   ops/start-app.sh

set -eu

REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
COMPOSE_FILE="${REPO_ROOT}/deploy/compose.yaml"
PROJECT="wedding"
CONTAINER="wedding-app"
IMAGE="wedding-app:latest"

# 兼容 docker compose 插件与 standalone docker-compose。
if docker compose version >/dev/null 2>&1; then
  DC="docker compose"
else
  DC="docker-compose"
fi

if docker inspect "${CONTAINER}" >/dev/null 2>&1; then
  RUNNING="$(docker inspect -f '{{.State.Running}}' "${CONTAINER}")"
  if [ "${RUNNING}" = "true" ]; then
    echo "容器 ${CONTAINER} 已在运行，无需操作。"
    exit 0
  fi
else
  if ! docker image inspect "${IMAGE}" >/dev/null 2>&1; then
    echo "错误：容器不存在且本地无镜像 ${IMAGE}。"
    echo "正式机无法离线构建，请先在开发机执行 ops/build-image.sh 并传输压缩包到本机，"
    echo "再执行 ops/load-image.sh 加载镜像。"
    exit 1
  fi
fi

echo "--- 启动 ${CONTAINER} ---"
$DC -p "${PROJECT}" -f "${COMPOSE_FILE}" up -d

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
echo "容器健康。"
