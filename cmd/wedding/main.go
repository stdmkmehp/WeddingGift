// Command wedding 是 WeddingGift 的单二进制入口。
//
// M1 阶段实现两个子命令：
//
//	wedding serve          启动占位主页 HTTP 服务
//	wedding healthz-check  向本地 /healthz 发起一次健康检查（供 Docker healthcheck 调用）
//	wedding version        打印构建版本信息
//
// 数据校验/导入、状态、密钥停用、备份等管理子命令在后续里程碑加入。
package main

import (
	"fmt"
	"os"

	"github.com/weddinggift/wedding/internal/app"
	"github.com/weddinggift/wedding/internal/buildinfo"
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
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintln(os.Stderr, "unknown command:", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `wedding — WeddingGift 单二进制入口

用法:
  wedding serve          启动占位主页 HTTP 服务
  wedding healthz-check  向本地 /healthz 发起一次健康检查
  wedding version        打印构建版本信息

环境变量:
  WEDDING_LISTEN_ADDR    HTTP 监听地址，默认 ":3000"
`)
}
