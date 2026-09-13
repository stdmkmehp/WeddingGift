#!/usr/bin/env sh
# WeddingGift：停服脚本（停止 wedding-app 容器，保留容器与数据）
#
# 做的事：容器在运行时 docker compose stop app（优雅停止，stop_grace_period
#   15s）并等待容器退出；已停止或不存在则幂等跳过。
#
# 不做的事：不删除容器、不动网络、不碰数据库与 nginx。
#
# 用法（在正式机器 /root/WeddingGift 仓库根执行）：
#   ops/stop-app.sh

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

if ! docker inspect "${CONTAINER}" >/dev/null 2>&1; then
  echo "容器 ${CONTAINER} 不存在，无需停服。"
  exit 0
fi

RUNNING="$(docker inspect -f '{{.State.Running}}' "${CONTAINER}")"
if [ "${RUNNING}" != "true" ]; then
  echo "容器 ${CONTAINER} 已处于停止状态，无需操作。"
  exit 0
fi

echo "--- 停止 ${CONTAINER} ---"
$DC -p "${PROJECT}" -f "${COMPOSE_FILE}" stop app

i=0
until [ "$(docker inspect -f '{{.State.Running}}' "${CONTAINER}" 2>/dev/null)" != "true" ]; do
  i=$((i + 1))
  if [ "$i" -gt 30 ]; then
    echo "错误：容器未能按时退出，最近日志："
    docker logs --tail 50 "${CONTAINER}" || true
    exit 1
  fi
  sleep 1
done
echo "容器已停止。"
