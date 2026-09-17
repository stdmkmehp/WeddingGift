package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSMTPEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "smtp.env")
	content := `# 注释行
WEDDING_SMTP_HOST=smtp.qq.com
WEDDING_SMTP_PORT=465

WEDDING_SMTP_USER= 1628284520@qq.com
WEDDING_SMTP_PASS="pnqoapdehwbmbaih"
WEDDING_MAIL_FROM='1628284520@qq.com'
WEDDING_MAIL_TO=a@qq.com, b@qq.com
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadSMTPEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"WEDDING_SMTP_HOST": "smtp.qq.com",
		"WEDDING_SMTP_PORT": "465",
		"WEDDING_SMTP_USER": "1628284520@qq.com",
		"WEDDING_SMTP_PASS": "pnqoapdehwbmbaih",
		"WEDDING_MAIL_FROM": "1628284520@qq.com",
		"WEDDING_MAIL_TO":   "a@qq.com, b@qq.com",
	}
	if len(got) != len(want) {
		t.Fatalf("解析条目数 = %d, want %d: %v", len(got), len(want), got)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
}

func TestLoadSMTPEnvFileMissing(t *testing.T) {
	got, err := loadSMTPEnvFile(filepath.Join(t.TempDir(), "not-exist.env"))
	if err != nil || got != nil {
		t.Errorf("文件缺失应返回 (nil, nil)，实际 (%v, %v)", got, err)
	}
}

func TestLoadSMTPEnvFileMalformed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "smtp.env")
	if err := os.WriteFile(path, []byte("WEDDING_SMTP_HOST=smtp.qq.com\n这一行没有等号\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := loadSMTPEnvFile(path)
	if err == nil || !strings.Contains(err.Error(), "第 2 行") {
		t.Errorf("格式非法应报行号错误，实际 %v", err)
	}
}

// TestLoadSMTPConfigPrecedence 环境变量优先于文件；文件补缺未设置的项。
func TestLoadSMTPConfigPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "smtp.env")
	content := strings.Join([]string{
		"WEDDING_SMTP_HOST=file-host",
		"WEDDING_SMTP_PORT=587",
		"WEDDING_SMTP_USER=file-user",
		"WEDDING_SMTP_PASS=file-pass",
		"WEDDING_MAIL_FROM=file@qq.com",
		"WEDDING_MAIL_TO=to1@qq.com, to2@qq.com",
	}, "\n")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEDDING_SMTP_ENV_PATH", path)
	t.Setenv("WEDDING_SMTP_HOST", "env-host") // 环境变量应胜出

	cfg, err := loadSMTPConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "env-host" {
		t.Errorf("Host 应取环境变量，实际 %q", cfg.Host)
	}
	if cfg.Port != 587 || cfg.User != "file-user" || cfg.Pass != "file-pass" ||
		cfg.From != "file@qq.com" || len(cfg.To) != 2 || cfg.To[1] != "to2@qq.com" {
		t.Errorf("文件补缺不对: %+v", cfg)
	}
	if !cfg.Enabled() {
		t.Error("完整配置应 Enabled")
	}
}

// TestLoadSMTPConfigDefaults 无环境变量且无文件：仅端口默认值，Enabled=false。
func TestLoadSMTPConfigDefaults(t *testing.T) {
	t.Setenv("WEDDING_SMTP_ENV_PATH", filepath.Join(t.TempDir(), "missing.env"))
	cfg, err := loadSMTPConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 465 {
		t.Errorf("缺省端口 = %d, want 465", cfg.Port)
	}
	if cfg.Enabled() {
		t.Errorf("未配置时不应 Enabled: %+v", cfg)
	}
}

// TestLoadSMTPConfigBadPort 文件中的非法端口应报错而非静默回退。
func TestLoadSMTPConfigBadPort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "smtp.env")
	if err := os.WriteFile(path, []byte("WEDDING_SMTP_PORT=abc\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WEDDING_SMTP_ENV_PATH", path)
	if _, err := loadSMTPConfig(); err == nil || !strings.Contains(err.Error(), "WEDDING_SMTP_PORT") {
		t.Errorf("非法端口应报错，实际 %v", err)
	}
}
