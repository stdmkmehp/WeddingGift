package report

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// SMTPConfig 是日报邮件的发送配置（来自 WEDDING_SMTP_* / WEDDING_MAIL_*
// 环境变量，见 app.LoadConfig；秘密值经 /data/wedding/secrets/smtp.env 注入，
// 不进入 Git）。
type SMTPConfig struct {
	Host string
	Port int
	User string
	Pass string
	From string
	To   []string
}

// Enabled 报告 SMTP 是否已完整配置。未配置时 report daily 仅 --dry-run 可用。
func (c SMTPConfig) Enabled() bool {
	return c.Host != "" && c.Port > 0 && c.From != "" && len(c.To) > 0
}

// SendMail 经 SMTP 发送纯文本邮件（UTF-8）。
// 465 端口走 SSL 直连（QQ 邮箱默认模式），其他端口在服务器支持时走 STARTTLS；
// 认证仅在 TLS 信道上进行。单次发送总耗时受 ctx 与连接 deadline 约束。
func SendMail(ctx context.Context, cfg SMTPConfig, subject, body string) error {
	if !cfg.Enabled() {
		return errors.New("SMTP 未配置：需要 WEDDING_SMTP_HOST / WEDDING_SMTP_PORT / WEDDING_MAIL_FROM / WEDDING_MAIL_TO")
	}

	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.Port))
	dialer := &net.Dialer{Timeout: 10 * time.Second}
	tlsCfg := &tls.Config{ServerName: cfg.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	var err error
	if cfg.Port == 465 {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsCfg)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("连接 SMTP 服务器 %s 失败: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()

	deadline := time.Now().Add(20 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)

	client, err := smtp.NewClient(conn, cfg.Host)
	if err != nil {
		return fmt.Errorf("建立 SMTP 会话失败: %w", err)
	}
	defer func() { _ = client.Close() }()

	if cfg.Port != 465 {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("STARTTLS 失败: %w", err)
			}
		}
	}
	if cfg.User != "" {
		if err := client.Auth(smtp.PlainAuth("", cfg.User, cfg.Pass, cfg.Host)); err != nil {
			return fmt.Errorf("SMTP 认证失败: %w", err)
		}
	}
	if err := client.Mail(cfg.From); err != nil {
		return fmt.Errorf("MAIL FROM 被拒绝: %w", err)
	}
	for _, to := range cfg.To {
		if err := client.Rcpt(to); err != nil {
			return fmt.Errorf("RCPT TO %s 被拒绝: %w", to, err)
		}
	}
	w, err := client.Data()
	if err != nil {
		return fmt.Errorf("DATA 命令失败: %w", err)
	}
	if _, err := w.Write([]byte(buildMessage(cfg, subject, body))); err != nil {
		return fmt.Errorf("写入邮件内容失败: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("提交邮件内容失败: %w", err)
	}
	if err := client.Quit(); err != nil {
		return fmt.Errorf("结束 SMTP 会话失败: %w", err)
	}
	return nil
}

// buildMessage 组装 RFC 5322 报文：头部非 ASCII 值按 RFC 2047 编码，
// 正文统一 CRLF 行尾（smtp.Data 的 writer 已处理点号转义）。
func buildMessage(cfg SMTPConfig, subject, body string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", encodeAddress("WeddingGift 日报", cfg.From))
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(cfg.To, ", "))
	fmt.Fprintf(&b, "Subject: %s\r\n", encodeHeader(subject))
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: 8bit\r\n")
	b.WriteString("\r\n")
	b.WriteString(crlf(body))
	return b.String()
}

// encodeAddress 渲染 "显示名 <地址>"，显示名非 ASCII 时按 RFC 2047 编码。
func encodeAddress(name, addr string) string {
	if name == "" {
		return addr
	}
	return encodeHeader(name) + " <" + addr + ">"
}

// encodeHeader 对含非 ASCII 字符的头部值做 RFC 2047 encoded-word 编码。
func encodeHeader(s string) string {
	if isASCII(s) {
		return s
	}
	return "=?UTF-8?B?" + base64.StdEncoding.EncodeToString([]byte(s)) + "?="
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

// crlf 把行尾统一为 CRLF：先归一已有 CRLF，避免双重转换。
func crlf(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}
