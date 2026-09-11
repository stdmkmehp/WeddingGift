// Package httpx 提供 HTTP 路由、中间件与占位主页/健康检查处理器。
//
// M1 阶段只包含链路验证所需的最小内容：占位主页、嵌入静态资源和
// 健康检查；不包含任何业务 API、数据库或业务秘密。
package httpx

import (
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/weddinggift/wedding/internal/buildinfo"
	"github.com/weddinggift/wedding/web"
)

// Server 持有 HTTP 处理所需的依赖。
type Server struct {
	logger  *slog.Logger
	handler http.Handler
	// startedAt 用于健康检查展示运行信息。
	startedAt time.Time
}

// New 构建带有全部路由与中间件的 Server。
func New(logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}

	s := &Server{
		logger:    logger,
		startedAt: time.Now().UTC(),
	}

	mux := http.NewServeMux()

	// 静态资源：从嵌入的 assets 子目录提供，路径为相对的 /assets/*。
	assetsFS, err := fs.Sub(web.Assets, "assets")
	if err != nil {
		return nil, err
	}
	assetsHandler := http.StripPrefix("/assets/", http.FileServer(http.FS(assetsFS)))
	mux.Handle("GET /assets/", assetsHandler)

	// 健康检查：进程存活即返回 ok；M1 无数据库，后续里程碑再加只读查询。
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	// 占位主页：仅根路径返回单页，其余未知路径 404。
	mux.HandleFunc("GET /", s.handleIndex)

	// 中间件链：请求 ID → 安全响应头 → 访问日志。
	s.handler = withRequestID(withSecurityHeaders(withAccessLog(logger, mux)))
	return s, nil
}

// Handler 返回组合完成的 http.Handler。
func (s *Server) Handler() http.Handler {
	return s.handler
}

// handleIndex 只在精确根路径返回占位主页，其余路径返回 404。
func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data, err := web.Assets.ReadFile("index.html")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(data)
}

// handleHealthz 返回进程健康状态与构建信息（JSON）。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": buildinfo.String(),
		"time":    time.Now().UTC().Format(time.RFC3339),
		"uptime":  time.Since(s.startedAt).Round(time.Second).String(),
	})
}
