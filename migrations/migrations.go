// Package migrations 通过 go:embed 把 SQL migration 编译进二进制。
//
// 文件命名约定 NNNN_name.sql，按文件名字典序依次应用；已应用的版本号
// 记录在 SQLite user_version pragma 中（从 1 起）。migration 在发布时由
// 同版本二进制通过 `wedding migrate` 显式执行，失败即停止发布。
package migrations

import "embed"

// FS 是编译进二进制的全部 SQL migration。
//
//go:embed *.sql
var FS embed.FS
