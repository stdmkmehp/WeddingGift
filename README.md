# WeddingGift

婚礼回礼卡登录与礼品展示服务。来宾扫码进入 `https://stg.costra.cloud/wedding/`，输入本名或昵称 + 8 位卡片密钥，查看本同行组的礼品激活码。

设计与规则以 `docs/` 与 `staging-machine-brief.md` 为事实源。当前进度：**M0 已冻结，M1 最小工程与占位主页已完成**。

## 目录结构

```
cmd/wedding/        单二进制入口（serve / healthz-check / version）
internal/
  app/              配置、serve 生命周期、健康检查子命令
  buildinfo/        编译期注入的版本信息
  httpx/            HTTP 路由与中间件（安全头、请求 ID、访问日志）
web/                占位主页 + 静态资源（go:embed 进二进制）
deploy/             compose（共享机器 + 本地）与 nginx 片段
ops/                构建与冒烟脚本
testdata/fixtures/  虚构数据包（M0 数据契约样例，无真实数据）
docs/               需求 / 产品 / 技术 / 开发计划 / M0 决策
```

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

# 本地容器冒烟（映射 3000 端口，仅本地用）
ops/smoke-local.sh
```

共享机器部署使用 `deploy/compose.yaml`（external network `costra_default`、只 expose 不发布端口、256M/0.5CPU、json-file 日志）：

```sh
docker compose -p wedding -f deploy/compose.yaml up -d --build
```

## 安全与边界

- 明文卡片密钥不入库（HMAC 摘要），礼品码 AEAD 加密（后续里程碑实现）。
- 日志不记录姓名、密钥、礼品码、Cookie、请求正文。
- 真实数据、`app.key`、明文密钥/礼品码绝不进入 Git（见 `.gitignore`）。
- 共享机器红线以 `staging-machine-brief.md` 为准：不触碰 Costra 任何数据、配置与端口。
