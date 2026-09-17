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
//	wedding data append --dir <dir> [--dry-run]  开放世界校验通过后单事务纯追加（要求已首导，在线可用）
//	wedding status                         输出脱敏统计（内部编号与数量，不含明文）
//	wedding key revoke --ref <card-ref>    停用密钥并删除其会话
//	wedding backup create --output <path>  SQLite online backup 一致性备份
//	wedding report daily [--dry-run] [--window 24h]  纯统计版日报（人工验证口径与 SMTP）
//
// 正式每日日报由 serve 进程内调度发送（统计 + 登录失败内存明细），不走 cron/CLI。
//
// 日志与输出纪律：不打印姓名、昵称、明文密钥、明文礼品码、Cookie 或请求正文。
// 例外：日报/告警邮件正文含同行组称呼与登录失败的输入名字——收件人即项目
// 所有者本人（管理员），邮件不经过日志；内存缓冲中密钥只存掩码；
// 其余 stdout/stderr 输出仍遵守上述纪律。
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/weddinggift/wedding/internal/admin"
	"github.com/weddinggift/wedding/internal/app"
	"github.com/weddinggift/wedding/internal/buildinfo"
	"github.com/weddinggift/wedding/internal/cryptox"
	"github.com/weddinggift/wedding/internal/domain"
	"github.com/weddinggift/wedding/internal/report"
	"github.com/weddinggift/wedding/internal/store"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	// cfgErr 仅为 SMTP 配置加载问题（文件不可读/格式非法）：日报是可选能力，
	// 绝不允许它阻塞 serve 对外服务——serve 记警告并禁用日报；
	// report daily 发送时才以该错误失败退出；其余子命令不受影响。
	cfg, cfgErr := app.LoadConfig()

	switch os.Args[1] {
	case "serve":
		logger := app.NewLogger()
		if cfgErr != nil {
			logger.Warn("smtp_config_invalid", slog.String("error", cfgErr.Error()))
		}
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
	case "report":
		os.Exit(runReport(cfg, cfgErr, os.Args[2:]))
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
		fmt.Fprintln(os.Stderr, "用法: wedding data <validate|import|append> --dir <数据目录>")
		return 2
	}
	sub := args[0]
	if sub == "append" {
		return runDataAppend(cfg, args[1:])
	}
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

// runDataAppend 处理 data append：装载追加包 → 读库内现状基线（含用 app.key
// 解密既有礼品凭证）→ 开放世界校验 → 核对报告 → --dry-run 止步或单事务纯追加。
// 服务在线时可安全执行（纯 INSERT 短事务；WAL + busy_timeout）。
func runDataAppend(cfg app.Config, args []string) int {
	fs := flag.NewFlagSet("data append", flag.ContinueOnError)
	dir := fs.String("dir", "", "追加数据目录（五类实体契约 CSV，允许缺失或仅表头）")
	dryRun := fs.Bool("dry-run", false, "只校验并输出核对报告，不写入数据库")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dir == "" {
		fmt.Fprintln(os.Stderr, "缺少 --dir 参数")
		return 2
	}

	ds, loadErrs := admin.LoadAppendDataset(*dir)
	if len(loadErrs) > 0 {
		fmt.Fprintln(os.Stderr, "数据文件解析失败：")
		for _, e := range loadErrs {
			fmt.Fprintln(os.Stderr, "  -", e)
		}
		return 1
	}

	// 追加校验是开放世界校验：必须读取库内现状基线。
	cx, err := cryptox.Load(cfg.AppKeyPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "append 失败:", err)
		return 1
	}
	db, ctx, cancel, err := openStore(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "append 失败:", err)
		return 1
	}
	defer db.Close()
	defer cancel()

	state, err := db.LoadExistingState(ctx, cx)
	if err != nil {
		fmt.Fprintln(os.Stderr, "append 失败:", err)
		return 1
	}
	if valErrs := domain.ValidateAppend(ds, state); len(valErrs) > 0 {
		fmt.Fprintf(os.Stderr, "数据校验未通过（%d 处问题）：\n", len(valErrs))
		for _, e := range valErrs {
			fmt.Fprintln(os.Stderr, "  -", e)
		}
		return 1
	}

	fmt.Print("数据校验通过。核对报告（脱敏）：\n", admin.AppendReport(ds, state))
	if *dryRun {
		fmt.Println("dry-run：未写入任何数据。")
		return 0
	}

	if err := db.AppendDataset(ctx, ds, cx); err != nil {
		fmt.Fprintln(os.Stderr, "append 失败:", err)
		return 1
	}
	fmt.Println("追加完成：全部新增数据已在单事务内写入，明文礼品凭证未落库。")
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

