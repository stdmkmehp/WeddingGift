# WeddingGift

婚礼回礼卡登录与礼品展示服务。来宾扫码进入 `https://stg.costra.cloud/wedding/`，输入本名或昵称 + 8 位卡片密钥，查看本同行组的礼品激活码。

设计与规则以 `docs/` 与 `docs/staging-machine-brief.md` 为事实源。当前进度：**M0 已冻结，M1 最小工程与占位主页、M2 真实 URL 链路均已在 staging 验收通过（G2 门禁通过，见 `docs/05-m2-runbook.md`），M3 数据、加密与管理能力已完成本地实现与测试（G3 门禁项全部覆盖），下一步 M4 鉴权与会话**。

## 目录结构

```
cmd/wedding/        单二进制入口（serve / migrate / data / status / key / backup / healthz-check / version）
internal/
  app/              配置、serve 生命周期、健康检查子命令
  admin/            数据文件装载、脱敏核对报告、状态文本
  buildinfo/        编译期注入的版本信息
  cryptox/          app.key 加载、HKDF 派生、密钥 HMAC、礼品码 AEAD、会话令牌
  domain/           领域实体、登录名称规范化、数据契约校验（不依赖 HTTP/SQLite）
  httpx/            HTTP 路由与中间件（安全头、请求 ID、访问日志）
  store/            SQLite 连接、migration、repository、online backup
migrations/         SQL migration（go:embed 进二进制）
web/                占位主页 + 静态资源（go:embed 进二进制）
deploy/             共享机器 compose、nginx 插入片段、cron 单元样例
ops/                构建、自检、部署、验证与本机备份脚本
testdata/fixtures/  虚构数据包（M0 数据契约样例，无真实数据）
docs/               需求 / 产品 / 技术 / 开发计划 / M0 决策 / M2 手册
```

## M3 数据与管理能力

数据库为单实例 SQLite（纯 Go 驱动 modernc.org/sqlite，CGO_ENABLED=0 可构建进 scratch 镜像）；明文密钥只存 HMAC 摘要、礼品码只存 AES-256-GCM 密文，两者由同一个 `app.key` 经 HKDF 派生（`app.key` 丢失则数据不可恢复，须离线异位保管）。

```sh
# 准备 app.key（二选一；部署时放 /data/wedding/secrets/app.key，0600、属主 65534）
openssl rand -hex 32 > app.key        # hex 文本形态
head -c 32 /dev/urandom > app.key     # 原始字节形态

# 本地开发时用环境变量指向本地路径
export WEDDING_DB_PATH=./local.db WEDDING_APP_KEY_PATH=./app.key

# 初始化/升级 schema（发布时显式执行）
go run ./cmd/wedding migrate

# 数据包只读校验（不触碰数据库；报告字段缺失、登录名称冲突、非法密钥等全部问题）
go run ./cmd/wedding data validate --dir testdata/fixtures

# 校验通过后单事务导入（要求空库；任一错误全部回滚；不生成密钥）
go run ./cmd/wedding data import --dir testdata/fixtures

# 脱敏状态（内部编号与数量，无明文）
go run ./cmd/wedding status

# 停用密钥并删除其会话（不自动补发、不改绑）
go run ./cmd/wedding key revoke --ref K001

# SQLite online backup 一致性备份（含完整性检查，目标存在则拒绝覆盖）
go run ./cmd/wedding backup create --output /path/to/wedding-backup.db
```

本机 cron 备份（staging）：`install -m 0644 -o root -g root deploy/wedding.cron /etc/cron.d/wedding`，每小时 `:44` 由 `ops/backup.sh` 通过容器内二进制产出备份并保留最近 72 份；不修改 Costra 的 cron 文件。

## 本地开发

需要 Go 1.27+。

```sh
# 运行测试
go test ./...

# 静态检查与格式
go vet ./...
gofmt -l .

# 本地运行占位主页
go run ./cmd/wedding serve
# 默认监听 :3000，可用 WEDDING_LISTEN_ADDR 覆盖
# 访问 http://127.0.0.1:3000/

# 健康检查（另开终端）
go run ./cmd/wedding healthz-check   # 退出码 0 表示健康

# 构建二进制
CGO_ENABLED=0 go build -o bin/wedding ./cmd/wedding
```

## 容器（需 Docker，通常在 staging 验证）

镜像为多阶段构建 + `scratch` 基底，非 root、只读 rootfs，健康检查用应用自带 `wedding healthz-check`（不依赖镜像内 curl/wget）。

```sh
# 构建镜像（注入版本信息）
ops/build-image.sh wedding-app:0.1.0
```

共享机器部署使用 `deploy/compose.yaml`（external network `costra_default`、只 expose 不发布端口、256M/0.5CPU、json-file 日志），由 `ops/deploy-app.sh` 驱动。

## M2 真实 URL 链路（staging 执行）

在 staging 机器 `/opt/wedding/` 仓库根按顺序执行（详见 `docs/05-m2-runbook.md`）：

```sh
ops/staging-selfcheck.sh   # WG-201 只读自检（staging-machine-brief §8）
ops/deploy-app.sh          # WG-202 创建 /data/wedding 目录并启动 wedding-app
# WG-203：人工按 runbook §4 插入 /wedding/ location（片段：deploy/nginx-wedding.conf）并热加载
ops/verify-public.sh       # 真实 URL + Costra 回归验证（任意可访问公网机器可执行）
```

注意：nginx 修改的对象是 Costra 项目的 live 配置，一律**人工按 runbook 操作**，不使用脚本；回滚同样人工按 runbook §6 执行。

## 安全与边界

- 明文卡片密钥不入库（HMAC 摘要），礼品码 AEAD 加密，数据库与备份中扫描不到明文。
- 日志不记录姓名、密钥、礼品码、Cookie、请求正文。
- 真实数据、`app.key`、明文密钥/礼品码绝不进入 Git（见 `.gitignore`）。
- 共享机器红线以 `docs/staging-machine-brief.md` 为准：不触碰 Costra 任何数据、配置与端口。
