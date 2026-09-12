// Package httpx 提供 HTTP 路由、中间件与页面/API 处理器。
//
// M4 起公网路由完整形态（技术架构 §9.1）：单页主页、静态资源、健康检查，
// 以及 bootstrap / login / logout 三个 API。所有 /api 请求经过
// Host/Origin 来源校验，登录接口另有双维度内存限流。
package httpx

import (
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/weddinggift/wedding/internal/auth"
	"github.com/weddinggift/wedding/internal/buildinfo"
	"github.com/weddinggift/wedding/internal/cryptox"
	"github.com/weddinggift/wedding/internal/gift"
	"github.com/weddinggift/wedding/internal/store"
	"github.com/weddinggift/wedding/web"
)

// Texts 是 M0 冻结的文案字段集合（docs/04-m0-decisions.md §3.1），
// 由 app 配置装配，bootstrap 时整体下发给前端。
type Texts struct {
	CoupleNames   string
	WeddingDate   string
	HomeIntro     string
	LoginHint     string
	ThanksMessage string
	PrivacyNote   string
	ContactHint   string
	IcpFilingText string
	IcpFilingURL  string
	// MaintenanceText 是维护/到期状态的统一说明（M0 §3.1，WG-505）。
	MaintenanceText string
}

// Deps 是 HTTP 服务的全部外部依赖，由 app.Serve 装配。
type Deps struct {
	Logger *slog.Logger
	DB     *store.DB
	Auth   *auth.Service
	Crypto *cryptox.Service
	// Gift 是 M5 礼品码 reveal 服务；nil 时按 DB+Crypto 构造默认实现。
	Gift *gift.Service
	// Texts 为文案配置（M0 §3.1）。
	Texts Texts
	// AllowedHosts 是 /api 接受的 Host 清单（不含端口；回环地址总是允许）。
	AllowedHosts []string
	// Limiter 是登录限流器；nil 时使用 §10.3 默认阈值。
	Limiter *RateLimiter
}

// Server 持有 HTTP 处理所需的依赖。
type Server struct {
	logger       *slog.Logger
	handler      http.Handler
	db           *store.DB
	auth         *auth.Service
	crypto       *cryptox.Service
	gift         *gift.Service
	texts        serviceTexts
	allowedHosts []string
	limiter      *RateLimiter
	// startedAt 用于健康检查展示运行信息。
	startedAt time.Time
}

// New 构建带有全部路由与中间件的 Server。
func New(deps Deps) (*Server, error) {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	limiter := deps.Limiter
	if limiter == nil {
		// §10.3 默认阈值：单 IP 10 分钟 60 次失败、单密钥 10 分钟 10 次失败，
		// 以不误伤婚礼现场共享 NAT 为前提。
		limiter = NewRateLimiter(10*time.Minute, 60, 10)
	}

	giftSvc := deps.Gift
	if giftSvc == nil {
		giftSvc = gift.New(deps.DB, deps.Crypto)
	}

	s := &Server{
		logger:       logger,
		db:           deps.DB,
		auth:         deps.Auth,
		crypto:       deps.Crypto,
		gift:         giftSvc,
		allowedHosts: deps.AllowedHosts,
		limiter:      limiter,
		startedAt:    time.Now().UTC(),
	}
	s.texts = serviceTexts{
		CoupleNames:     deps.Texts.CoupleNames,
		WeddingDate:     deps.Texts.WeddingDate,
		HomeIntro:       deps.Texts.HomeIntro,
		LoginHint:       deps.Texts.LoginHint,
		ThanksMessage:   deps.Texts.ThanksMessage,
		PrivacyNote:     deps.Texts.PrivacyNote,
		ContactHint:     deps.Texts.ContactHint,
		IcpFilingText:   deps.Texts.IcpFilingText,
		IcpFilingURL:    deps.Texts.IcpFilingURL,
		MaintenanceText: deps.Texts.MaintenanceText,
	}

	mux := http.NewServeMux()

	// 静态资源：从嵌入的 assets 子目录提供，路径为相对的 /assets/*。
	assetsFS, err := fs.Sub(web.Assets, "assets")
	if err != nil {
		return nil, err
	}
	assetsHandler := http.StripPrefix("/assets/", http.FileServer(http.FS(assetsFS)))
	mux.Handle("GET /assets/", assetsHandler)

	// 健康检查：进程存活 + schema 版本 + SQLite 只读查询（§15.2）。
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	// 公网 API（§9.1）：统一先过 Host/Origin 来源校验。
	mux.HandleFunc("GET /api/bootstrap", s.withGuard(s.handleBootstrap))
	mux.HandleFunc("POST /api/auth/login", s.withGuard(s.handleLogin))
	mux.HandleFunc("POST /api/auth/logout", s.withGuard(s.handleLogout))
	mux.HandleFunc("POST /api/gifts/reveal", s.withGuard(s.handleRevealGift))

	// 占位主页：仅根路径返回单页，其余未知路径 404。
	mux.HandleFunc("GET /", s.handleIndex)

	// 中间件链：请求 ID → 安全响应头 → 访问日志。
	s.handler = withRequestID(withSecurityHeaders(withAccessLog(logger, mux)))
	return s, nil
}

// withGuard 把 Host/Origin 来源校验包在 API 处理器外层。
func (s *Server) withGuard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.guardAPI(w, r) {
			return
		}
		h(w, r)
	}
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

// handleHealthz 返回进程健康状态（JSON）：进程存活 + schema 版本 + SQLite
// 只读探活。数据库不可用时返回 503，Docker healthcheck 据此判定不健康。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	dbState := "ok"
	if err := s.db.Ping(r.Context()); err != nil {
		status = "unhealthy"
		dbState = "unreachable"
	} else if _, err := s.db.SchemaVersion(r.Context()); err != nil {
		status = "unhealthy"
		dbState = "schema_unreadable"
	}

	code := http.StatusOK
	if status != "ok" {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]any{
		"status":   status,
		"database": dbState,
		"version":  buildinfo.String(),
		"time":     time.Now().UTC().Format(time.RFC3339),
		"uptime":   time.Since(s.startedAt).Round(time.Second).String(),
	})
}
