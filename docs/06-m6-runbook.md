<!--
文档名称: WeddingGift 正式环境部署操作手册
文档类型: 运维操作手册 / 里程碑记录
版本: v2.2
日期: 2026-09-12
状态: 待上机执行（WG-601/602 本地部分已完成）
依赖文档: docs/03-development-plan.md（M6/WG-603~604）, docs/06-m6-security-review.md, docs/staging-machine-brief.md, docs/04-m0-decisions.md（§2 数据契约）
-->

# WeddingGift 正式环境部署操作手册

本手册覆盖 WeddingGift 在正式机器上的完整生命周期：**从 0 部署 → 配置 → 启动 → 验证 → 日常更新 → 回滚**。

环境前提（项目当前处于开发测试阶段，直接在正式机器上操作）：

- 正式机器即与 Costra 共享的腾讯云机器（`root@106.54.173.55`），正式访问地址为 `https://stg.costra.cloud/wedding/`；
- 正式环境通过 **git 仓库拉取全部代码**，仓库根目录固定为 `/root/WeddingGift/`；
- 运行时数据收束在 `/data/wedding/`（data/backups/secrets/import/nginx 五个子目录），不散落其他位置。

红线（全程有效）：

- 遵守 `staging-machine-brief.md`：只增不改 Costra 资源，禁止重启 `costra-nginx`，不触碰 Costra 的 cron 与数据目录；
- 真实数据、`app.key`、明文密钥/礼品码**永不进 Git**；`app.key` 离线副本异位保管，重要性等同或高于数据库备份；
- 避开避峰窗口执行重型操作：每天 00:00/06:00/12:00/18:00（±10 分钟）、06:27、23:50–24:00；
- 所有命令以 root 在正式机器执行；非交互环境执行 `docker compose run` 时加 `-T`。

## 目录

