# WeddingGift 项目长期约定

- 业务身份：朋友本名全局唯一，不处理同名；同一成员可配置本名及零到多个日常昵称作为登录名称，所有登录名称规范化后全局唯一。
- 卡片密钥：由项目所有者人工生成，严格为 8 位 ASCII 数字；一个密钥最多绑定一个同行组，一个成员只属于一个同行组且一个同行组最多绑定一个密钥。
- 礼品：首批类型为旅游年卡、电影券、咖啡券；同行组可拥有多份礼品，类型可扩展，礼品实例可覆盖类型默认兑换信息。
- 架构原则：小范围、低并发、低数据量，优先核心功能并避免过度设计。采用 Go 单体、单实例 SQLite 和本机 cron 备份；不做 COS、主从、高可用、复杂恢复、重审计或高并发体系。
- 依赖管理（2026-09-12 定）：Go 依赖以 `vendor/` 目录随仓库提交（开发机 `go mod vendor` 生成，约 137MB，主要为 modernc.org/libc）；Docker 构建用 `-mod=vendor` 零网络——staging 构建机无法直达 proxy.golang.org，Dockerfile 禁止 `go mod download`。升级依赖流程：开发机 `go mod tidy && go mod vendor` 后随代码一起提交。
- 备份约定：SQLite online backup API（非裸 dump）；宿主机独立 `/etc/cron.d/wedding`（不动 Costra 两个 cron 文件），每小时 `:44` 触发、保留最近 72 份（≈3 天）；:44 用于避开整点 pg_dump、:47 主从检查、06:27 刷新、23:5x 分析窗口。健康检查用应用自带 `wedding healthz-check` 子命令（兼容 distroless/scratch）。
- 目录收束约定（2026-09-12 更正）：WeddingGift 全部产出集中到两处根目录——`/root/WeddingGift/`（staging 实际 git clone 位置：代码/compose/脚本/cron 与 nginx 片段仓库副本）与 `/data/wedding/`（data/backups/secrets/import/nginx 子目录）；应用日志走 Docker json-file，不散落其他位置。`app.key` 离线异位保管，重要性等同或高于数据库备份。
- 目录权限模型（2026-09-12 上机实测后修正）：容器内进程恒为 UID 65534 且 cap_drop ALL，bind mount 保留宿主机属主/权限位。`/data/wedding` 顶层与 `import` 必须 root:root `0755`（65534 需穿越顶层并读取 import 内 0644 CSV，`0700` 会全链路 EACCES）；`data`/`backups`/`secrets` 为 `65534:65534` `0700`；`nginx` 为 root:root `0700` 且不进容器。已固化在 `ops/deploy-app.sh` 与 06-m6-runbook §2.3、02 架构 §13.1（v2.2）。
- 开发顺序：先实现最小主页，再通过 `https://stg.costra.cloud/wedding/` 验证真实访问链路，之后开发数据、鉴权和多礼品功能。
- 部署边界：WeddingGift 与 Costra 数据严格隔离；Costra 既有主从与热备仅是共享机器事实，不属于 WeddingGift 架构。
