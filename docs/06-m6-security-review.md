<!--
文档名称: WeddingGift M6 安全与共享边界核对
文档类型: 安全清单 / 残余风险确认
版本: v1.1
日期: 2026-09-13
状态: WG-602 完成，未接受的核心安全问题为零
依赖文档: docs/00-project-brief.md（§7.2/§8）, docs/02-technical-architecture.md（§10/§18）, docs/staging-machine-brief.md
-->

# WeddingGift M6：安全与共享边界核对（WG-602）

本文档落地开发计划 WG-602：对 SQL 注入、XSS、来源校验、枚举差异、会话撤销、缓存、日志泄密与秘密扫描逐项核对并给出自动化证据；复核 Costra 根 Service Worker、同源可执行内容与共享网络边界；确认残余风险。范围严格限定在既有威胁模型内，**不扩展为通用安全审计项目**（技术架构 §18/§19）。

## 目录

- [1. 核对方法](#1-核对方法)
- [2. 应用层安全核对](#2-应用层安全核对)
- [3. 共享边界复核](#3-共享边界复核)
- [4. 残余风险确认](#4-残余风险确认)
- [5. 变更记录](#5-变更记录)

## 1. 核对方法

- 代码审查：以 `internal/` 全部 SQL、模板渲染、日志调用点为对象。
- 自动化证据：`internal/httpx/security_test.go`（M6 新增安全回归）与 M3–M5 既有测试；统一入口 `ops/test.sh`。
- 公网实证：2026-09-12 从开发机对 `https://stg.costra.cloud` 执行只读检查（结果见 §3）。

## 2. 应用层安全核对

| 核对项 | 机制 | 自动化证据 | 结论 |
|---|---|---|---|
| SQL 注入 | 全部 SQL 参数化（`internal/store`），无字符串拼接查询 | `TestLoginSQLInjection`：恒真/注释截断/堆叠删除/密钥位注入统一 401 且与基线逐字节一致，注入后正常登录可用 | 通过 |
| XSS（输出侧） | Go `encoding/json` 默认 HTML 转义；前端动态内容一律 `textContent`；CSP `default-src 'self'` + `object-src 'none'` | `TestBootstrapEscapesHTML`：恶意组称呼输出为 ` <script> `；`ops/test.sh` 步骤 6 静态拦截 `innerHTML`/`insertAdjacentHTML` 等危险 API | 通过 |
| 来源校验 | `/api` Host 白名单（回环恒允许）+ 非 GET 必带同源 Origin | `TestAPIGuards`：非法 Host 404、缺 Origin 403、跨源 403、非 JSON 415、未知字段 400、超 8 KB 400 | 通过 |
| 枚举差异 | 名称无效/密钥无效统一 `401 + auth_failed` 同文案；「密钥已绑定其他同行组」为产品决策要求的明确提示 `401 + key_already_bound`（已绑定密钥对攻击者无可用性，且同样计入限流，见 §5 2026-09-13 增补）；reveal 不存在/跨组统一 `404 + gift_unavailable` 同响应体 | `TestLoginUnifiedFailure`（响应逐字节一致且不设 Cookie）；`TestLoginKeyAlreadyBound`（明确提示、不设 Cookie、与模糊失败可区分）；`TestRevealGiftIsolation`（跨组与未知 ID 响应体一致）；`TestLoginSQLInjection`（注入与基线一致） | 通过 |
| 会话撤销 | 退出删会话；密钥停用删其会话且既有会话立即失效；固定 30 天过期；组停用失效 | `TestRevokedKeyInvalidatesSession`、`TestSessionInvalidatedByRevoke`（store 层）、`TestBootstrapAfterKeyRevoke`（HTTP 层：bootstrap 回 null + 清 Cookie + reveal 401）、`TestExpiredSessionRejectedByAPI` | 通过 |
| 缓存 | 全部 JSON 响应 `Cache-Control: no-store`（`writeJSON` 统一保证）；主页 `no-cache` | `TestLoginSuccess`/`TestRevealGiftFlow`/`TestHealthzReturnsOK` 断言 no-store；`ops/verify-public.sh` §6 公网复核 bootstrap no-store | 通过 |
| 日志泄密 | 字段白名单（request_id/route/status/error_class/internal_key_ref/first_bind）；不记录请求正文、姓名、密钥、礼品码、Cookie、完整 IP | `TestLogsDoNotLeakSecrets`：完整业务流程（登录成功/失败/限流/bootstrap/reveal/logout）后扫描日志，不含任何 fixtures 姓名/昵称/明文密钥/礼品码/会话令牌/Cookie 值 | 通过 |
| 秘密扫描 | `.gitignore` 覆盖 app.key/*.db/真实数据；fixtures 全虚构标记 | `ops/test.sh` 步骤 7：Git 内敏感文件、私钥材料、testdata 的 FAKE 前缀礼品码与 2026 虚构密钥号段 | 通过 |
| 密钥猜测防护 | 8 位密钥仅存 HMAC-SHA256 摘要（`K_access_lookup` 由 app.key 经 HKDF 派生，可抗离线穷举）；统一错误；IP 10 分钟 60 次失败 + 密钥摘要 10 分钟 10 次失败内存限流 | `TestNoPlaintextLeak`（db/wal/shm/备份扫描无明文）；`TestLoginRateLimit`/`TestRateLimiterWindow`；cryptox 冻结测试向量 | 通过 |
| 请求体与超时 | JSON 8 KB 硬上限（nginx 2m 为外层粗防，纵深关系见技术架构 §9.2）；HTTP Read/Write/Idle 超时 | `TestAPIGuards` 超限 400；`internal/app/serve.go` 超时配置 | 通过 |
| Cookie | Secure/HttpOnly/SameSite=Lax；Path 由 nginx `proxy_cookie_path / /wedding/` 收编；仅存 256 bit 随机令牌，库内为 SHA-256 摘要 | `TestLoginSuccess` 属性断言；`ops/canary-smoke.sh` §3 公网冒烟断言 Path 收编 | 通过（Path 收编待 M6 发布后冒烟实证） |
| 礼品码明文边界 | 明文只在 reveal 响应出现；不进 URL/日志/浏览器持久存储 | `TestRevealGiftFlow`/`TestLogsDoNotLeakSecrets`；前端 `revealedCodes` 仅存内存（`ops/test.sh` 步骤 6 禁持久存储 API） | 通过 |

## 3. 共享边界复核

### 3.1 同网可达（`costra_default`）

- `deploy/compose.yaml` 仅声明 `external: true` 加入既有网络；无 `ports:`、无 host network、无 privileged、无 docker.sock；挂载白名单仅 `/data/wedding/{data,backups,secrets,import}`。
- 代码扫描（2026-09-12）：全仓库无 Costra 凭据、连接串、端口（5432/6379/1543x）或 PostgreSQL/Redis 客户端引用；唯一命中为 `ops/verify-public.sh` 中容器名的健康回归（合法用途）。
- 结论：维持简报 §8.1 既定残余风险——网络层可达但无凭据、无客户端、无地址配置、挂载白名单，误用面已最小化。

### 3.2 同源风险（`stg.costra.cloud`）

2026-09-12 只读实证（开发机 → 公网）：

| 检查 | 结果 | 结论 |
|---|---|---|
| 根作用域 Service Worker（`/sw.js`、`/service-worker.js`、`/serviceworker.js`、`/manifest.json`） | 全部 404 | 无根 SW 劫持 `/wedding/` 请求的面 |
| 根路径兜底 | 404 `text/html`，响应带 `X-Content-Type-Options: nosniff`、`X-Frame-Options: DENY` | 无可执行内容，且防 sniff/框架嵌入 |
| `/uploads/`（Costra 用户上传） | 目录列举 403；非常见探测文件 404；无法构造已知上传文件 URL | 无公开可执行内容面；上传文件类型约束属 Costra 侧既有控制，维持既定残余风险 |
| `/v1/`（Costra API） | 404 `application/json` | 非同源脚本面 |
| `/wedding/api/bootstrap`（线上现状） | 404 | M2 占位镜像仍在运行，业务 API 待 M6 发布接入 |

WeddingGift 自身同源防护：会话 Cookie `SameSite=Lax` + Path 收编 `/wedding/`；CSP 禁外源脚本与对象；无第三方脚本/字体/统计。

## 4. 残余风险确认

| 残余风险 | 状态 | 处置 |
|---|---|---|
| `costra_default` 同网可达 | **确认接受** | 无凭据/无客户端/挂载白名单/最小权限；发现异常立即停止 WeddingGift 并复核容器 |
| `stg.costra.cloud` 同源 | **确认接受** | 根 SW 不存在（2026-09-12 实证）；兜底 404 带安全头；`/uploads/` 无公开可执行面；若 Costra 侧变更引入根 SW 或同源可执行内容，立即下线并改独立 origin 重新决策 |
| 整机损坏致主库与本机备份同时丢失 | **确认接受**（既定） | 离线事实源（名单/密钥/礼品码 + app.key）人工重建，不承诺 RPO/RTO |
| 首次故意输入他人姓名误绑 | **确认接受**（既定业务风险） | 所有者停服后人工修正，必要时更换已泄漏礼品码 |

**核对结论：未接受的核心安全问题为零，WG-602 完成。**

## 5. 变更记录

| 版本 | 日期 | 变更内容 |
|---|---|---|
| v1.1 | 2026-09-13 | 绑定约束放宽增补：一个同行组可绑定多个密钥（0001 schema 直接移除 `access_key.group_id` 唯一约束，测试阶段重置数据库不做版本兼容）；「密钥已绑定其他同行组」由统一模糊改为明确提示（`401 + key_already_bound`）——已绑定密钥对攻击者无可用性，失败仍计入 IP/密钥摘要双维度限流，日志纪律不变（`TestLogsDoNotLeakSecrets` 覆盖该新分支），枚举差异结论维持通过 |
| v1.0 | 2026-09-12 | 完成 WG-602：应用层 12 项核对全部通过（含 M6 新增安全回归证据）；同网/同源边界复核与 2026-09-12 公网实证；残余风险确认 |
