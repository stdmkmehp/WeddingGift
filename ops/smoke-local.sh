#!/usr/bin/env sh
# WeddingGift 本地容器冒烟脚本
#
# 用途：在有 Docker 的环境用 deploy/compose.local.yaml 起容器，
# 验证镜像可构建、健康检查通过、占位主页与 /healthz 可访问。
# 不依赖 costra_default 网络与 /data/wedding 真实目录。
#
# 用法：ops/smoke-local.sh
#
# 注意：仅本地验证使用；该 compose 会映射本机 3000 端口，生产环境严禁发布端口。

set -eu

REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
COMPOSE_FILE="${REPO_ROOT}/deploy/compose.local.yaml"
PROJECT="wedding-local"
BASE_URL="http://127.0.0.1:3000"

# 兼容 docker compose 插件与 standalone docker-compose。
if docker compose version >/dev/null 2>&1; then
  DC="docker compose"
else
  DC="docker-compose"
fi

cleanup() {
  echo "--- 清理 ---"
  $DC -p "$PROJECT" -f "$COMPOSE_FILE" down -v >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "--- 构建并启动 ---"
$DC -p "$PROJECT" -f "$COMPOSE_FILE" up -d --build

echo "--- 等待健康检查 ---"
i=0
until [ "$(docker inspect -f '{{.State.Health.Status}}' wedding-app-local 2>/dev/null)" = "healthy" ]; do
  i=$((i + 1))
  if [ "$i" -gt 30 ]; then
    echo "健康检查超时"
    docker logs wedding-app-local || true
    exit 1
  fi
  sleep 2
done
echo "容器健康"

echo "--- 访问验证 ---"
echo "GET /        : $(curl -s -o /dev/null -w '%{http_code}' "${BASE_URL}/")"
echo "GET /assets/app.css : $(curl -s -o /dev/null -w '%{http_code}' "${BASE_URL}/assets/app.css")"
echo "GET /healthz : $(curl -s "${BASE_URL}/healthz")"
echo "GET /nope    : $(curl -s -o /dev/null -w '%{http_code}' "${BASE_URL}/nope")"

echo "--- 资源占用 ---"
docker stats --no-stream wedding-app-local || true

echo "--- 冒烟完成 ---"
