# WeddingGift 虚构追加数据包（`data append` 演练样例）

本目录是 `wedding data append`（数据契约 §2.8，上线后纯追加）的虚构样例，
用于自动化测试与追加流程演练。**全部为虚构数据**，可安全提交进 Git。

前提：数据库已用 `testdata/fixtures/` 完成首次导入（本包的引用目标都在其中）。

## 覆盖矩阵

| 场景 | 数据 |
|---|---|
| 新同行组（追加一个家庭） | G101（周斌 & 吴倩 一家，2 名可登录成员） |
| 新礼品类型 | cake_card 蛋糕卡（`theme_variant` 留空，复用现有卡片视觉，不改前端） |
| 新礼品发给新组 | GI101（蛋糕卡，G101 ordinal 1） |
| 给既有组补成员 | M103 加入 G001（can_login=true，登录名「卷毛哥哥」与显示名一致） |
| 给既有组补发礼品 | GI102（电影券发 G001，ordinal 3；G001 库内仅占 1）、GI103（蛋糕卡发 G002，ordinal 4 避开库内已占的 1–3） |
| 引用既有礼品类型 | GI102 引用既有类型 movie_voucher；GI103 用本批新类型 cake_card——新类型同样可发给既有组 |

说明：ordinal 只需在组内唯一（无连续性要求）。「既有成员仅允许加 nickname」这条规则
不再由本包演示，改由 `TestAppendConflictRollback/既有成员追加本名` 覆盖。

## 本地演练

```sh
# 1. 准备临时库并完成首次导入（沿用主 fixtures）
export WEDDING_DB_PATH="$(mktemp -d)/wedding.db"
export WEDDING_APP_KEY_PATH="$(pwd)/testdata/fixtures/../fixtures-app.key"  # 任意 ≥32 字节本地 app.key
go run ./cmd/wedding migrate
go run ./cmd/wedding data import --dir testdata/fixtures

# 2. 追加演练：先 dry-run 复核（零写入），再实跑，最后 status 核对
go run ./cmd/wedding data append --dir testdata/fixtures-append --dry-run
go run ./cmd/wedding data append --dir testdata/fixtures-append
go run ./cmd/wedding status
```

正式环境在线追加流程见 `docs/06-m6-runbook.md` §6.4（`ops/append-data.sh`）。

## 安全约束

- 与主 fixtures 相同：真实追加数据绝不进入本目录或 Git；
  `code_plaintext`/`secret_plaintext` 仅为虚构值。
- 本目录不提供 `access_keys.csv`：追加模式不支持新增密钥（放置数据行会被拒绝）。
