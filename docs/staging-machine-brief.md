# Staging 机器共享使用说明书（新工程接入必读）

> **本文档用途**：一台腾讯云 Lighthouse 机器上已运行一个生产级项目（Costra 记账小程序）的 staging 环境、生产数据库热备副本和数据分析栈。现计划在同一台机器上临时运行一个**全新的、与 Costra 完全无关的小型网页服务**（约 3 个月，面向少数朋友的卡片密钥登录 + 礼品激活码展示）。
> 本文档假设读者**完全不了解 Costra**，读完即可掌握：机器现状、Costra 对机器的占用情况、新工程开发与部署必须遵守的规则。
> 文档信息来源：Costra 仓库 `infra/` 与 `docs/ops/` 的事实源文件（2026-09 时点）。
>
> **2026-09-11 接入方式已定**：不开新域名、不签新证书，新站点挂在 **`https://stg.costra.cloud/wedding/`** 路径下，由现有 nginx 反代到新应用容器。相关规则见 §3/§5/§6。

---

## 0. TL;DR（30 秒版）

- 机器已有 **3 套 Docker Compose 栈并存**，且**持有生产数据库的完整热备副本**——它是生产环境的灾难恢复源，稳定性优先于一切。
- 新站点接入方式（已定）：在现有 nginx 的 live 配置里**插入一段 `location /wedding/` 块**反代到你的容器。**不开安全组新端口、不加 DNS 记录、不签新证书**。该配置有严格的编辑纪律，见 §3 与 §6。
- **绝对禁止使用机器上已有的 PostgreSQL（5432）和 Redis（6379）**：它们属于 staging 环境，**每天 06:27 会被生产数据整体覆盖（DB 恢复 + Redis FLUSHALL）**。你的数据必须放自己的容器/卷。
- 你的应用必须容器化、带内存上限（≤256M）、日志限大小、只挂自己的目录。
- 出口带宽只有 **1Mbps（约 125KB/s）**，页面和静态资源必须轻量。
- 应用跑在 `/wedding/` 路径前缀下，**重定向、链接、Cookie 都要按前缀规则处理**（§6.3），这是路径方案特有的开发约束。

---

## 1. 机器基本情况

| 项 | 值 |
|---|---|
| 云厂商 / 机型 | 腾讯云 Lighthouse（轻量应用服务器） |
| 公网 IP | `106.54.173.55` |
| 已绑域名 | `stg.costra.cloud`（A 记录 → 上述 IP，TTL 300）——**新站点复用此域名，走 `/wedding/` 路径** |
| 主机名 | `costra-prod-slave` |
| 内网 IP | `172.17.16.4`（与 Costra 生产机 `172.17.0.3` 同 VPC 不同子网） |
| 出口带宽 | **1Mbps（约 125KB/s）** |
| 操作系统 | Tencent Linux（tlinux 3.3 系列） |
| 容器运行时 | Docker 24.x + docker compose v2 |
| SSH | `root@106.54.173.55` |
| CPU / 内存 / 磁盘规格 | **未实测记录**——首次上机必须先跑 §8 的自检命令确认余量 |

**Docker 网络现状**（重要：VPC 网段是 172.17.0.0/16，与 docker 默认网段重合，现有网络已刻意避让，新建网络交给 docker 自动分配即可，**不要手工指定网段**）：

| Docker 网络 | 网段 | 归属 |
|---|---|---|
| `bridge`（docker0） | 172.18.x.x | 默认 |
| `costra_default` | 172.19.x.x | Costra staging 栈（**新应用将以外部网络方式加入它**，见 §6） |
| `costra-analyze_default` | 172.20.x.x | Costra 分析栈 |

**安全组（云防火墙）**：仅放行必要端口（22/80/443 等）。Costra 数据库端口仅绑 `127.0.0.1`，不对公网开放。**新工程不得申请在安全组放行任何新端口**——所有外部流量从现有 nginx 的 443 进入。

---

## 2. Costra 对机器的占用全景

机器上共存 **3 套互相隔离的 Docker Compose 栈**。理解这张表是遵守规则的前提：

