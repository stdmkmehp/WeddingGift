# WeddingGift

婚礼回礼卡登录与礼品展示服务。来宾扫码进入 `https://stg.costra.cloud/wedding/`，输入本名或昵称 + 8 位卡片密钥，查看本同行组的礼品激活码。

设计与规则以 `docs/` 与 `docs/staging-machine-brief.md` 为事实源。当前进度：**M0 已冻结，M1 最小工程与占位主页、M2 真实 URL 链路均已在 staging 验收通过（G2 门禁通过，见 `docs/05-m2-runbook.md`），M3 数据、加密与管理能力、M4 鉴权与会话、M5 多礼品与来宾体验均已完成本地实现与测试；M6 本地部分已完成（WG-601 自动化回归、WG-602 安全核对），上机部分手册就绪（WG-603 真实数据导入、WG-604 发布与冒烟，见 `docs/06-m6-runbook.md`），待执行正式开放**。

## 目录结构

```
cmd/wedding/        单二进制入口（serve / migrate / data / status / key / backup / healthz-check / version）
internal/
  app/              配置、serve 生命周期、健康检查子命令
  admin/            数据文件装载、脱敏核对报告、状态文本
  auth/             登录决策、绑定编排（一组可多密钥）、会话创建/解析/退出（分层错误）
  buildinfo/        编译期注入的版本信息
  cryptox/          app.key 加载、HKDF 派生、密钥 HMAC、礼品码 AEAD、会话令牌
  domain/           领域实体、登录名称规范化、数据契约校验（不依赖 HTTP/SQLite）
  gift/             礼品码 reveal（归属校验 + 按需解密）
  httpx/            HTTP 路由与中间件（安全头、请求 ID、来源校验、限流、API）
  store/            SQLite 连接、migration、repository、BEGIN IMMEDIATE 绑定事务、online backup
migrations/         SQL migration（go:embed 进二进制）
web/                单页应用（index.html + app.css + app.js，go:embed 进二进制）
deploy/             共享机器 compose、nginx 插入片段、cron 单元样例
ops/                本地预览、构建、部署、停起服、数据维护与本机备份脚本
testdata/fixtures/  虚构数据包（M0 数据契约样例，无真实数据）
testdata/fixtures-append/  虚构追加数据包（`data append` 契约样例与演练，无真实数据）
docs/               需求 / 产品 / 技术 / 开发计划 / M0 决策 / M2 手册 / M6 安全核对与手册
```

## M6 质量收口与正式开放

本地部分（已完成）：

```sh
# WG-601 核心自动化回归：格式 / 静态检查 / 竞态测试（单元、数据校验、repository、
# 绑定一致性、HTTP、安全回归、来宾旅程 E2E）/ 发布形态静态构建
gofmt -l .                                   # 无输出为通过
go vet ./...
go test -race ./...
CGO_ENABLED=0 go build -o /tmp/wedding ./cmd/wedding

# fixtures 契约自检（虚构数据包本身即契约样例，validate 不过则契约已漂移）
go run ./cmd/wedding data validate --dir testdata/fixtures
```

前端与秘密侧不设脚本，按约定人工 + CR 把关：`web/` 内不得出现 `innerHTML`/`insertAdjacentHTML` 等危险 API、不得引入外部资源、不得使用本地持久存储；真实数据与 `app.key` 绝不进 Git（见 `.gitignore`）。

- WG-602 安全与共享边界核对：SQL 注入、XSS、枚举差异、会话撤销、缓存、日志泄密、秘密扫描逐项通过，同网/同源残余风险已确认（2026-09-12 公网实证），见 `docs/06-m6-security-review.md`。
- 真实 URL 与 canary API 的验证脚本（`verify-public.sh` / `canary-smoke.sh`）已移除，改为人工按 `docs/06-m6-runbook.md` §4 的清单核对；上机自检（`staging-selfcheck.sh`）同样改为按手册 §2.2 的只读命令人工核对。

上机部分（手册见 `docs/06-m6-runbook.md`）：真实数据包（含 canary 模板）本地 dry-run → staging 构建新镜像 → migrate → 上机 validate → 单事务 import → 数量核对 → 发布前回滚点（数据库 + nginx 备份）→ 启动正式服务 → nginx 热加载 → 按 §4 人工核对（真实 URL + canary API）+ 三目标浏览器人工冒烟 → 安装独立 cron `/etc/cron.d/wedding` 并验证备份轮转 → 明文源文件移出服务器。

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

