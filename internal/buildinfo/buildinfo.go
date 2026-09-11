// Package buildinfo 承载编译期注入的版本信息。
package buildinfo

// 这些变量由构建时 -ldflags 注入，默认值用于开发环境。
var (
	// Version 是构建版本号（如 git tag 或短 commit）。
	Version = "dev"
	// Commit 是构建对应的 git commit 短哈希。
	Commit = "unknown"
	// BuildTime 是构建时间（RFC3339）。
	BuildTime = "unknown"
)

// String 返回适合展示的版本摘要。
func String() string {
	return Version + " (" + Commit + ", " + BuildTime + ")"
}
