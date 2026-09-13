#!/usr/bin/env sh
# WeddingGift：正式环境加载镜像（解压 ops/build-image.sh 产出的压缩包并 docker load）
#
# 背景：正式环境构建机无法直达 Go 模块源与镜像源，镜像一律由开发机
# ops/build-image.sh 构建打包后传输到本机，本脚本解压并 load 到本地镜像库。
# load 覆盖 wedding-app:latest tag，后续起服/重建容器即使用新镜像。
#
# 做的事：
#   1. 选择压缩包：参数指定路径；缺省取 ${REPO_ROOT}/dist 下最新的
#      wedding-app-*.tar.gz；
#   2. 体检：存在、非空、gzip 魔数（1f 8b）、gzip -t 完整性；
#   3. 流式解压 docker load（不落中间 tar 文件）；
#   4. 核对 load 后镜像 wedding-app:latest 存在，提示下一步。
#
# 不做的事：不启动/重建容器、不做 schema 迁移、不清理旧镜像
#   （load 覆盖 tag 后旧镜像层变 dangling，可 `docker image prune` 人工清理）。
#
# 用法（在正式机器 /root/WeddingGift 仓库根执行）：
#   ops/load-image.sh                                           # 加载 dist 下最新压缩包
#   ops/load-image.sh /root/WeddingGift/dist/wedding-app-xxx.tar.gz

set -eu

REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
DIST_DIR="${REPO_ROOT}/dist"
IMAGE="wedding-app:latest"

SRC=""
if [ $# -gt 1 ]; then
  echo "用法: ops/load-image.sh [<tar.gz 文件>]"
  exit 2
fi
if [ $# -eq 1 ]; then
  SRC="$1"
else
  SRC="$(ls -1t "${DIST_DIR}"/wedding-app-*.tar.gz 2>/dev/null | head -n 1 || true)"
  [ -n "${SRC}" ] || { echo "错误：${DIST_DIR} 下没有可用压缩包（wedding-app-*.tar.gz）。"; exit 1; }
fi

echo "--- 压缩包体检 ---"
[ -f "${SRC}" ] || { echo "错误：文件不存在：${SRC}"; exit 1; }
[ -s "${SRC}" ] || { echo "错误：文件为空：${SRC}"; exit 1; }
MAGIC="$(head -c 2 "${SRC}" | od -An -tx1 | tr -d ' \n')"
[ "${MAGIC}" = "1f8b" ] || { echo "错误：${SRC} 不是 gzip 压缩文件（魔数 ${MAGIC}）"; exit 1; }
gzip -t "${SRC}" || { echo "错误：压缩包完整性校验失败：${SRC}"; exit 1; }
ls -lh "${SRC}"

echo "--- 解压并 docker load ---"
gzip -dc "${SRC}" | docker load

echo "--- 核对镜像 ---"
docker image inspect "${IMAGE}" >/dev/null 2>&1 || {
  echo "错误：load 后仍未找到镜像 ${IMAGE}。"
  exit 1
}
docker images "${IMAGE}"

echo ""
echo "完成。下一步（详见 docs/06-m6-runbook.md §5 发布序列）："
echo "  ops/start-app.sh    # 仅起服；发布/回滚请按 runbook 停服 → migrate → 起服"
