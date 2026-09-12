// Package app 负责应用配置、生命周期装配与子命令实现。
package app

import "os"

// Config 是从环境变量读取的运行配置。
type Config struct {
	// ListenAddr 是 HTTP 监听地址，如 ":3000"。
	ListenAddr string
	// DBPath 是 SQLite 数据库文件路径。
	DBPath string
	// AppKeyPath 是应用主秘密 app.key 的路径（密钥 HMAC 与礼品码 AEAD 由此派生）。
	AppKeyPath string
}

// LoadConfig 从环境变量读取配置并填充默认值。
// 默认值对齐共享机器目录收束约定（/data/wedding/...）；本地开发用环境变量覆盖。
func LoadConfig() Config {
	return Config{
		ListenAddr: envOr("WEDDING_LISTEN_ADDR", ":3000"),
		DBPath:     envOr("WEDDING_DB_PATH", "/data/wedding/data/wedding.db"),
		AppKeyPath: envOr("WEDDING_APP_KEY_PATH", "/data/wedding/secrets/app.key"),
	}
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