| 资源 | ① staging 栈 | ② PG 从库（生产热备） | ③ 分析栈 |
|---|---|---|---|
| 用途 | Costra 测试环境（完整业务栈） | **生产数据库的 hot standby 只读副本 + 备份产出** | 线上数据分析（按需运行） |
| compose project | `costra` | `costra-standby` | `costra-analyze` |
| 容器名 | `costra-postgres` · `costra-redis` · `costra-server` · `costra-nginx` | `costra-standby-pg` | `costra-analyze-pg` · `costra-analyze-node` |
| 运行模式 | **常驻**（restart: unless-stopped） | **常驻**（restart: unless-stopped） | **按需启停**（restart: "no"，仅分析窗口拉起，跑完即停） |
| 宿主机端口 | PG `127.0.0.1:5432` · Redis `127.0.0.1:6379` · server 仅容器内网 8080 · **nginx 独占 80/443（公网）** | `127.0.0.1:15433` + 内网 `15433` | `127.0.0.1:15432` |
| 内存 limits | 合计 **3.0G**（server 1G + redis 768M + pg 1G + nginx 256M） | **768M** | PG 512M + node 256M（仅运行时占用） |
| 数据/日志目录 | `/data/pgsql` · `/data/redis` · `/data/uploads` · `/data/exports` · `/data/import-staging` · `/data/logs/{postgres,redis,server,nginx}` | `/data/pgsql-standby` · `/data/logs/standby` · 备份落 `/data/slave/backups` | named volume · `/opt/costra-analyze`（部署目录）· `/tmp/costra-analyze-*` |
| 配置文件 | `/data/conf/server.env` | `/data/conf/standby.env`（含复制凭据） | `/data/conf/analytics.env` |

**已占用端口速查**（新应用自选容器内端口，不发布到宿主机）：

```
22     SSH
80/443 costra-nginx（公网入口，独占）
5432   Costra staging PG（127.0.0.1）
6379   Costra staging Redis（127.0.0.1）
15432  分析栈 PG（127.0.0.1）
15433  PG 从库（127.0.0.1 + 内网）
```

**URL 路径占用速查**（`stg.costra.cloud` 下已被 Costra 占用的前缀，新应用**禁止冲突**）：

```
/v1/          Costra 业务 API（反代到 costra-server）
/uploads/     Costra 用户上传静态文件
/healthz      Costra 健康检查
/.well-known/ ACME 证书挑战
/wedding/     ← 你的应用（本文档预留）
其他一切       兜底 return 404
```

**关键认知**：
- ②号栈的 `/data/pgsql-standby` 里是**生产环境全量数据**。生产库宕机时靠它恢复。它是全机最敏感的资产。
- ①号栈（staging）的数据**不被珍视**：每天 06:27 会被生产备份整体刷新（见 §4），它的 PG/Redis 对任何第三方应用都不可用作存储。
- ③号栈每天定时拉起跑分析，是整机内存/CPU 峰值时段。

---

## 3. nginx 接入点：live 配置的编辑纪律（路径方案核心）

新站点不开新域名，而是**复用 `stg.costra.cloud` 的既有 HTTPS server block**，在其中插入一段 `location /wedding/`。要编辑的文件正是 Costra 的 live nginx 配置，因此本节是全文档最需要纪律的部分。

### 3.1 事实背景

- 公网入口是容器 **`costra-nginx`**（`nginx:1.27-alpine`），监听 80/443，以只读方式挂载宿主机 conf.d 目录：`/data/conf/nginx/` → 容器内 `/etc/nginx/conf.d/`（**宿主机可写**）。
- live 配置文件：**`/data/conf/nginx/costra.cloud.conf`**（`stg.costra.cloud` 的 80 + 443 两个 server block 都在里面）。
- 该文件由 Costra 部署脚本从仓库模板生成，但**只在文件缺失时才复制**（`infra/scripts/05-start-stack.sh`：live 已存在则跳过，保护运行时配置）。所以：
  - ✅ **日常 Costra 重新部署（redeploy）不会冲掉你的手改**；
  - ❌ **但 Costra 模板更新时的标准运维操作是"删除 live conf + 重跑脚本"或手动 cp 模板覆盖——一旦发生，你插入的 location 块静默消失，站点立刻 404**。

### 3.2 编辑纪律（五条，缺一不可）

