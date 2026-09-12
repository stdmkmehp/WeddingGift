<!--
文档名称: WeddingGift M2 真实 URL 访问链路操作手册
文档类型: 运维操作手册 / 里程碑记录
版本: v1.3
日期: 2026-09-12
状态: M2 已执行并验收通过（G2 门禁通过），里程碑关闭
依赖文档: docs/03-development-plan.md（M2/WG-201~203）, docs/staging-machine-brief.md（§3/§5/§6/§8）, docs/02-technical-architecture.md（§12）
-->

# WeddingGift M2：真实 URL 访问链路操作手册

本手册落地开发计划 M2（WG-201～WG-203）：在共享腾讯云机器上部署**无真实数据**的占位主页容器，并把 `https://stg.costra.cloud/wedding/` 接入现有 costra-nginx，验证「浏览器 → HTTPS → nginx → Docker 网络 → wedding-app」完整链路。

红线提醒：全程只读/插入式操作 Costra 资源；禁止重启 `costra-nginx`；不创建 cron（cron 属 M3/WG-305）；不导入任何真实姓名、密钥或礼品码。

## 目录

- [1. 前置条件](#1-前置条件)
- [2. WG-201 只读自检](#2-wg-201-只读自检)
- [3. WG-202 部署占位主页容器](#3-wg-202-部署占位主页容器)
- [4. WG-203 nginx 接入与真实 URL 验证](#4-wg-203-nginx-接入与真实-url-验证)
- [5. 执行记录（回填）](#5-执行记录回填)
- [6. 回滚预案](#6-回滚预案)
- [7. G2 门禁核对](#7-g2-门禁核对)

## 1. 前置条件

- staging 已 `git clone`（或 `git pull` 到最新）本仓库于 `/opt/wedding/`。
- M1 已在 staging 通过容器冒烟验证（镜像构建、健康检查、主页与 `/healthz` 访问全绿）。
- 当前时间避开避峰窗口：每天 00:00/06:00/12:00/18:00（±10 分钟）、06:27、23:50–24:00。
- 所有命令以 root 在 staging 执行；脚本位于 `/opt/wedding/ops/`。

## 2. WG-201 只读自检

```sh
ops/staging-selfcheck.sh
```

全部为只读命令，结果自动保存到 `ops/records/selfcheck-<UTC时间戳>.log`（不进 Git）。逐项核对：

| 核对项 | 通过标准 |
|---|---|
| CPU / 内存 / `/data` 磁盘 | 余量足以再容纳 256M / 0.5 CPU |
| `docker stats` | 三栈占用与说明书一致，无异常吃满 |
| `ss -tlnp` | 无 3000 等计划外端口冲突（应用不发布端口，仅确认习惯） |
| Docker 网络 | `costra_default` 存在，网段非 VPC 172.17 段 |
| cron | `costra-standby`/`costra-analyze` 原样；`/etc/cron.d/wedding` 尚不存在 |
| nginx live conf | `grep -c wedding` 为 0；兜底 `location / { return 404; }` 恰好 1 处 |

任一不达标：停止，先排查，不进入下一步。

## 3. WG-202 部署占位主页容器

```sh
ops/deploy-app.sh
```

脚本自动完成：

1. 校验 `costra_default` 网络存在并提示避峰窗口；
2. 创建 `/data/wedding/{data,backups,secrets,import,nginx}`（全部 0700；`data`/`backups`/`secrets` 属主为容器用户 65534——M3 起应用需写库/备份并读取 `secrets/app.key`；`import`/`nginx` 保持 root）；
3. 注入版本信息（git describe / commit / UTC 构建时间）并 `docker compose -p wedding -f deploy/compose.yaml up -d --build`；
4. 等待 `wedding-app` 健康，从 `costra-nginx` 视角验证容器名可解析；
5. `docker stats --no-stream` 复核整机资源。

通过标准：容器 healthy；`wedding-app` 内存远低于 256M；Costra 容器全部 `running` 且未被修改。

## 4. WG-203 nginx 接入与真实 URL 验证

> **谨慎原则**：修改的对象是 Costra 项目的 live nginx 配置，本步骤**全部由人工按说明书 §3/§6 纪律执行**，不使用自动化脚本。插入片段以仓库副本 `deploy/nginx-wedding.conf` 为准。

操作序列（对应说明书 §3.2 五条纪律，逐条人工执行）：

```sh
# 1. 先备份再改：备份 live conf 到自有目录（目录收束约定，替代 §6.2 示例中的 /data/wedding/ 根）
cp -p /data/conf/nginx/costra.cloud.conf "/data/wedding/nginx/costra.cloud.conf.$(date -u +%Y%m%d-%H%M%S).bak"
# 首次插入前另存一份原始备份，供三个月后拆除时对照（§7）
cp -p /data/conf/nginx/costra.cloud.conf /data/wedding/nginx/costra.cloud.conf.orig.bak

# 2. 只增不改：用编辑器打开 /data/conf/nginx/costra.cloud.conf，
#    在 443 server block 的兜底 location / { return 404; } 正上方，
#    原样插入 deploy/nginx-wedding.conf 的全部内容
#    （首尾 BEGIN/END 标记之间为整块范围，便于日后干净删除，与说明书 §6.2 片段完全一致）。
#    文件其余任何一行都不许动；插入前确认兜底 location 恰好 1 处：
grep -nE 'location / \{[[:space:]]*return 404' /data/conf/nginx/costra.cloud.conf

# 3. 改后验证（零停机热加载，禁止 restart 或 compose up 重建 costra-nginx）
docker exec costra-nginx nginx -t && docker exec costra-nginx nginx -s reload

# 4. 自留副本：把改后完整文件存一份到自有目录
cp -p /data/conf/nginx/costra.cloud.conf "/data/wedding/nginx/costra.cloud.conf.$(date -u +%Y%m%d-%H%M%S).after"

# 5. 真实 URL + Costra 回归验证
ops/verify-public.sh
```

若 `nginx -t` 失败：**不要 reload**，立即用第 1 步的备份恢复并重新校验：

```sh
cp -p <备份文件> /data/conf/nginx/costra.cloud.conf
docker exec costra-nginx nginx -t
```

`verify-public.sh` 覆盖：主页 200 且为链路验证页、`/wedding` 301、相对静态资源 200、`/wedding/healthz` ok、页面无根路径绝对引用、Costra `/healthz` 与兜底 404 回归、staging 本机容器与资源回归。在任意能访问公网的机器都可再跑一遍复核。

## 5. 执行记录（回填）

> 执行后回填本节，作为 G2 门禁与后续审计依据。

- 执行人 / 执行时间（UTC）：
- 自检记录文件：`ops/records/selfcheck-________.log`
- 镜像/版本：`version=______ commit=______`
- nginx 备份：`/data/wedding/nginx/costra.cloud.conf.________.bak`
- `verify-public.sh` 结果：____ 通过 / ____ 失败
- 资源占用峰值（docker stats 摘要）：
- Costra 回归结论：
- 异常与处理：

## 6. 回滚预案

nginx 相关回滚同样**人工执行**，两种方法任选（对应说明书 §7）：

| 场景 | 人工操作 |
|---|---|
| nginx 校验失败 | 不 reload，用改前备份恢复后重新 `nginx -t`（命令见 §4 末尾） |
| 插入后需撤销 wedding 块 | 方法一：编辑 live conf，按 `# ========= BEGIN wedding` 到 `# ========= END wedding` 标记整块删除，`nginx -t` 后 reload。方法二：先 `diff <备份> <live>` 确认差异仅为 wedding 片段（Costra 侧无其他改动），再用备份恢复，`nginx -t` 后 reload |
| 应用层回滚 | `docker compose -p wedding -f deploy/compose.yaml down`（镜像仍在，可重新 up） |
| 完全拆除（回到 M2 前状态） | 先按上表撤销 wedding 块并 reload，再 `compose down`，最后按需 `rm -rf /data/wedding` |

回滚后验证：`curl -sI https://stg.costra.cloud/wedding/` 应返回 404（Costra 兜底），Costra 各路径正常。

## 7. G2 门禁核对

- [x] 真实 URL `https://stg.costra.cloud/wedding/`、相对静态资源与 `/wedding/healthz` 均可访问；
- [x] `/wedding` 301 到 `/wedding/`；页面无域名根路径依赖；
- [x] nginx 仅插入允许的 wedding 块，备份与片段副本就位，回滚步骤实测可用；
- [x] Costra 原有关键路径、容器与资源状态无变化；
- [x] 页面与容器不含任何真实姓名、密钥或礼品数据；
- [x] 第 5 节执行记录已回填。

**验收结论：2026-09-12 M2 验收通过（G2 门禁全部满足），真实 URL 访问链路打通。**

后续进入 M3（数据、加密与管理能力，本地开发为主）。

## 8. 变更记录

| 版本 | 日期 | 变更内容 |
|---|---|---|
| v1.0 | 2026-09-11 | 完成 M2 操作手册：WG-201～203 脚本化步骤、执行记录模板、回滚预案与 G2 门禁核对 |
| v1.1 | 2026-09-12 | nginx 接入与回滚改为全人工操作（对象为 Costra live conf，谨慎原则），删除 nginx-install/nginx-rollback 脚本；不再保留本地 compose 冒烟（compose.local.yaml/smoke-local.sh），容器操作统一在线上机器执行 |
| v1.2 | 2026-09-12 | 修正 WG-202 目录属主描述：`secrets` 属主同为容器用户 65534（root:root 0700 目录会阻止容器内非 root 进程读取 `app.key`），与 `ops/deploy-app.sh` 修复同步 |
| v1.3 | 2026-09-12 | M2 验收通过：G2 门禁核对全部勾选，里程碑关闭，进入 M3 |