// runReport 处理 report daily：收集滚动窗口（默认最近 24 小时）新增的绑定与
// 登录及累积数据，渲染纯文本统计版日报邮件。--dry-run 只统计并打印不发送；
// 发送要求已通过 WEDDING_SMTP_* / WEDDING_MAIL_* 完整配置 SMTP。
//
// 定位：本命令是统计口径与 SMTP 链路的人工验证工具。正式的每日日报由 serve
// 进程内调度发送（含登录失败明细）——失败缓冲只存在于 serve 进程内存，
// 本独立进程不可见，因此 CLI 渲染纯统计版并如实注明。
func runReport(cfg app.Config, cfgErr error, args []string) int {
	if len(args) < 1 || args[0] != "daily" {
		fmt.Fprintln(os.Stderr, "用法: wedding report daily [--dry-run] [--window 24h]")
		return 2
	}
	fs := flag.NewFlagSet("report daily", flag.ContinueOnError)
	dryRun := fs.Bool("dry-run", false, "只统计并打印邮件内容，不发送")
	window := fs.Duration("window", 24*time.Hour, "滚动统计窗口宽度")
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	// SMTP 配置加载失败：dry-run 仅提示（统计与 SMTP 无关）；发送时必须失败退出。
	if cfgErr != nil {
		fmt.Fprintln(os.Stderr, "SMTP 配置警告:", cfgErr)
		if !*dryRun {
			return 1
		}
	}

	db, ctx, cancel, err := openStore(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, "report daily 失败:", err)
		return 1
	}
	defer db.Close()
	defer cancel()

	summary, err := report.Collect(ctx, db, time.Now(), *window)
	if err != nil {
		fmt.Fprintln(os.Stderr, "report daily 失败:", err)
		return 1
	}
	subject, body := report.RenderDailyStatsOnly(summary)

	if *dryRun {
		fmt.Printf("Subject: %s\n\n%s", subject, body)
		return 0
	}
	if err := report.SendMail(ctx, cfg.SMTP, subject, body); err != nil {
		fmt.Fprintln(os.Stderr, "report daily 失败:", err)
		return 1
	}
	fmt.Printf("日报已发送：%s → %s\n", subject, strings.Join(cfg.SMTP.To, ", "))
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
  wedding data append --dir <dir> [--dry-run]  校验通过后单事务纯追加（要求已首导，在线可用）
  wedding status                         输出脱敏统计
  wedding key revoke --ref <ref>         停用密钥并删除其会话
  wedding backup create --output <path>  SQLite online backup 一致性备份
  wedding report daily [--dry-run] [--window 24h]
                                         纯统计版日报（人工验证口径与 SMTP 链路；
                                         正式日报由 serve 进程内调度发送）

环境变量:
  WEDDING_LISTEN_ADDR    HTTP 监听地址，默认 ":3000"
  WEDDING_DB_PATH        SQLite 数据库路径，默认 "/data/wedding/data/wedding.db"
  WEDDING_APP_KEY_PATH   app.key 路径，默认 "/data/wedding/secrets/app.key"
  WEDDING_ALLOWED_HOSTS  /api 接受的 Host 白名单（逗号分隔），默认 "stg.costra.cloud"
  WEDDING_SMTP_HOST      日报 SMTP 服务器（如 smtp.qq.com），为空则 serve 不启动日报调度
  WEDDING_SMTP_PORT      日报 SMTP 端口，默认 465（SSL 直连；其他端口走 STARTTLS）
  WEDDING_SMTP_USER      日报 SMTP 认证用户名
  WEDDING_SMTP_PASS      日报 SMTP 认证密码/授权码（秘密）
  WEDDING_MAIL_FROM      日报发件地址
  WEDDING_MAIL_TO        日报收件地址（逗号分隔多个）
  WEDDING_SMTP_ENV_PATH  SMTP 配置文件路径（KEY=VALUE 形式，环境变量优先、文件补缺），
                         默认 "/data/wedding/secrets/smtp.env"；文件缺失则仅用环境变量
  WEDDING_REPORT_DAILY_AT  serve 进程内日报的北京时间发送时刻（HH:MM），默认 "07:17"
  WEDDING_*文案变量       WEDDING_COUPLE_NAMES / WEDDING_DATE / WEDDING_THANKS_MESSAGE 等（M0 §3.1）
`)
}