- [1. 前置条件](#1-前置条件)
- [2. 首次部署（从 0 到可访问）](#2-首次部署从-0-到可访问)
- [3. 配置说明](#3-配置说明)
- [4. 验证](#4-验证)
- [5. 日常发布更新（非首次）](#5-日常发布更新非首次)
- [6. cron 备份管理](#6-cron-备份管理)
- [7. 回滚预案](#7-回滚预案)
- [8. 执行记录（回填）](#8-执行记录回填)
- [9. G6 门禁核对](#9-g6-门禁核对)
- [10. 变更记录](#10-变更记录)

## 1. 前置条件

- 发布候选 commit 在开发机本地 `ops/test.sh` 全绿（gofmt/vet/`go test -race`/静态构建/fixtures 契约/前端静态检查/秘密扫描）。
- 真实数据包已按 §3.3 准备好（含 canary 模板），人工核对清单逐项打勾。
- 当前时间避开避峰窗口（见上方红线）。

## 2. 首次部署（从 0 到可访问）

> 机器已运行 Costra 三套栈，本节全部操作均为**新增**，不修改任何既有资源。
> 步骤有严格顺序：目录与 app.key → 构建 → 迁移 → 导入 → 启动 → nginx → 验证。`serve` 启动要求 schema 已迁移且 app.key 可读，缺一直接退出，因此必须先完成 2.2～2.6 再启动容器。

### 2.1 拉取代码

```sh
git clone <仓库地址> /root/WeddingGift
cd /root/WeddingGift && git checkout <发布候选 commit>
```

### 2.2 机器只读自检

```sh
ops/staging-selfcheck.sh
```

全部为只读命令，结果保存到 `ops/records/selfcheck-<UTC时间戳>.log`。逐项核对：CPU/内存/磁盘余量足以再容纳 256M/0.5 CPU；`costra_default` 网络存在；cron 与 nginx live conf 中无 wedding 残留；Costra 容器无异常。任一不达标：停止排查，不进入下一步。

### 2.3 创建数据目录

```sh
mkdir -p /data/wedding/data /data/wedding/backups /data/wedding/secrets /data/wedding/import /data/wedding/nginx
# 权限模型（容器内进程为 65534，bind mount 保留宿主机属主与权限位）：
#   - 顶层与 import 为 root:root 0755——65534 必须能穿越顶层、并穿越+读取 import 内的
#     CSV（文件由 root 放置，0644）；若为 root:root 0700，validate/import/serve 全链路 EACCES；
#   - data/backups/secrets 为 65534:65534 0700——应用以非 root 写库、写备份、读 app.key；
#   - nginx 为 root:root 0700——不进容器，仅 root 人工操作（改前备份与片段副本）。
chmod 755 /data/wedding /data/wedding/import
chmod 700 /data/wedding/data /data/wedding/backups /data/wedding/secrets /data/wedding/nginx
chown 65534:65534 /data/wedding/data /data/wedding/backups /data/wedding/secrets
```

### 2.4 生成并放置 app.key（一次性配置）

app.key 是应用主秘密（派生密钥 HMAC 与礼品码加密密钥），要求 ≥32 字节熵：

```sh
# 生成（十六进制文本形态；也可用 head -c 32 /dev/urandom 的原始字节形态）
openssl rand -hex 32 > /data/wedding/secrets/app.key
chmod 600 /data/wedding/secrets/app.key
chown 65534:65534 /data/wedding/secrets/app.key
ls -l /data/wedding/secrets/app.key   # 确认 -rw------- 1 65534 65534
```

**立即把 app.key 复制到项目所有者的离线位置异位保管**（与真实数据源文件同级）。app.key 丢失则数据库内密钥摘要与礼品密文全部失效，备份也救不回。

### 2.5 构建并传输镜像（开发机 → 正式机）

正式机无法直达 Go 模块源与镜像源，**镜像一律在开发机构建**（Dockerfile 构建阶段需拉取 `golang:1.27-alpine` 并 `go mod download`），打包传输到正式机后 load：

```sh
# 1. 开发机仓库根执行：构建镜像并打包压缩
#    产出 dist/wedding-app-<version>-<commit>-<UTC时间戳>.tar.gz（dist/ 已 gitignore）
ops/build-image.sh

# 2. 传输到正式机器（开发机执行）
scp dist/wedding-app-<version>-<commit>-<时间戳>.tar.gz root@106.54.173.55:/root/WeddingGift/dist/

# 3. 正式机器加载镜像
cd /root/WeddingGift && ops/load-image.sh
```

记录 `version/commit/build_time`（build-image.sh 输出，写入 §8 执行记录）。

### 2.6 数据库 schema 迁移

```sh
docker compose -p wedding -f deploy/compose.yaml run --rm app migrate
# 预期输出：migrate 完成：本次应用 N 个 migration，当前 schema 版本 1
```

### 2.7 导入真实数据（含 canary）

数据包 = 六个契约 CSV（字段见 `docs/04-m0-decisions.md` §2）= 真实数据 + canary 虚构数据（模板见 §3.3）。导入要求空库、单事务、任一错误零写入，**canary 必须与真实数据同批导入**（无法事后补录）。

```sh
# 1. 开发机上本地预检（零写入 dry-run）
go run ./cmd/wedding data validate --dir <真实数据目录>

# 2. 上传数据包到正式机器（文件 0644 root:root——容器内 65534 需读取；导入后即清理）
scp -r <真实数据目录>/ root@106.54.173.55:/data/wedding/import/

# 3. 上机复核（dry-run，任一错误零写入）
docker compose -p wedding -f deploy/compose.yaml run --rm app data validate --dir /data/wedding/import

# 4. 单事务导入
docker compose -p wedding -f deploy/compose.yaml run --rm app data import --dir /data/wedding/import

# 5. 脱敏数量核对：与离线事实源逐项比对（组/成员/登录名称/礼品/密钥计数）
docker compose -p wedding -f deploy/compose.yaml run --rm app status

# 6. 回滚点：导入后立即备份一份数据库
TS="$(date -u +%Y%m%d-%H%M%S)"
docker compose -p wedding -f deploy/compose.yaml run --rm app backup create \
  --output "/data/wedding/backups/wedding-pre-release-${TS}.db"
```

### 2.8 启动服务

```sh
ops/start-app.sh                      # 起服并等待健康（healthy 前 serve 不会对外响应）
docker stats --no-stream              # wedding-app 远低于 256M/0.5 CPU，Costra 容器无异常
```

### 2.9 nginx 接入（人工操作，禁用脚本）

修改对象是 Costra 的 live nginx 配置，按 `staging-machine-brief.md` §3.2 五条纪律逐条人工执行：

```sh
# 1. 先备份再改；首次插入另存一份原始备份（供三个月后拆除对照）
cp -p /data/conf/nginx/costra.cloud.conf "/data/wedding/nginx/costra.cloud.conf.$(date -u +%Y%m%d-%H%M%S).bak"
cp -p /data/conf/nginx/costra.cloud.conf /data/wedding/nginx/costra.cloud.conf.orig.bak

# 2. 只增不改：编辑 /data/conf/nginx/costra.cloud.conf，在 443 server block 的
#    兜底 location / { return 404; } 正上方，原样插入 deploy/nginx-wedding.conf 全部内容
#    （BEGIN/END 标记之间为整块）。插入前确认兜底 location 恰好 1 处：
grep -nE 'location / \{[[:space:]]*return 404' /data/conf/nginx/costra.cloud.conf

# 3. 改后验证 + 零停机热加载（禁止 restart 或 compose up 重建 costra-nginx）
docker exec costra-nginx nginx -t && docker exec costra-nginx nginx -s reload

# 4. 自留副本
cp -p /data/conf/nginx/costra.cloud.conf "/data/wedding/nginx/costra.cloud.conf.$(date -u +%Y%m%d-%H%M%S).after"
```

若 `nginx -t` 失败：**不要 reload**，用第 1 步备份恢复后重新校验：`cp -p <备份> /data/conf/nginx/costra.cloud.conf && docker exec costra-nginx nginx -t`。

### 2.10 安装备份 cron

```sh
# 独立 cron 单元（不动 Costra 的 /etc/cron.d/costra-standby、costra-analyze）
install -m 0644 -o root -g root deploy/wedding.cron /etc/cron.d/wedding

# 手动执行两次，验证产出与轮转逻辑（第二次不报错、各生成一份备份）
ops/backup.sh && ops/backup.sh
ls -lt /data/wedding/backups/ | head -5
tail -5 /data/wedding/backups/backup.log

# 确认 Costra cron 未被触碰
md5sum /etc/cron.d/costra-standby /etc/cron.d/costra-analyze
```

cron 每小时 `:44` 触发，保留最近 72 份（≈3 天）。首次自动备份在下个 `:44` 后确认。

### 2.11 清理明文源文件

导入核对无误、源文件已转回项目所有者离线位置后：

```sh
rm -rf /data/wedding/import/*
```

至此首次部署完成，进入 §4 验证。

## 3. 配置说明

### 3.1 环境变量（compose 已注入默认值，按需覆盖）

| 变量 | 默认值 | 说明 |
|---|---|---|
| `WEDDING_LISTEN_ADDR` | `:3000` | HTTP 监听地址（仅容器内网，不发布宿主机端口） |
| `WEDDING_DB_PATH` | `/data/wedding/data/wedding.db` | SQLite 数据库路径 |
| `WEDDING_APP_KEY_PATH` | `/data/wedding/secrets/app.key` | 主秘密路径（只读挂载） |
| `WEDDING_ALLOWED_HOSTS` | `stg.costra.cloud` | `/api` 的 Host 白名单（逗号分隔） |
| `WEDDING_COUPLE_NAMES` / `WEDDING_DATE` / `WEDDING_THANKS_MESSAGE` 等 | 见 M0 §3.1 | 页面文案变量 |

### 3.2 容器基线（deploy/compose.yaml 要点，勿随意改动）

独立 project `wedding`、容器名 `wedding-app`；只 expose 3000、以外部网络加入 `costra_default`；非 root（65534）+ 只读 rootfs + tmpfs /tmp + cap_drop ALL + no-new-privileges；内存 256M / CPU 0.5 硬上限；json-file 日志 50m×3；仅挂载 `/data/wedding` 自有目录（secrets、import 只读）。

### 3.3 数据包规范（含 canary 模板）

六个契约 CSV 的字段定义见 `docs/04-m0-decisions.md` §2。canary 虚构数据用于 §4.2 冒烟，与 `ops/canary-smoke.sh` 默认值一一对应：

```csv
# recipient_groups.csv 追加
CANARY1,金丝雀甲测试组,,active
CANARY2,金丝雀乙测试组,,active

# members.csv 追加
MC01,CANARY1,金丝雀甲,true,1
MC02,CANARY2,金丝雀乙,true,1

# member_login_names.csv 追加
MC01,金丝雀甲,real_name
MC01,小雀甲,nickname
MC02,金丝雀乙,real_name

# access_keys.csv 追加（虚构号段，绝不与真实密钥冲突）
KC01,20269001,active,
KC02,20269002,active,
KC03,20269003,active,

# gift_items.csv 追加（激活码为虚构值）
GIC01,CANARY1,coffee_voucher,CANARYCOFFEE901,,,,1,active
GIC02,CANARY2,travel_annual_card,CANARYTRAVEL902,,,,1,active
GIC03,CANARY2,movie_voucher,CANARYMOVIE903,,,,2,active
```

说明：组 A 一名成员带昵称（覆盖多名称冒烟）、一份礼品；组 B 两份礼品（覆盖多礼品显示）；KC03 不绑定任何组，用于"组内第二密钥拒绝"用例。

导入前人工核对（开发计划 §6.3 必做项）：

- [ ] 本名唯一无错别字；昵称归属正确且与任何本名/昵称不冲突；
- [ ] 每成员只属一组，每组至少一名可登录成员；每组至少一份礼品、顺序正确；
- [ ] 密钥严格 8 位 ASCII 数字、全局唯一、未被表格破坏前导零；
- [ ] 三类礼品数量与采购数量一致；激活码未被转成科学计数法/日期；
- [ ] 兑换链接全部 HTTPS；canary 模板已并入且密钥为 2026900x 虚构号段。

## 4. 验证

### 4.1 真实 URL 自动化验证（25 项检查）

```sh
ops/verify-public.sh
```

覆盖：主页 200 且为真实单页应用、`/wedding` 301、相对静态资源 200、`/wedding/healthz` status/database 均 ok、无根路径绝对引用、bootstrap API（session null/no-store/文案字段）、API 防护（401/403/404/415）、安全响应头（CSP/nosniff/DENY/Referrer-Policy）、Costra 回归（/healthz 200、兜底 404、未占用 /v1/）、staging 本机容器与资源回归。在任意能访问公网的机器都可再跑一遍复核。

### 4.2 canary API 冒烟（22 项断言）

```sh
ops/canary-smoke.sh
```

覆盖：首绑登录、同组多名称再登录、同组另一成员登录、跨组拒绝、组内第二密钥拒绝、多礼品摘要与逐份 reveal、Cookie Path 收编（/wedding/）、退出与旧会话失效。脚本幂等，已绑定状态下重跑依然全过。

### 4.3 浏览器人工冒烟（目标浏览器矩阵，G5/G6 门禁）

- [ ] iOS Safari：扫码打开 → canary 甲登录 → 逐份显示/复制/隐藏 → 退出；
- [ ] Android Chrome：同上；
- [ ] 微信 WebView：同上，并验证剪贴板受限时的长按选择复制降级；
- [ ] 窄屏无横向滚动；触控目标可点；页面无根路径 404 资源。

### 4.4 发布后复核

- [ ] `docker stats --no-stream`：wedding-app 远低于 256M/0.5 CPU，Costra 各容器无异常；
- [ ] `docker logs --tail 100 wedding-app`：无明文姓名/密钥/礼品码，无异常报错；
- [ ] TLS 与响应头已由 `ops/verify-public.sh` §8 覆盖；
- [ ] canary 密钥默认保持 active 不停用（虚构数据无泄漏风险，供后续复冒烟）。如需关闭：`docker compose -p wedding -f deploy/compose.yaml run --rm app key revoke --ref KC01`（注意 revoke 后冒烟脚本第 2/7 步将按预期失败）。

## 5. 日常发布更新（非首次）

代码、配置、数据结构有变更时的标准发布序列：

```sh
# 1. 开发机构建新镜像并传输（见 §2.5 第 1–2 步）；正式机：
cd /root/WeddingGift && git pull && git checkout <发布 commit>
ops/load-image.sh

# 2. 发布前回滚点：数据库备份 + nginx 配置备份
TS="$(date -u +%Y%m%d-%H%M%S)"
docker compose -p wedding -f deploy/compose.yaml run --rm app backup create \
  --output "/data/wedding/backups/wedding-pre-release-${TS}.db"
cp -p /data/conf/nginx/costra.cloud.conf "/data/wedding/nginx/costra.cloud.conf.${TS}.pre-release"

# 3. 停旧容器 → schema 迁移（无新 migration 时为 no-op）→ 启动新容器
#    （load 新镜像后 up -d 自动重建容器使用最新镜像；start-app.sh 内含等待健康）
ops/stop-app.sh
docker compose -p wedding -f deploy/compose.yaml run --rm app migrate
ops/start-app.sh

# 4. nginx 热加载（容器重建后重新解析容器地址；不修改配置则无需备份/编辑）
docker exec costra-nginx nginx -t && docker exec costra-nginx nginx -s reload

# 5. 验证
ops/verify-public.sh
ops/canary-smoke.sh
```

## 6. 备份与数据维护

### 6.1 cron 备份管理

- 安装/更新：`install -m 0644 -o root -g root deploy/wedding.cron /etc/cron.d/wedding`（首次部署已在 §2.10 完成）；
- 手动备份：`ops/backup.sh`；产出 `/data/wedding/backups/wedding-<UTC时间戳>.db`，日志追加到 `/data/wedding/backups/backup.log`；
- 轮转：`ops/backup.sh` 内 `KEEP=72`，每小时一份保留 ≈3 天；
- 确认 Costra cron 未被触碰：`md5sum /etc/cron.d/costra-standby /etc/cron.d/costra-analyze`。

### 6.2 从备份还原（ops/restore-db.sh）

线上数据出问题时的标准恢复手段：

```sh
ops/stop-app.sh                              # 先停服（脚本仅操作数据库，运行中直接拒绝执行）
ops/restore-db.sh                            # 还原 backups 下最新备份（交互确认）
ops/restore-db.sh wedding-<UTC时间戳>.db     # 指定 backups 目录下文件名
ops/restore-db.sh <备份文件> --yes           # 非交互确认
ops/start-app.sh                             # 完成后起服
```

脚本流程：备份体检（存在/非空/SQLite 魔数）→ 兜底备份当前库（`wedding-pre-restore-<TS>.db`；当前库已损坏则移入 `data/quarantine-<TS>/` 保留，不销毁）→ 替换 `wedding.db` → migrate（旧 schema 备份自动前滚）→ 输出 status 脱敏计数供核对。**任一环节失败自动回滚到还原前状态**（服务保持停止，起服由操作者确认后显式执行）。注意：备份只能与生成时的同一把 app.key 配合；还原后旧会话在 30 天 TTL 内仍有效，需强制重新登录时按架构 §17.3 人工清空 SESSION 表。

### 6.3 清库重导（测试循环，ops/reset-db.sh）

开发测试阶段在正式环境反复测试后的重置手段（`data import` 要求空库，清库是重新导入的前置）：

```sh
ops/stop-app.sh          # 先停服（脚本仅操作数据库，运行中直接拒绝执行）
ops/reset-db.sh          # 交互确认
ops/reset-db.sh --yes    # 非交互
ops/start-app.sh         # 完成后起服
```

脚本流程：打印当前 status 留档 → 清库前自动备份（`wedding-pre-reset-<TS>.db`）→ 删库（含 -wal/-shm）→ migrate 重建 schema。完成后按 §2.7 第 3–5 步重新 validate / import / status。app.key 保持不变，同一数据包可重复导入。

## 7. 回滚预案

| 场景 | 操作 |
|---|---|
| 数据预检/导入失败 | 未启动新服务，数据库保持导入前状态（单事务零写入）；修正离线数据后从 §2.7 第 3 步重来 |
| 导入后数量核对不符 | 不启动服务；离线核对事实源，必要时 `rm /data/wedding/data/wedding.db*` 后从 §2.6 重来（密钥摘要与礼品密文随库重建，app.key 不变即可） |
| 启动后应用异常 | `ops/stop-app.sh`；用 `ops/restore-db.sh <备份文件>` 恢复（仅操作 DB，先停服、失败自动回滚、完成后 `ops/start-app.sh`；可选恢复后清空 SESSION）；镜像问题则在开发机 `git checkout <上一 commit>` 重新 `ops/build-image.sh`，传输后 load 并 `ops/start-app.sh` |
| 需整体下线业务 | 按 M2 runbook §6 人工删除 nginx wedding 块并热加载，再 `docker compose -p wedding -f deploy/compose.yaml down`（真实数据库文件保留不动，操作前再次备份） |
| nginx 异常（`/wedding/` 全 404） | 第一嫌疑是 live conf 被 Costra 模板刷新覆盖：`grep -c wedding /data/conf/nginx/costra.cloud.conf` 为 0 即确认，按 §2.9 重新插入片段并热加载 |

回滚后验证：`ops/verify-public.sh` 按当前形态核对（业务下线形态时第 1/6/7 项预期失败）；Costra 回归项必须全过。

## 8. 执行记录（回填）

> 执行后回填本节，作为 G6 门禁与审计依据。

- 执行人 / 执行时间（UTC）：
- 发布版本：`version=______ commit=______`（schema 版本：1）
- app.key 离线异位保管确认：
- 真实数据核对：组 __ / 成员 __ / 登录名称 __ / 礼品 __ / 密钥 __（与离线事实源一致：是/否）
- 回滚点：数据库备份 `______`；nginx 备份 `______`
- `verify-public.sh`：__ 通过 / __ 失败
- `canary-smoke.sh`：__ 通过 / __ 失败
- 浏览器人工冒烟（iOS Safari / Android Chrome / 微信 WebView）：
- cron 安装时间与首次自动备份确认（下个 :44 后 `ls -lt /data/wedding/backups/`）：
- `docker stats` 摘要与 Costra 回归结论：
- 明文源文件清理确认：
- 异常与处理：

## 9. G6 门禁核对

- [ ] 产品验收 PA-01～PA-15 全部通过（PA-01/14 由 `verify-public.sh` 与浏览器冒烟覆盖，其余由 `ops/test.sh` 自动化与 `canary-smoke.sh` 覆盖）；
- [ ] 真实数据数量与离线事实源一致；明文源文件已移出服务器；
- [ ] canary 冒烟（API + 三目标浏览器）通过；
- [ ] cron 独立单元安装，手动备份两次成功，最近备份确认，72 份轮转逻辑就位；
- [ ] 发布版本、回滚点（数据库 + nginx 备份）已记录；
- [ ] Costra 原路径、容器与资源状态不变；
- [ ] WG-602 安全核对结论维持"未接受的核心安全问题为零"。

**全部通过后，才通知真实来宾使用（开发计划 G6）。**

## 10. 变更记录

| 版本 | 日期 | 变更内容 |
|---|---|---|
| v2.3 | 2026-09-13 | 发布流程改为「开发机构建镜像压缩包 + 正式机 load」（正式机无法直达 Go 模块源/镜像源）：新增 `ops/build-image.sh`（开发机构建 + `docker save` 打包到 `dist/`）与 `ops/load-image.sh`（正式机体检/解压/load）；新增 `ops/stop-app.sh`、`ops/start-app.sh` 独立停起服脚本；`ops/reset-db.sh` 与 `ops/restore-db.sh` 改为**仅操作数据库**（不停起服，容器运行中直接拒绝执行，回滚也不自动起服）；§2.5/§2.8/§5/§6.2/§6.3/§7 同步 |
| v2.2 | 2026-09-12 | 修正 §2.3 目录权限缺陷：`/data/wedding` 顶层与 `import` 由 `0700` 改 `0755`（root 属主不变）——容器 65534 需穿越顶层并读取 import 内 CSV，原写法导致 validate/import/serve 全链路 permission denied（上机实测）；`nginx` 明确 root:root `0700` 不进容器；§2.7 上传文件明确 `0644`；与 `ops/deploy-app.sh` 同步 |
| v2.1 | 2026-09-12 | 新增数据维护脚本：`ops/reset-db.sh`（清库重置，清库前自动备份）与 `ops/restore-db.sh`（备份还原，失败自动回滚）；§6 扩展为「备份与数据维护」，§7 应用异常回滚改走脚本 |
| v2.0 | 2026-09-12 | 重写为自包含操作手册：从 0 部署全序列（拉码/自检/目录/app.key/构建/迁移/导入/启动/nginx/cron）、配置说明、验证、日常发布更新、回滚；不再以"M2 已就位"为前提，去除测试环境表述 |
| v1.1 | 2026-09-12 | 仓库根目录由 `/opt/wedding` 更正为 `/root/WeddingGift`（staging 实际 git clone 位置） |
| v1.0 | 2026-09-12 | 完成 M6 上机手册：真实数据包 canary 模板、WG-603 导入序列、WG-604 发布与冒烟（verify-public/canary-smoke/浏览器矩阵）、cron 启用、回滚预案与 G6 门禁 |
