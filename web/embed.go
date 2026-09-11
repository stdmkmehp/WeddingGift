// Package web 通过 go:embed 把占位主页与静态资源编译进二进制。
package web

import "embed"

// Assets 是编译进二进制的 web 资源（index.html 与 assets/*）。
//
//go:embed index.html assets
var Assets embed.FS