# 上线后纯追加（要求已首导；开放世界校验 + 单事务纯 INSERT，在线可用；--dry-run 零写入复核）
go run ./cmd/wedding data append --dir testdata/fixtures-append --dry-run
go run ./cmd/wedding data append --dir testdata/fixtures-append

# 脱敏状态（内部编号与数量，无明文）
go run ./cmd/wedding status

# 停用密钥并删除其会话（不自动补发、不改绑）
go run ./cmd/wedding key revoke --ref K001

# SQLite online backup 一致性备份（含完整性检查，目标存在则拒绝覆盖）
go run ./cmd/wedding backup create --output /path/to/wedding-backup.db

# 每日统计日报：滚动窗口（默认 24h）新增绑定/登录 + 累积数据，渲染纯文本邮件
# --dry-run 只打印不发送；实发需先配置 WEDDING_SMTP_* / WEDDING_MAIL_*（见 docs/06 §6.5）
go run ./cmd/wedding report daily --dry-run
```

本机 cron（staging）：`install -m 0644 -o root -g root deploy/wedding.cron /etc/cron.d/wedding`，每小时 `:44` 由 `ops/backup.sh` 通过容器内二进制产出备份并保留最近 72 份；每天 `07:17` 由 `ops/daily-report.sh` 发送统计日报（SMTP 配置在 `/data/wedding/secrets/smtp.env`，经 compose `env_file` 注入，不进 Git）；不修改 Costra 的 cron 文件。备份还原用 `ops/restore-db.sh`（缺省取最新备份，还原前自动兜底备份，失败自动回滚）；测试循环的清库重导用 `ops/reset-db.sh`（清库前自动备份，停服删库后 migrate 重建 schema）。

## M4 鉴权与会话

`serve` 自 M4 起完整接线：启动时打开数据库（要求已 `migrate`）并加载 `app.key`，缺一即退出。

公网 API（浏览器路径 `/wedding/api/...`，nginx 抹前缀后应用见 `/api/...`）：

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/api/bootstrap` | 服务文案 + 当前会话（同行组称呼/专属短句/礼品摘要）；无会话时 `session: null` |
| POST | `/api/auth/login` | 名字或昵称 + 8 位密钥登录；首绑在 `BEGIN IMMEDIATE` 事务内完成；成功写 `wedding_session` Cookie |
| POST | `/api/auth/logout` | 删除会话并清 Cookie；幂等 |
| POST | `/api/gifts/reveal` | M5 起可用：按 `giftId` 返回本组某份礼品的明文激活码；需有效会话，跨组/未知 ID 统一 `404 + gift_unavailable`，响应 no-store |

关键语义：

- 失败分三层：名称无效或密钥无效/停用统一 `401 + auth_failed` 同一文案；「密钥已绑定其他同行组」返回 `401 + key_already_bound` 明确提示（可联系新郎处理）；格式错误（公开规则）返回 `400 + invalid_input`。
- 会话为 256 bit 随机令牌，数据库存 SHA-256 摘要；固定 30 天有效期（§9.4 授权的收敛决策）；密钥停用或组停用后既有会话立即失效。
- 防护：/api 要求 Host 白名单（`WEDDING_ALLOWED_HOSTS`，回环地址总是允许）+ 非 GET 必带同源 Origin；JSON 8 KB 上限、拒绝未知字段；登录按 IP（10 分钟 60 次失败）与密钥摘要（10 分钟 10 次失败）双维度内存限流。
- 文案由环境变量配置（`WEDDING_COUPLE_NAMES`、`WEDDING_THANKS_MESSAGE` 等，默认 M0 占位值）。

本地冒烟：

```sh
export WEDDING_DB_PATH=./local.db WEDDING_APP_KEY_PATH=./app.key WEDDING_ALLOWED_HOSTS=127.0.0.1
go run ./cmd/wedding migrate && go run ./cmd/wedding data import --dir testdata/fixtures
go run ./cmd/wedding serve &
curl -c jar.txt -X POST http://127.0.0.1:3000/api/auth/login \
  -H 'Content-Type: application/json' -H 'Origin: http://127.0.0.1:3000' \
  -d '{"loginName":"张伟","keyCode":"20260002"}'
curl -b jar.txt http://127.0.0.1:3000/api/bootstrap
# 逐份揭示礼品码（giftId 来自 bootstrap/login 响应的礼品摘要）
curl -b jar.txt -X POST http://127.0.0.1:3000/api/gifts/reveal \
  -H 'Content-Type: application/json' -H 'Origin: http://127.0.0.1:3000' \
  -d '{"giftId":"<礼品内部 ID>"}'
```

