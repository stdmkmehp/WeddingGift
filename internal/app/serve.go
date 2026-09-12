package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/weddinggift/wedding/internal/auth"
	"github.com/weddinggift/wedding/internal/buildinfo"
	"github.com/weddinggift/wedding/internal/cryptox"
	"github.com/weddinggift/wedding/internal/httpx"
	"github.com/weddinggift/wedding/internal/store"
)

// NewLogger 构建输出到 stdout 的结构化 JSON logger。
func NewLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
}

// Serve 启动 HTTP 服务并在收到终止信号时优雅关闭。
//
// M4 起 serve 必须完整接线：打开数据库（要求 schema 已迁移）、加载 app.key
// 派生加密材料、装配鉴权服务与限流。任一依赖缺失都直接失败退出，
// 不以降级形态对外服务（避免占位页掩盖配置错误）。
func Serve(cfg Config, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.RequireSchema(ctx); err != nil {
		return err
	}

	cx, err := cryptox.Load(cfg.AppKeyPath)
	if err != nil {
		return err
	}

	// 启动时顺手清理过期会话，保持 session 表有界。
	if n, err := db.DeleteExpiredSessions(ctx, time.Now().UTC().Format(time.RFC3339)); err != nil {
		logger.Warn("expired_session_cleanup_failed", slog.String("error_class", "internal"))
	} else if n > 0 {
		logger.Info("expired_sessions_cleaned", slog.Int64("count", n))
	}

	srv, err := httpx.New(httpx.Deps{
		Logger:       logger,
		DB:           db,
		Auth:         auth.New(db, cx),
		Crypto:       cx,
		Texts:        cfg.Texts,
		AllowedHosts: cfg.AllowedHosts,
	})
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("server_starting",
			slog.String("addr", cfg.ListenAddr),
			slog.String("version", buildinfo.String()),
		)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		logger.Info("server_stopping")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return httpServer.Shutdown(shutdownCtx)
	}
}
