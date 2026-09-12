// Command wedding 是 WeddingGift 的单二进制入口。
//
// 子命令（技术架构 §11）：
//
//	wedding serve                          启动 HTTP 服务
//	wedding healthz-check                  向本地 /healthz 发起一次健康检查（Docker healthcheck 用）
//	wedding version                        打印构建版本信息
//	wedding migrate                        应用数据库 migration（发布时显式执行）
//	wedding data validate --dir <dir>      只读校验数据文件并输出脱敏核对报告
//	wedding data import --dir <dir>        校验通过后单事务导入（要求空库，任一错误全部回滚）
//	wedding status                         输出脱敏统计（内部编号与数量，不含明文）
//	wedding key revoke --ref <card-ref>    停用密钥并删除其会话
//	wedding backup create --output <path>  SQLite online backup 一致性备份
//
// 日志与输出纪律：不打印姓名、昵称、明文密钥、明文礼品码、Cookie 或请求正文。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/weddinggift/wedding/internal/admin"
	"github.com/weddinggift/wedding/internal/app"
	"github.com/weddinggift/wedding/internal/buildinfo"
	"github.com/weddinggift/wedding/internal/cryptox"
	"github.com/weddinggift/wedding/internal/domain"
	"github.com/weddinggift/wedding/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cfg := app.LoadConfig()

	switch os.Args[1] {
	case "serve":
		logger := app.NewLogger()
		if err := app.Serve(cfg, logger); err != nil {
			logger.Error("server_error", "error", err.Error())
			os.Exit(1)
		}
	case "healthz-check":
		if err := app.HealthCheck(cfg); err != nil {
			fmt.Fprintln(os.Stderr, "healthz-check failed:", err)
			os.Exit(1)
		}
	case "version":
		fmt.Println(buildinfo.String())
	case "migrate":
		os.Exit(runMigrate(cfg))
	case "data":
		os.Exit(runData(cfg, os.Args[2:]))
	case "status":
		os.Exit(runStatus(cfg))
	case "key":
		os.Exit(runKey(cfg, os.Args[2:]))
	case "backup":
		os.Exit(runBackup(cfg, os.Args[2:]))
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", os.Args[1])
		usage()
		os.Exit(2)
	}
}

// commandTimeout 是管理子命令的统一超时（数据量极小，30 秒绰绰有余）。
const commandTimeout = 30 * time.Second

// openStore 打开数据库并要求 schema 已迁移到当前版本。
func openStore(cfg app.Config) (*store.DB, context.Context, context.CancelFunc, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		cancel()
		return nil, nil, nil, err
	}
	if err := db.RequireSchema(ctx); err != nil {
		_ = db.Close()
		cancel()
		return nil, nil, nil, err
	}
	return db, ctx, cancel, nil
}

func runMigrate(cfg app.Config) int {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()

	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate 失败:", err)
		return 1
	}
	defer db.Close()

	applied, err := db.Migrate(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate 失败:", err)
		return 1
	}
	version, err := db.SchemaVersion(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate 失败:", err)
		return 1
	}
	fmt.Printf("migrate 完成：本次应用 %d 个 migration，当前 schema 版本 %d\n", applied, version)
	return 0
}

func runData(cfg app.Config, args []string) int {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "用法: wedding data <validate|import> --dir <数据目录>")
		return 2
	}
	sub := args[0]
	if sub != "validate" && sub != "import" {
		fmt.Fprintln(os.Stderr, "未知 data 子命令:", sub)
		return 2
	}

	fs := flag.NewFlagSet("data "+sub, flag.ContinueOnError)
	dir := fs.String("dir", "", "数据文件目录（含六个契约 CSV）")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "缺少 --dir 参数")
		return 2
	}

	// 装载与校验是两个独立阶段：任一阶段出错都中止，导入时数据库零写入。
	ds, loadErrs := admin.LoadDataset(*dir)
	if len(loadErrs) > 0 {
		fmt.Fprintln(os.Stderr, "数据文件解析失败：")
		for _, e := range loadErrs {
			fmt.Fprintln(os.Stderr, "  -", e)
		}
		return 1
	}
	if valErrs := domain.Validate(ds); len(valErrs) > 0 {
		fmt.Fprintf(os.Stderr, "数据校验未通过（%d 处问题）：\n", len(valErrs))
		for _, e := range valErrs {
			fmt.Fprintln(os.Stderr, "  -", e)
		}
		return 1
	}

	fmt.Print("数据校验通过。核对报告（脱敏）：\n", admin.DatasetReport(ds))
	if sub == "validate" {
		return 0
	}

	cx, err := cryptox.Load(cfg.AppKeyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "import 失败:", err)
		return 1
	}
	db, ctx, cancel, err := openStore(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "import 失败:", err)
		return 1
	}
	defer db.Close()
	defer cancel()

	if err := db.ImportDataset(ctx, ds, cx); err != nil {
		fmt.Fprintln(os.Stderr, "import 失败:", err)
		return 1
	}
	fmt.Println("导入完成：全部数据已在单事务内写入，明文密钥与礼品码未落库。")
	return 0
}