1. **只增不改**：仅在 443 server block 的兜底 `location / { return 404; }` **上方**插入 §6.2 给出的两个 location 块。文件其余任何一行都不许动（包括 80 端口 block、SSL 配置、其他 location、日志配置）。
2. **先备份再改**：编辑前 `cp /data/conf/nginx/costra.cloud.conf /data/<你的项目>/costra.cloud.conf.orig.bak`。
3. **改后验证**：`docker exec costra-nginx nginx -t && docker exec costra-nginx nginx -s reload`（零停机热加载）。**绝不用 `docker restart` 或 `docker compose up` 重建 costra-nginx**——那会让 Costra staging 短暂断网。
4. **自留副本**：把插入的 location 块片段和改后完整文件都保存到自己的项目目录（§6.2 的片段建议直接提交进你的工程仓库）。
5. **被冲掉后的恢复**：若发现站点突然全 404，第一嫌疑就是 live conf 被 Costra 模板刷新覆盖。确认方法：`grep -c wedding /data/conf/nginx/costra.cloud.conf`（返回 0 即被覆盖）。恢复 = 重新插入块 + reload。Costra 每次 nginx 相关变更后，养成 `curl -sI https://stg.costra.cloud/wedding/` 验证一次的习惯。

### 3.3 已知接受的偏差

在 `stg.costra.cloud` 下挂第三方应用，意味着 Costra 的 staging 环境 nginx 比 prod 多出一块配置，破坏了其"staging 与 prod 仅 3 处差异"的对齐原则。这是**项目所有者已知情接受的临时偏差**，期限 3 个月，到期按 §7 拆除恢复。代价是：Costra 侧做 nginx diff 审计时会看到这块多出来的配置——保持插入块的注释标记完整（§6.2 片段自带首行注释），让 Costra 维护者一眼识别这是外来块、勿动。

---

## 4. 机器上的定时任务（避开这些窗口做重型操作）

| 时间 | 任务 | 影响 |
|---|---|---|
| 每小时 `:47` | 主从复制健康检查 | 秒级，可忽略 |
| **每天 00:00 / 06:00 / 12:00 / 18:00** | 从库 `pg_dump` 全量备份 → `/data/slave/backups` | CPU/IO 峰值，分钟级 |
| **每天 06:27** | **用生产备份整体刷新 staging：DB 恢复 + avatars 替换 + Redis FLUSHALL** | staging 栈短暂扰动；**这就是不能用 staging PG/Redis 存数据的原因** |
| **每天 23:59** | 分析栈日报（两个容器拉起，峰值约 768M 内存） | 分钟级，整机内存峰值时段 |
| 每周日 23:50 | 分析栈周报 | 同上 |
| 每日（随机时刻） | certbot renew | 秒级 |
| **每小时 `:44`** | WeddingGift 本机备份（`/etc/cron.d/wedding`，见 §5.3(4)） | 秒级，SQLite online backup，文件极小 |

内存叠加模型：staging 常驻 3.0G + 从库常驻 768M + 分析窗口峰值 768M。你的应用必须在上述峰值叠加时仍然活得下来，所以内存上限要小（§5）。

WeddingGift 备份选 **每小时 `:44`** 是为避开整点（00/06/12/18 从库 pg_dump）、`:47`（主从健康检查）、06:27（staging 刷新）和 23:5x（分析窗口）等既有窗口；`:44` 与 `:47` 相差 3 分钟且 WeddingGift 备份为秒级 SQLite online backup，不与主从健康检查产生实质竞争。

---

## 5. 新工程开发规则

### 5.1 红线（违反会影响生产环境，零容忍）

1. **不得触碰生产数据副本**：禁止挂载、读取、写入 `/data/pgsql-standby`、`/data/slave/backups`、`/data/conf/standby.env`。
2. **禁止使用 Costra 的任何有状态组件存数据**：staging PG（5432）、staging Redis（6379）、从库 PG（15433）、分析栈 PG（15432）一律禁用。WeddingGift 已确定使用自己的 SQLite 文件和 `/data/wedding/` 自有目录，不增加 PostgreSQL 容器。
3. **不得修改或删除这些文件/目录**：
   - `/data/conf/nginx/costra.cloud.conf` —— **唯一例外：允许按 §3/§6 的纪律插入 `/wedding/` location 块**，其余内容一行不许动；
   - `/data/conf/server.env`、`/data/conf/standby.env`、`/data/conf/analytics.env`；
   - `/etc/cron.d/costra-standby`、`/etc/cron.d/costra-analyze` —— **只读勿动；WeddingGift 的定时任务必须放在独立文件 `/etc/cron.d/wedding` 中，不得写入或修改这两个 Costra cron 文件**；
   - `/data` 下 Costra 既有的一切目录（`/data/pgsql` `/data/redis` `/data/uploads` `/data/exports` `/data/import-staging` `/data/logs/*` `/data/slave` 等）。
