#!/usr/bin/env sh
# WeddingGift WG-201：共享机器只读自检
#
# 严格对应 docs/staging-machine-brief.md §8，全部命令只读，不修改任何文件或配置。
# 结果同时输出到终端与 ops/records/selfcheck-<UTC时间戳>.log（records 已 gitignore）。
#
# 用法（在 staging 机器、仓库根目录外任意位置执行均可）：
#   ops/staging-selfcheck.sh
#
# 完成标准（G2 前置）：资源余量满足 256M/0.5CPU，costra_default 网络存在，
# nginx live conf 中尚无 wedding 块，/etc/cron.d/wedding 尚不存在。

set -eu

REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
RECORDS_DIR="${REPO_ROOT}/ops/records"
STAMP="$(date -u +%Y%m%d-%H%M%S)"
LOG_FILE="${RECORDS_DIR}/selfcheck-${STAMP}.log"

mkdir -p "${RECORDS_DIR}"

section() {
  echo ""
  echo "================================================================"
  echo "== $1"
  echo "================================================================"
}

{
  section "自检时间（UTC）"
  date -u +%Y-%m-%dT%H:%M:%SZ
  hostname

  section "机器规格与余量：nproc / free -h / df -h /data"
  nproc
  free -h
  df -h /data

  section "三栈实时占用（含 limits 对比）：docker stats --no-stream"
  docker stats --no-stream

  section "容器清单：docker ps -a"
  docker ps -a

  section "端口占用全景（确认与 22/80/443/5432/6379/15432/15433 无冲突计划）"
  ss -tlnp

  section "Docker 网络（确认 costra_default 存在；未占用 VPC 172.17 段）"
  docker network ls -q | xargs -r docker network inspect \
    -f '{{.Name}} {{range .IPAM.Config}}{{.Subnet}} {{end}}'

  section "现有 Costra 定时任务（只读，勿改）"
  cat /etc/cron.d/costra-standby /etc/cron.d/costra-analyze

  section "WeddingGift cron 单元现状（首次上机应不存在）"
  ls -l /etc/cron.d/wedding 2>/dev/null || echo "no wedding cron yet（符合预期）"

  section "nginx live conf 的 location 现状（应无 wedding 块）"
  grep -n 'location' /data/conf/nginx/costra.cloud.conf
  echo ""
  echo "wedding 块出现次数：$(grep -c 'wedding' /data/conf/nginx/costra.cloud.conf || true)（应为 0）"

  section "兜底 location 锚点检查（插入脚本依赖，期望恰好 1 处）"
  grep -n 'location / {[[:space:]]*return 404' /data/conf/nginx/costra.cloud.conf || echo "未找到，需人工核对 conf 结构"

  section "nginx conf.d 目录清单（只读）"
  ls -l /data/conf/nginx/

  section "WeddingGift 自有目录现状（首次上机应不存在）"
  ls -ld /root/WeddingGift /data/wedding 2>/dev/null || echo "/root/WeddingGift 或 /data/wedding 尚未创建"
} 2>&1 | tee "${LOG_FILE}"

echo ""
echo "自检记录已保存：${LOG_FILE}"
echo "请对照 docs/05-m2-runbook.md 第 2 节核对结果后再进入部署步骤。"