## M5 多礼品与来宾体验

单页应用（`web/`，全部相对路径，无前端框架/CDN/外部字体）：

- 视觉按产品架构 §10.2 令牌：暖象牙底、灰粉酒红主色、香槟金装饰；卡片 20px 圆角、控件 12px、触控目标 ≥44px、主区域 ≤560px、`prefers-reduced-motion` 生效。
- 状态机：初始化（加载态，不闪现礼品）→ 登录 → 同行组欢迎 + 多礼品列表 → 退出/会话失效回登录；bootstrap 失败进入维护态（`WEDDING_MAINTENANCE_TEXT`，可重试）。
- 登录交互：格式就地拦截（空名称、非 8 位数字不发请求）、提交防重复与加载态、统一失败/限流/网络异常文案。
- 礼品交互：初始全部遮罩；逐份"显示激活码"（reveal 接口）→"复制"（剪贴板 API，不可用时选中文本提示长按）→"隐藏"；退出、刷新或会话失效自动清空全部明文；动态内容一律 `textContent` 渲染。
- 合规：页脚隐私短说明、联系提示、备案号链接均由 bootstrap 文案驱动（`WEDDING_PRIVACY_NOTE` 等）。

## 本地开发

需要 Go 1.27+。

```sh
# 运行测试
go test ./...

# 静态检查与格式
go vet ./...
gofmt -l .

# 本地预览（一键：隔离目录内准备 app.key 与库 → migrate → 导入 fixtures → 起服）
ops/local_start.sh                 # http://127.0.0.1:3000/（Ctrl-C 停止）
ops/local_start.sh --port 3211     # 换端口（端口被占用时会预检报错）
ops/local_start.sh --reset         # 删除本地临时库重建并重新导入 fixtures
ops/local_start.sh --no-import     # 只建 schema，仅看登录页
# 登录样例（testdata/fixtures）：张伟 + 20260002 / 王芳 + 20260001 / 陈强 + 20260003
# 注意：web/ 经 go:embed 编译进二进制，改 HTML/CSS/JS 后必须重跑脚本

# 等价手工步骤（M4 起需先准备 app.key 并完成 migrate/import，见上文 M3/M4 段）
export WEDDING_DB_PATH=./local.db WEDDING_APP_KEY_PATH=./app.key
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

构建统一走 `deploy/compose.yaml` 的 `build` 段，版本信息经 `WEDDING_VERSION`/`WEDDING_COMMIT`/`WEDDING_BUILD_TIME` 环境变量注入（缺省回退 dev/unknown）：

```sh
# 仅构建镜像（注入版本信息）
WEDDING_VERSION="$(git describe --tags --always --dirty)" docker compose -f deploy/compose.yaml build
```

共享机器部署由 `ops/deploy-app.sh` 驱动（external network `costra_default`、只 expose 不发布端口、256M/0.5CPU、json-file 日志）。

## M2 真实 URL 链路（staging 执行）

该链路已于 2026-09-12 在 staging 验收通过（G2 门禁，详见 `docs/05-m2-runbook.md`）。当时的 `ops/staging-selfcheck.sh` 与 `ops/verify-public.sh` 已随 M6 脚本精简移除，现在对应动作是：

```sh
# WG-201 只读自检：按 docs/06-m6-runbook.md §2.2 的命令人工核对（不再产出自检日志）
ops/deploy-app.sh          # WG-202 创建 /data/wedding 目录并启动 wedding-app
# WG-203：人工按 runbook §4 插入 /wedding/ location（片段：deploy/nginx-wedding.conf）并热加载
# 真实 URL + Costra 回归：按 docs/06-m6-runbook.md §4.1 人工核对
```

注意：nginx 修改的对象是 Costra 项目的 live 配置，一律**人工按 runbook 操作**，不使用脚本；回滚同样人工按 runbook §6 执行。

## 安全与边界

- 明文卡片密钥不入库（HMAC 摘要），礼品码 AEAD 加密，数据库与备份中扫描不到明文。
- 日志不记录姓名、密钥、礼品码、Cookie、请求正文。
- 真实数据、`app.key`、明文密钥/礼品码绝不进入 Git（见 `.gitignore`）。
- 共享机器红线以 `docs/staging-machine-brief.md` 为准：不触碰 Costra 任何数据、配置与端口。
