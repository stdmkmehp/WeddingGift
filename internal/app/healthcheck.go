package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HealthCheck 向本地 serve 端口发起一次 /healthz 请求。
//
// 供 Docker healthcheck 通过 `wedding healthz-check` 调用：成功返回 nil，
// 失败返回错误（调用方据此设置非 0 退出码）。不依赖镜像内的 curl/wget，
// 因此镜像可用 distroless/scratch 精简基底。
func HealthCheck(cfg Config) error {
	url := "http://" + cfg.HealthAddr() + "/healthz"

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// 读走响应体以便连接复用；内容此处无需解析。
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: status %d", resp.StatusCode)
	}
	return nil
}