4. **不得在安全组放行新端口**；新应用不得 `ports:` 发布任何端口到宿主机（只 `expose` 给 docker 内网）。
5. **容器隔离**：禁止 `network_mode: host`、`privileged: true`、`docker.sock` 挂载；volumes 只挂自己的目录。
6. **URL 前缀纪律**：你的应用只占 `/wedding/`。不得占用或探测 `/v1/`、`/uploads/`、`/healthz`、`/.well-known/` 等 Costra 路径；不得在应用内发起对这些路径的写操作。

### 5.2 必做（资源与运维纪律）

1. **独立 compose project**：`name:` 用项目自己的前缀（不要含 `costra`），`container_name` 同理，独立 named volume 或独立宿主目录（建议放 `/data/<你的项目>/`，与 Costra 目录平级）。
2. **内存上限**：WeddingGift 单应用容器的 `deploy.resources.limits.memory` ≤ 256M；CPU limits 按需 ≤ 0.5。项目使用内嵌 SQLite，不增加数据库容器。
3. **日志限大小**：每个容器显式声明 json-file 上限（docker 默认不限，会吃满磁盘）：
   ```yaml
   logging:
     driver: json-file
     options:
       max-size: "50m"
       max-file: "3"
   ```
4. **`restart: unless-stopped`**，宿主机重启后能自愈。
5. **上机先自检**（§8），确认内存与 `/data` 磁盘余量后再定 limits。
6. **部署/重启等操作避开** §4 的峰值窗口（整点备份、06:27 刷新、23:50–24:00 分析窗口）。
7. 跑起来后执行一次 `docker stats --no-stream` 复核整机占用，确认峰值叠加时不触顶。

### 5.3 建议（体验与合规）

1. 出口 1Mbps：页面总体积控制在百 KB 级，开 gzip，不放图片、视频等大资产；确有需要时先重新评估带宽，不预设对象存储或 CDN。
2. WeddingGift 使用“本名或配置昵称 + 8 位卡片密钥”登录；卡片密钥和登录后展示的礼品激活码都属于敏感数据，使用现有 HTTPS，避免写入日志，并对登录接口做基础限流。
3. **合规**：`stg.costra.cloud` 共用主备案（沪ICP备2026017253号-2，现有 server block 已自动带 `X-ICP-Filing` 响应头）。对外可访问的网页按备案公示规范，应在页面底部悬挂备案号并链接 `https://beian.miit.gov.cn/`；网页内容与备案主体性质的一致性、收集登录信息的隐私口径，由服务所有者自行把关。临时 3 个月用途，建议控制传播范围、不公开引流。
4. **WeddingGift 备份边界已定**：只使用宿主机 cron 和 `/data/wedding/backups/` 本地目录，不接 COS、不做主从或异机恢复。宿主机 cron 以独立文件 `/etc/cron.d/wedding` 落地（不写 Costra 的 cron 文件），默认 **每小时 `:44`** 触发一次 SQLite online backup 并 **保留最近 72 份（≈3 天）**，具体时间点与份数可在上线前调整；不得写入 `/data/slave/backups` 或分析栈的 inbox 目录。
5. **WeddingGift 目录收束**：所有 WeddingGift 产出必须集中在两处固定根目录，不散落到其他位置——`/root/WeddingGift/`（代码、compose、部署与备份脚本、cron 片段、nginx 备份片段的仓库副本）与 `/data/wedding/`（运行时数据 `data/`、本机备份 `backups/`、应用秘密 `secrets/`、临时导入 `import/`、nginx 改前备份 `nginx/`）。应用运行日志走 Docker json-file（§5.2.3），不额外落地散乱日志文件。拆除时只清理这两个根目录与 `/etc/cron.d/wedding`（§7）。

