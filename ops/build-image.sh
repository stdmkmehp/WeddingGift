#!/usr/bin/env sh
# WeddingGift 镜像构建脚本
#
# 用途：构建注入版本信息的 wedding-app 镜像。
# 在有 Docker 的环境（如 staging 或装了 Docker 的开发机）执行。
#
# 用法：
#   ops/build-image.sh [TAG]
# 例：
#   ops/build-image.sh wedding-app:0.1.0

set -eu

TAG="${1:-wedding-app:latest}"

# 版本信息：优先用 git 描述，失败则回退默认值。
VERSION="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
COMMIT="$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"

echo "Building ${TAG}"
echo "  version=${VERSION} commit=${COMMIT} build_time=${BUILD_TIME}"

docker build \
  --build-arg "VERSION=${VERSION}" \
  --build-arg "COMMIT=${COMMIT}" \
  --build-arg "BUILD_TIME=${BUILD_TIME}" \
  -t "${TAG}" \
  "${REPO_ROOT}"

echo "Done: ${TAG}"