func runStatus(cfg app.Config) int {
	db, ctx, cancel, err := openStore(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "status 失败:", err)
		return 1
	}
	defer db.Close()
	defer cancel()

	stats, err := db.Stats(ctx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "status 失败:", err)
		return 1
	}
	fmt.Print(admin.StatusText(stats))
	return 0
}

func runKey(cfg app.Config, args []string) int {
	if len(args) < 1 || args[0] != "revoke" {
		fmt.Fprintln(os.Stderr, "用法: wedding key revoke --ref <卡片 external_ref>")
		return 2
	}
	fs := flag.NewFlagSet("key revoke", flag.ContinueOnError)
	ref := fs.String("ref", "", "卡片密钥的 external_ref（如 K001）")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *ref == "" {
		fmt.Fprintln(os.Stderr, "缺少 --ref 参数")
		return 2
	}

	db, ctx, cancel, err := openStore(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "key revoke 失败:", err)
		return 1
	}
	defer db.Close()
	defer cancel()

	result, err := db.RevokeAccessKey(ctx, *ref)
	if err != nil {
		fmt.Fprintln(os.Stderr, "key revoke 失败:", err)
		return 1
	}
	switch result {
	case store.RevokeDone:
		fmt.Printf("密钥 %s 已停用，其全部会话已删除。\n", *ref)
	case store.RevokeAlreadyDisabled:
		fmt.Printf("密钥 %s 此前已处于停用状态；其会话已再次清理。\n", *ref)
	case store.RevokeNotFound:
		fmt.Fprintf(os.Stderr, "密钥 %s 不存在。\n", *ref)
		return 1
	}
	return 0
}

func runBackup(cfg app.Config, args []string) int {
	if len(args) < 1 || args[0] != "create" {
		fmt.Fprintln(os.Stderr, "用法: wedding backup create --output <目标文件路径>")
		return 2
	}
	fs := flag.NewFlagSet("backup create", flag.ContinueOnError)
	out := fs.String("output", "", "备份文件输出路径（须不存在，不覆盖）")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "缺少 --output 参数")
		return 2
	}

	db, ctx, cancel, err := openStore(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "backup create 失败:", err)
		return 1
	}
	defer db.Close()
	defer cancel()

	if err := db.BackupTo(ctx, *out); err != nil {
		fmt.Fprintln(os.Stderr, "backup create 失败:", err)
		return 1
	}
	fmt.Printf("备份完成：%s（online backup API，完整性检查通过）\n", *out)
	return 0
}

func usage() {
	fmt.Fprint(os.Stderr, `wedding — WeddingGift 单二进制入口

用法:
  wedding serve                          启动 HTTP 服务
  wedding healthz-check                  向本地 /healthz 发起一次健康检查
  wedding version                        打印构建版本信息
  wedding migrate                        应用数据库 migration
  wedding data validate --dir <dir>      只读校验数据文件（六个契约 CSV）
  wedding data import --dir <dir>        校验通过后单事务导入（要求空库）
  wedding status                         输出脱敏统计
  wedding key revoke --ref <ref>         停用密钥并删除其会话
  wedding backup create --output <path>  SQLite online backup 一致性备份

环境变量:
  WEDDING_LISTEN_ADDR    HTTP 监听地址，默认 ":3000"
  WEDDING_DB_PATH        SQLite 数据库路径，默认 "/data/wedding/data/wedding.db"
  WEDDING_APP_KEY_PATH   app.key 路径，默认 "/data/wedding/secrets/app.key"
  WEDDING_ALLOWED_HOSTS  /api 接受的 Host 白名单（逗号分隔），默认 "stg.costra.cloud"
  WEDDING_*文案变量       WEDDING_COUPLE_NAMES / WEDDING_DATE / WEDDING_THANKS_MESSAGE 等（M0 §3.1）
`)
}
