<!--
文档名称: WeddingGift M6 正式开放操作手册
文档类型: 运维操作手册 / 里程碑记录
版本: v1.0
日期: 2026-09-12
状态: 待上机执行（WG-601/602 本地部分已完成）
依赖文档: docs/03-development-plan.md（M6/WG-601~604）, docs/06-m6-security-review.md, docs/05-m2-runbook.md, docs/staging-machine-brief.md
-->

# WeddingGift M6：质量收口与正式开放操作手册

本手册落地开发计划 M6 的上机部分：WG-603（导入真实数据并启用 cron）与 WG-604（发布正式版本并冒烟）。本地部分（WG-601 自动化回归、WG-602 安全核对）已完成，结论：

- **WG-601**：`ops/test.sh` 一条命令全绿（gofmt/vet/`go test -race`/静态构建/fixtures 契约/前端静态检查/秘密扫描）；新增 `internal/httpx/security_test.go` 安全回归与来宾旅程 E2E。
- **WG-602**：`docs/06-m6-security-review.md` 全部核对通过，残余风险已确认。

红线提醒：全程遵守 `staging-machine-brief.md`；nginx 操作按 M2 runbook §4 纪律人工执行；真实数据、`app.key`、明文密钥/礼品码永不进 Git。

## 目录

- [1. 前置条件](#1-前置条件)
- [2. 真实数据包规范（含 canary 模板）](#2-真实数据包规范含-canary-模板)
- [3. WG-603 真实数据导入](#3-wg-603-真实数据导入)
- [4. WG-604 发布与冒烟](#4-wg-604-发布与冒烟)
- [5. cron 启用与备份验证](#5-cron-启用与备份验证)
- [6. 执行记录（回填）](#6-执行记录回填)
- [7. 回滚预案](#7-回滚预案)
- [8. G6 门禁核对](#8-g6-门禁核对)
- [9. 变更记录](#9-变更记录)

## 1. 前置条件

- 本地 `ops/test.sh` 全绿（发布候选 commit 上执行）。
- 真实数据包已在**本地**完成 dry-run：`go run ./cmd/wedding data validate --dir <真实数据目录>` 通过，且 §2 的人工核对完成。
- staging 已 `git pull` 到发布候选 commit；当前时间避开避峰窗口（每天 00:00/06:00/12:00/18:00 ±10 分钟、06:27、23:50–24:00）。
- M2 已建立的 nginx `/wedding/` 接入与容器仍在运行（`grep -c wedding /data/conf/nginx/costra.cloud.conf` ≥ 1）。
- 所有命令以 root 在 staging 执行；非交互环境执行 `docker compose run` 时加 `-T`。

## 2. 真实数据包规范（含 canary 模板）

数据目录含六个契约 CSV（字段见 `docs/04-m0-decisions.md` §2），由真实数据与 **canary 虚构数据**共同组成——canary 用于 WG-604 冒烟，随真实数据同一事务导入（导入要求空库，无法事后补录）。

### 2.1 canary 模板（与 `ops/canary-smoke.sh` 默认值一一对应）

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

说明：组 A 一名成员带昵称（覆盖多名称冒烟）、一份礼品；组 B 两份礼品（覆盖多礼品显示）；KEY3 不绑定任何组，用于"组内第二密钥拒绝"用例。

### 2.2 导入前人工核对（开发计划 §6.3 必做项）

- [ ] 本名唯一无错别字；昵称归属正确且与任何本名/昵称不冲突；
- [ ] 每成员只属一组，每组至少一名可登录成员；每组至少一份礼品、顺序正确；
- [ ] 密钥严格 8 位 ASCII 数字、全局唯一、未被表格破坏前导零；
- [ ] 三类礼品数量与采购数量一致；激活码未被转成科学计数法/日期；
- [ ] 兑换链接全部 HTTPS；canary 模板已并入且密钥为 2026900x 虚构号段。

## 3. WG-603 真实数据导入

```sh
cd /opt/wedding && git pull   # 发布候选 commit

# 1. app.key 就位（首次放置时）：0600、属主 65534，离线副本异位保管
ls -l /data/wedding/secrets/app.key

# 2. 上传真实数据包（在开发机执行；权限 0600）
scp -r <真实数据目录>/ root@106.54.173.55:/data/wedding/import/

# 3. 构建新镜像（记录版本；不启动）
export WEDDING_VERSION="$(git describe --tags --always --dirty)" \
       WEDDING_COMMIT="$(git rev-parse --short HEAD)" \
       WEDDING_BUILD_TIME="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
docker compose -p wedding -f deploy/compose.yaml build

# 4. 停止 M2 占位容器
docker compose -p wedding -f deploy/compose.yaml stop app

# 5. schema 迁移（一次性容器；失败即停止，不进入下一步）
docker compose -p wedding -f deploy/compose.yaml run --rm app migrate

# 6. 上机环境复核数据包（dry-run；任一错误零写入）
docker compose -p wedding -f deploy/compose.yaml run --rm app data validate --dir /data/wedding/import

# 7. 单事务导入（要求空库；若 /data/wedding/data 已有非预期 wedding.db，
#    先停机备份并移走后再从第 5 步重来）
docker compose -p wedding -f deploy/compose.yaml run --rm app data import --dir /data/wedding/import

# 8. 脱敏数量核对：与离线事实源逐项比对（组/成员/登录名称/礼品/密钥计数）
docker compose -p wedding -f deploy/compose.yaml run --rm app status

# 9. 发布前回滚点：数据库备份 + nginx 配置备份
TS="$(date -u +%Y%m%d-%H%M%S)"
docker compose -p wedding -f deploy/compose.yaml run --rm app backup create \
  --output "/data/wedding/backups/wedding-pre-release-${TS}.db"
cp -p /data/conf/nginx/costra.cloud.conf "/data/wedding/nginx/costra.cloud.conf.${TS}.pre-m6"
```

## 4. WG-604 发布与冒烟

```sh
# 1. 启动正式服务并等待健康
docker compose -p wedding -f deploy/compose.yaml up -d
docker inspect -f '{{.State.Health.Status}}' wedding-app   # 应为 healthy

# 2. nginx 热加载（容器重建后重新解析容器地址；不修改配置则无需备份/编辑）
docker exec costra-nginx nginx -t && docker exec costra-nginx nginx -s reload

# 3. 真实 URL 全量验证（25 项检查；含 bootstrap/防护/安全头/Costra 回归）
ops/verify-public.sh

# 4. canary API 冒烟（22 项断言：首绑/多名称/同组/跨组/第二密钥/多礼品/reveal/Cookie 收编/退出）
ops/canary-smoke.sh
```

浏览器人工冒烟（目标浏览器矩阵，G5/G6 门禁）：

- [ ] iOS Safari：扫码打开 → canary 甲登录 → 逐份显示/复制/隐藏 → 退出；
- [ ] Android Chrome：同上；
- [ ] 微信 WebView：同上，并验证剪贴板受限时的长按选择复制降级；
- [ ] 窄屏无横向滚动；触控目标可点；页面无根路径 404 资源。

发布后复核：

- [ ] `docker stats --no-stream`：wedding-app 远低于 256M/0.5 CPU，Costra 各容器无异常；
- [ ] `docker logs --tail 100 wedding-app`：无明文姓名/密钥/礼品码，无异常报错；
- [ ] TLS 与响应头已由 `ops/verify-public.sh` §8 覆盖；
- [ ] canary 密钥默认保持 active 不停用（虚构数据无泄漏风险，供后续复冒烟；
      冒烟脚本幂等，已绑定状态下重跑依然全过）。如需关闭：`key revoke --ref KC01`（注意 revoke 后冒烟脚本第 2/7 步将按预期失败）。

明文源文件清理（导入核对无误后执行）：

```sh
# 源文件已转回项目所有者离线位置后，清空服务器导入目录
rm -rf /data/wedding/import/*
```

## 5. cron 启用与备份验证

```sh
# 安装独立 cron 单元（不动 Costra 的 cron 文件）
install -m 0644 -o root -g root deploy/wedding.cron /etc/cron.d/wedding

# 手动执行两次，验证产出与轮转逻辑（第二次不报错、各生成一份备份）
ops/backup.sh && ops/backup.sh
ls -lt /data/wedding/backups/ | head -5
tail -5 /data/wedding/backups/backup.log

# 确认 Costra cron 未被触碰
md5sum /etc/cron.d/costra-standby /etc/cron.d/costra-analyze
```

## 6. 执行记录（回填）

> 执行后回填本节，作为 G6 门禁与审计依据。

- 执行人 / 执行时间（UTC）：
- 发布版本：`version=______ commit=______`（schema 版本：1）
- 真实数据核对：组 __ / 成员 __ / 登录名称 __ / 礼品 __ / 密钥 __（与离线事实源一致：是/否）
- 发布前回滚点：数据库备份 `______`；nginx 备份 `______`
- `verify-public.sh`：__ 通过 / __ 失败
- `canary-smoke.sh`：__ 通过 / __ 失败
- 浏览器人工冒烟（iOS Safari / Android Chrome / 微信 WebView）：
- cron 安装时间与首次自动备份确认（下个 :44 后 `ls -lt /data/wedding/backups/`）：
- `docker stats` 摘要与 Costra 回归结论：
- 明文源文件清理确认：
- 异常与处理：

## 7. 回滚预案

| 场景 | 操作 |
|---|---|
| 数据预检/导入失败 | 未启动新服务，数据库保持导入前状态（单事务零写入）；修正离线数据后从 §3 第 6 步重来 |
| 导入后数量核对不符 | 不启动新服务；离线核对事实源，必要时 `rm /data/wedding/data/wedding.db*` 后从 §3 第 5 步重来（密钥摘要与礼品密文随库重建，app.key 不变即可） |
| 启动后应用异常 | `docker compose -p wedding -f deploy/compose.yaml stop app`；用 §3 第 9 步的 pre-release 备份恢复（停服 → 替换 `wedding.db` → 可选清空 SESSION → `up -d`）；镜像问题则 `git checkout <上一 commit>` 重新 build 后 `up -d` |
| 需整体回到占位页 | `git checkout <M2 commit> && docker compose -p wedding -f deploy/compose.yaml up -d --build`（业务下线，链路保留；真实数据库文件保留不动，回切前再次备份） |
| nginx 异常 | M6 不修改 nginx 配置；若发现 `/wedding/` 404（live conf 被 Costra 模板刷新覆盖），按 M2 runbook §4 重新插入片段并热加载 |

回滚后验证：`ops/verify-public.sh` 按当前形态核对（占位页形态时第 1/6/7 项预期失败）；Costra 回归项必须全过。

## 8. G6 门禁核对

- [ ] 产品验收 PA-01～PA-15 全部通过（PA-01/14 由 `verify-public.sh` 与浏览器冒烟覆盖，其余由 `ops/test.sh` 自动化与 `canary-smoke.sh` 覆盖）；
- [ ] 真实数据数量与离线事实源一致；明文源文件已移出服务器；
- [ ] canary 冒烟（API + 三目标浏览器）通过；
- [ ] cron 独立单元安装，手动备份两次成功，最近备份确认，72 份轮转逻辑就位；
- [ ] 发布版本、回滚点（数据库 + nginx 备份）已记录；
- [ ] Costra 原路径、容器与资源状态不变；
- [ ] WG-602 安全核对结论维持"未接受的核心安全问题为零"。

**全部通过后，才通知真实来宾使用（开发计划 G6）。**

## 9. 变更记录

| 版本 | 日期 | 变更内容 |
|---|---|---|
| v1.0 | 2026-09-12 | 完成 M6 上机手册：真实数据包 canary 模板、WG-603 导入序列、WG-604 发布与冒烟（verify-public/canary-smoke/浏览器矩阵）、cron 启用、回滚预案与 G6 门禁 |