---

## 6. 推荐接入拓扑与步骤

```
朋友浏览器 ──HTTPS──▶ stg.costra.cloud/wedding/（costra-nginx 既有 443 server block）
                          │ location /wedding/ → proxy_pass（抹掉前缀）
                          ▼
                  http://<你的容器名>:<端口>/（仅 docker 内网）
                          │
                  你的应用容器（独立 compose project，加入 costra_default 网络）
                  数据：SQLite 文件 + WeddingGift 自有目录
```

### 6.1 应用 compose（要点）

```yaml
name: wedding          # 项目前缀自定，但不得含 costra

services:
  app:
    # image / build 自定
    container_name: wedding-app
    restart: unless-stopped
    expose:
      - "3000"          # 应用监听端口自定；只 expose，禁止 ports: 发布到宿主机
    volumes:
      - /data/wedding/data:/data/wedding/data           # SQLite 数据
      - /data/wedding/backups:/data/wedding/backups     # 本机 cron 备份输出
      - /data/wedding/secrets:/data/wedding/secrets:ro  # 应用秘密只读
    networks:
      - costra_default  # 加入 Costra staging 栈的既有网络（只读复用，不修改它）
    deploy:
      resources:
        limits:
          memory: 256M
          cpus: '0.5'
    logging:
      driver: json-file
      options:
        max-size: "50m"
        max-file: "3"

networks:
  costra_default:
    external: true      # 引用已存在的网络，compose 不会创建/修改它
```

### 6.2 nginx 插入块（唯一允许的手改）

在 `/data/conf/nginx/costra.cloud.conf` 的 **443 server block** 内、兜底 `location / { return 404; }` 的**正上方**，原样插入：

```nginx
    # ========= BEGIN wedding 临时站点（2026-09-11 插入 · 外来块 · 约3个月后随 §7 拆除 · Costra 维护者勿动） =========
    location = /wedding {
        return 301 /wedding/;
    }

    location /wedding/ {
        # proxy_pass 带尾斜杠 = 抹掉前缀：/wedding/api/auth/login → 应用看到 /api/auth/login
        proxy_pass http://wedding-app:3000/;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_connect_timeout 5s;
        proxy_read_timeout 60s;
        client_max_body_size 2m;
        # Cookie 路径收编：应用若 Set-Cookie Path=/，强制改写为 /wedding/，防与 Costra 会话互串
        proxy_cookie_path / /wedding/;
    }
    # ========= END wedding 临时站点（2026-09-11 插入 · 外来块 · 约3个月后随 §7 拆除 · Costra 维护者勿动） =========
```

> nginx 解析器对缩进/空白完全无感（指令靠 `;` 与 `{}` 界定）；要求块整体 4 空格缩进、内部 8 空格，是为了与 live 文件既有惯例（如 `/v1/` 块）保持一致，便于 Costra 维护者 diff 审计和三个月后按 BEGIN/END 标记干净拆除。

> 容器名/端口以你的 compose 为准并同步改这两处。nginx 按容器名解析是因为应用容器加入了 `costra_default` 网络。

**操作序列**：

```bash
# 1. 备份
cp /data/conf/nginx/costra.cloud.conf /data/wedding/costra.cloud.conf.orig.bak

# 2. 编辑插入上述块（编辑器自选）

# 3. 校验 + 热加载（零停机）
docker exec costra-nginx nginx -t && docker exec costra-nginx nginx -s reload

# 4. 验证
curl -sI https://stg.costra.cloud/wedding/
```

### 6.3 应用开发的路径前缀规则（路径方案特有）

proxy_pass 带尾斜杠会**抹掉前缀**，应用内部看到的请求路径是 `/`（不是 `/wedding/`）。由此产生四条开发约束：

1. **所有重定向和站内链接用相对路径**（如 `Location: login`、`href="assets/app.css"`），**禁止绝对路径**（`/login` 会被浏览器解析到 `stg.costra.cloud/login` → 命中 Costra 兜底 404）。
2. **静态资源引用同理**：用相对路径，或显式以 `/wedding/` 开头的完整路径。
3. **Cookie**：名称加项目前缀（如 `wedding_session`），避免与 Costra staging 的会话 Cookie 撞名；路径已由 nginx `proxy_cookie_path` 收编到 `/wedding/`，应用侧无需再设 Path（若框架强设 Path，设 `/wedding`）。
4. **如果所用框架必须感知 base path**（如某些 SSR 框架）：也可把 `proxy_pass` 改为**不带尾斜杠**（`proxy_pass http://wedding-app:3000;`），应用将看到完整的 `/wedding/...` 路径，框架配 base path `/wedding` 即可。二选一，不要混用。

