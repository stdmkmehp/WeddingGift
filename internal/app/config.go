// Package app 负责应用配置、生命周期装配与子命令实现。
package app

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/weddinggift/wedding/internal/httpx"
	"github.com/weddinggift/wedding/internal/report"
)

// Config 是从环境变量读取的运行配置。
type Config struct {
	// ListenAddr 是 HTTP 监听地址，如 ":3000"。
	ListenAddr string
	// DBPath 是 SQLite 数据库文件路径。
	DBPath string
	// AppKeyPath 是应用主秘密 app.key 的路径（密钥 HMAC 与礼品码 AEAD 由此派生）。
	AppKeyPath string
	// AllowedHosts 是 /api 接受的 Host 清单（不含端口）；回环地址总是允许。
	AllowedHosts []string
	// Texts 是 M0 §3.1 冻结的文案字段（新人称呼与婚期已定稿为默认值，
	// 其余仍为占位默认，真实文案上线前用环境变量覆盖）。
	Texts httpx.Texts
	// SMTP 是每日统计日报与登录失败告警的发送配置；未完整配置时 serve
	// 不启动进程内日报调度，CLI report daily 仅 --dry-run 可用。
	// 取值来源：环境变量优先，SMTPEnvPath 文件补缺（运行时主动读取，
	// 与 app.key 同一秘密管理模式，不经 compose env_file 注入）。
	SMTP report.SMTPConfig
	// ReportDailyAt 是 serve 进程内日报的北京时间发送时刻（"HH:MM"）。
	ReportDailyAt string
}

// LoadConfig 从环境变量与 SMTP 配置文件读取配置并填充默认值。
// 默认值对齐共享机器目录收束约定（/data/wedding/...）；本地开发用环境变量覆盖。
// SMTP 配置文件缺失不报错（可选配置）；存在但格式非法则返回错误。
func LoadConfig() (Config, error) {
	smtp, err := loadSMTPConfig()
	if err != nil {
		return Config{}, err
	}
	return Config{
		ListenAddr:   envOr("WEDDING_LISTEN_ADDR", ":3000"),
		DBPath:       envOr("WEDDING_DB_PATH", "/data/wedding/data/wedding.db"),
		AppKeyPath:   envOr("WEDDING_APP_KEY_PATH", "/data/wedding/secrets/app.key"),
		AllowedHosts: splitAndTrim(envOr("WEDDING_ALLOWED_HOSTS", "stg.costra.cloud")),
		Texts: httpx.Texts{
			CoupleNames:     envOr("WEDDING_COUPLE_NAMES", "赵伟 & 刘子璇"),
			WeddingDate:     envOr("WEDDING_DATE", "2026-09-26"),
			HomeIntro:       envOr("WEDDING_HOME_INTRO", "感谢你来见证我们的幸福时刻"),
			LoginHint:       envOr("WEDDING_LOGIN_HINT", "请输入你的名字，以及卡片上的 8 位数字"),
			ThanksMessage:   envOr("WEDDING_THANKS_MESSAGE", "谢谢你的祝福，这是我们为你准备的小小心意"),
			PrivacyNote:     envOr("WEDDING_PRIVACY_NOTE", "本页限时开放（即日起至2026-12-31）。本页只收集识别所需的信息，活动结束后删除。"),
			ContactHint:     envOr("WEDDING_CONTACT_HINT", "如有疑问，请咨询赵伟/刘子璇。"),
			IcpFilingText:   envOr("WEDDING_ICP_FILING_TEXT", "沪ICP备2026017253号-2"),
			IcpFilingURL:    envOr("WEDDING_ICP_FILING_URL", "https://beian.miit.gov.cn/"),
			MaintenanceText: envOr("WEDDING_MAINTENANCE_TEXT", "页面维护中，请稍后再试。"),
		},
		SMTP:          smtp,
		ReportDailyAt: envOr("WEDDING_REPORT_DAILY_AT", "07:17"),
	}, nil
}

// loadSMTPConfig 组装 SMTP 配置：真实环境变量优先，WEDDING_SMTP_ENV_PATH
// 指向的配置文件（默认 /data/wedding/secrets/smtp.env）补缺。
// 文件缺失视为未配置（可选能力）；文件存在但格式非法返回错误。
func loadSMTPConfig() (report.SMTPConfig, error) {
	file, err := loadSMTPEnvFile(envOr("WEDDING_SMTP_ENV_PATH", "/data/wedding/secrets/smtp.env"))
	if err != nil {
		return report.SMTPConfig{}, err
	}
	pick := func(key string) string {
		if v := os.Getenv(key); v != "" {
			return v
		}
		return file[key]
	}

	cfg := report.SMTPConfig{
		Host: pick("WEDDING_SMTP_HOST"),
		User: pick("WEDDING_SMTP_USER"),
		Pass: pick("WEDDING_SMTP_PASS"),
		From: pick("WEDDING_MAIL_FROM"),
		To:   splitAndTrim(pick("WEDDING_MAIL_TO")),
		Port: 465,
	}
	if ps := pick("WEDDING_SMTP_PORT"); ps != "" {
		n, err := strconv.Atoi(ps)
		if err != nil || n <= 0 {
			return report.SMTPConfig{}, fmt.Errorf("WEDDING_SMTP_PORT 非法: %q", ps)
		}
		cfg.Port = n
	}
	return cfg, nil
}

// HealthAddr 返回本机健康检查请求使用的地址（供 healthz-check 子命令使用）。
// 当监听地址形如 ":3000" 时，指向 127.0.0.1:3000。
func (c Config) HealthAddr() string {
	addr := c.ListenAddr
	if len(addr) > 0 && addr[0] == ':' {
		return "127.0.0.1" + addr
	}
	return addr
}

func envOr(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return fallback
}

func splitAndTrim(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}
