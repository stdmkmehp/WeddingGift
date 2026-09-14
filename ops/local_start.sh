#!/usr/bin/env sh
# WeddingGift：本地预览服务（开发机看页面用，不用于正式环境）
#
# 做的事：
#   1. 在临时目录（默认 ${TMPDIR:-/tmp}/wedding-preview）准备一份与正式环境
#      完全隔离的 app.key 与 SQLite 库，绝不触碰 /data/wedding 与任何容器；
#   2. go run ./cmd/wedding migrate 建 schema（幂等，重复执行为 no-op）；
#   3. 本地库不存在时自动导入 testdata/fixtures 虚构数据包，便于直接登录查看
#      礼品页；已存在则跳过，保留此前的登录与密钥绑定状态；
#   4. 前台启动 go run ./cmd/wedding serve，日志直接打在终端，Ctrl-C 即停。
#
# 不做的事：
#   - 不使用 Docker / compose：正式镜像只能开发机构建后 scp 上机
#     （见 README「依赖与镜像发布」），本地一律 go run；
#   - 不停起正式容器，不读写正式数据库、备份与 nginx；
#   - 不导入真实数据：真实数据按 docs/06-m6-runbook.md §2.7 上机导入。
#
# 用法（在仓库根执行）：
#   ops/local_start.sh                 # 默认 http://127.0.0.1:3000/
#   ops/local_start.sh --port 3211     # 指定端口
#   ops/local_start.sh --reset         # 删除本地临时库重建，并重新导入 fixtures
#   ops/local_start.sh --no-import     # 只建 schema 不导数据（仅看登录页）
#   ops/local_start.sh --help
#
# 登录样例（testdata/fixtures，纯虚构）：
#   张伟 + 20260002（G002，3 份礼品）/ 王芳 + 20260001 / 陈强 + 20260003
#
# 注意：web/ 经 go:embed 编译进二进制，改 HTML/CSS/JS 后必须 Ctrl-C 重跑本脚本。

set -eu

REPO_ROOT="$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)"
cd "${REPO_ROOT}"

LOCAL_DIR="${WEDDING_LOCAL_DIR:-${TMPDIR:-/tmp}/wedding-preview}"
DB_PATH="${WEDDING_DB_PATH:-${LOCAL_DIR}/local.db}"
KEY_PATH="${WEDDING_APP_KEY_PATH:-${LOCAL_DIR}/app.key}"
LISTEN_ADDR="${WEDDING_LISTEN_ADDR:-:3000}"
FIXTURES_DIR="${REPO_ROOT}/testdata/fixtures"

RESET=0
IMPORT=1

usage() {
  sed -n '2,32p' "$0" | sed 's/^# \{0,1\}//'
}

while [ $# -gt 0 ]; do
  case "$1" in
    --port)
      [ $# -ge 2 ] || { echo "错误：--port 缺少参数"; exit 2; }
      LISTEN_ADDR=":${2}"
      shift 2
      ;;
    --reset)
      RESET=1
      shift
      ;;
    --no-import)
      IMPORT=0
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      echo "未知参数：$1"
      usage
      exit 2
      ;;
  esac
done

# 展示用端口：LISTEN_ADDR 形如 ":3000" / "127.0.0.1:3211" / "3000"。
case "${LISTEN_ADDR}" in
  *:*) PORT="${LISTEN_ADDR##*:}" ;;
  *)   PORT="${LISTEN_ADDR}" ;;
esac

# 全部子命令共享同一份本地配置。
export WEDDING_DB_PATH="${DB_PATH}"
export WEDDING_APP_KEY_PATH="${KEY_PATH}"
export WEDDING_LISTEN_ADDR="${LISTEN_ADDR}"

echo "--- 前置检查 ---"
command -v go >/dev/null 2>&1 || { echo "错误：未找到 go，请先安装 Go 1.27+。"; exit 1; }
command -v openssl >/dev/null 2>&1 || {
  echo "错误：未找到 openssl，无法生成本地 app.key。"
  echo "可自行生成后重跑：openssl rand -hex 32 > ${KEY_PATH}"
  exit 1
}
if command -v lsof >/dev/null 2>&1 && lsof -nP -iTCP:"${PORT}" -sTCP:LISTEN >/dev/null 2>&1; then
  echo "错误：端口 ${PORT} 已被占用（先停止占用方，或用 --port 换个端口）。"
  exit 1
fi
echo "go: $(go version | awk '{print $3}')，监听 ${LISTEN_ADDR}"

echo "--- 1/3 准备本地隔离目录与 app.key ---"
mkdir -p "${LOCAL_DIR}"
chmod 700 "${LOCAL_DIR}"
if [ "${RESET}" = 1 ]; then
  rm -f "${DB_PATH}" "${DB_PATH}-wal" "${DB_PATH}-shm"
  echo "已删除本地临时库（--reset）。"
fi
if [ ! -f "${KEY_PATH}" ]; then
  openssl rand -hex 32 > "${KEY_PATH}"
  echo "已生成 app.key：${KEY_PATH}"
else
  echo "复用已有 app.key：${KEY_PATH}"
fi
chmod 600 "${KEY_PATH}"

echo "--- 2/3 建 schema（幂等） ---"
DB_EXISTS=0
[ -f "${DB_PATH}" ] && DB_EXISTS=1
go run ./cmd/wedding migrate

echo "--- 3/3 导入虚构数据（testdata/fixtures） ---"
if [ "${IMPORT}" = 0 ]; then
  echo "按 --no-import 跳过导入，仅可查看登录页。"
elif [ "${DB_EXISTS}" = 1 ]; then
  echo "本地库已存在，跳过导入以免覆盖既有登录/绑定状态；如需重导：ops/local_start.sh --reset"
else
  go run ./cmd/wedding data import --dir "${FIXTURES_DIR}"
fi

echo ""
echo "--- 启动本地预览服务 ---"
echo "访问：http://127.0.0.1:${PORT}/"
echo "登录样例：张伟 + 20260002 / 王芳 + 20260001 / 陈强 + 20260003"
echo "停止：Ctrl-C（或修改 web/ 后重跑本脚本，go:embed 需重启生效）"
echo ""

exec go run ./cmd/wedding serve