---

## 7. 三个月后拆除清单

建的时候按此边界建，拆的时候就是干净的原路退回：

1. 从 `/data/conf/nginx/costra.cloud.conf` **删除 §6.2 插入的内容**（`# ========= BEGIN wedding` 到 `# ========= END wedding` 两行注释之间的全部，含这两行本身），然后 `docker exec costra-nginx nginx -t && docker exec costra-nginx nginx -s reload`。若期间 Costra 未动过该文件，也可直接用 §3.2 的备份恢复——**恢复前先 diff 确认 Costra 侧无其他改动**；
2. **删除 WeddingGift 的 cron 单元** `rm -f /etc/cron.d/wedding`（Costra 的 `costra-standby`、`costra-analyze` 两个 cron 文件不动）；
3. `docker compose -p wedding down -v`；
4. 删 `/data/wedding/` 与 `/root/WeddingGift/`（WeddingGift 全部数据、备份、秘密、代码与 compose 目录）；
5. 无 DNS、无证书需要清理（路径方案天然无此两项）。

全程对 Costra 三栈零接触，live conf 恢复插入前原貌。

---

## 8. 首次上机自检命令（把结果记录下来再定资源上限）

```bash
# 机器规格与余量
nproc; free -h; df -h /data

# 三栈实时占用（含 limits 对比）
docker stats --no-stream

# 端口占用全景（确认不与 22/80/443/5432/6379/15432/15433 冲突）
ss -tlnp

# Docker 网络（确认 costra_default 存在；确认没有占用 VPC 的 172.17 段）
docker network ls -q | xargs -r docker network inspect \
  -f '{{.Name}} {{range .IPAM.Config}}{{.Subnet}} {{end}}'

# 现有定时任务（只读，勿改）
cat /etc/cron.d/costra-standby /etc/cron.d/costra-analyze

# 确认 WeddingGift cron 单元尚未创建（首次上机应为空/不存在）
ls -l /etc/cron.d/wedding 2>/dev/null || echo "no wedding cron yet"

# nginx live 配置现状（只读，确认 wedding 块尚未存在）
grep -n 'location' /data/conf/nginx/costra.cloud.conf
```

---

## 附录 A：关键路径速查

| 用途 | 路径 |
|---|---|
| nginx conf.d 目录 | `/data/conf/nginx/` |
| Costra staging live 配置（**仅允许按 §3/§6 插入 wedding 块**） | `/data/conf/nginx/costra.cloud.conf` |
| Costra 各栈 env（**勿动**） | `/data/conf/{server,standby,analytics}.env` |
| 生产数据副本（**勿碰**） | `/data/pgsql-standby`、`/data/slave/backups` |
| Costra 项目代码 | `/root/Costra` |
| 新应用建议落地目录 | `/root/WeddingGift/`（代码 / compose / 脚本 / cron 片段 / nginx 备份片段）· `/data/wedding/`（运行时数据、备份、秘密、导入、nginx 改前备份） |
| WeddingGift cron 单元（**独立文件，勿写 Costra cron**） | `/etc/cron.d/wedding` |

## 附录 B：术语

- **Costra**：本机已有的家庭记账小程序项目（微信小程序 + Node/Fastify + PostgreSQL + Redis）。
- **staging 栈**：Costra 的测试环境，数据每日被生产覆盖，**不是**给你用的"现成数据库"。
- **PG 从库 / hot standby**：生产 PostgreSQL 的流复制只读副本，是全机最敏感资产。
- **分析栈**：对生产备份做离线数据分析的按需容器组，每天深夜短暂运行。
- **live conf**：`/data/conf/nginx/costra.cloud.conf`，Costra 部署脚本只在它缺失时才从仓库模板复制，日常部署不覆盖；但模板更新时的强制刷新会连你的手改一起冲掉（§3）。
