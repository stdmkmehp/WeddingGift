#!/usr/bin/env sh
# WeddingGift：开发机构建镜像并打包压缩（供正式环境离线 load）
#
# 背景：正式环境构建机无法直达 Go 模块源与镜像源，Dockerfile 构建阶段需要
# 拉取 golang:1.27-alpine 并 go mod download，因此镜像一律在开发机（网络可达）
# 构建、docker save 后 gzip 压缩，传输到正式环境后由 ops/load-image.sh
# 解压 load。本脚本把「构建 + 打包」固化为可重复操作。
#
# 做的事：
#   1. 前置检查：docker 可用；
#   2. 注入 git 版本信息（version/commit/build_time，与 ops/deploy-app.sh 一致，
#      对应 compose.yaml 的 build.args）并 docker compose build；
#   3. docker save wedding-app:latest 并 gzip 压缩到
#      dist/wedding-app-<version>-<commit>-<UTC时间戳>.tar.gz；
#   4. 打印 SHA-256 与传输/加载的下一步命令。
#
# 不做的事：不 push registry；不在开发机运行容器；不自动清理旧压缩包
#   （dist/ 已 gitignore，旧包按需人工删除）。
#
# 用法（在开发机仓库根执行，构建需开发机网络）：
#   ops/build-image.sh

set -eu

REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
COMPOSE_FILE="${REPO_ROOT}/deploy/compose.yaml"
PROJECT="wedding"
IMAGE="wedding-app:latest"
DIST_DIR="${REPO_ROOT}/dist"

# 兼容 docker compose 插件与 standalone docker-compose。
if docker compose version >/dev/null 2>&1; then
  DC="docker compose"
else
  DC="docker-compose"
fi

echo "--- 前置检查 ---"
if ! docker info >/dev/null 2>&1; then
  echo "错误：docker 不可用（daemon 未运行或权限不足）。"
  exit 1
fi

# 注入版本信息（对应 compose.yaml 的 build.args；与 ops/deploy-app.sh 一致）。
VERSION="$(git -C "${REPO_ROOT}" describe --tags --always --dirty 2>/dev/null || echo dev)"
COMMIT="$(git -C "${REPO_ROOT}" rev-parse --short HEAD 2>/dev/null || echo unknown)"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
export WEDDING_VERSION="${VERSION}" WEDDING_COMMIT="${COMMIT}" WEDDING_BUILD_TIME="${BUILD_TIME}"
echo "version=${VERSION} commit=${COMMIT} build_time=${BUILD_TIME}"

echo "--- 构建镜像（Dockerfile 内 go mod download，需要开发机网络） ---"
$DC -p "${PROJECT}" -f "${COMPOSE_FILE}" build app

echo "--- docker save 并压缩 ---"
mkdir -p "${DIST_DIR}"
TS="$(date -u +%Y%m%d-%H%M%S)"
OUT="${DIST_DIR}/wedding-app-${VERSION}-${COMMIT}-${TS}.tar.gz"
docker save "${IMAGE}" | gzip > "${OUT}"
ls -lh "${OUT}"
if command -v sha256sum >/dev/null 2>&1; then
  sha256sum "${OUT}"
else
  shasum -a 256 "${OUT}"
fi

echo ""
echo "完成。下一步（传输到正式机器并加载）："
echo "  scp ${OUT} root@<正式机器>:/root/WeddingGift/dist/"
echo "  ssh root@<正式机器> 'cd /root/WeddingGift && ops/load-image.sh'"
