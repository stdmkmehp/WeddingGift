// Package app 负责应用配置、生命周期装配与子命令实现。
package app

import "os"

// Config 是从环境变量读取的运行配置。M1 只需监听地址。
type Config struct {
	// ListenAddr 是 HTTP 监听地址，如 ":3000"。
	ListenAddr string
}

// LoadConfig 从环境变量读取配置并填充默认值。
func LoadConfig() Config {
	return Config{
		ListenAddr: envOr("WEDDING_LISTEN_ADDR", ":3000"),
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
