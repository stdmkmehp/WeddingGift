// 公网 API：bootstrap / login / logout / gifts reveal（技术架构 §9.1 路由表）。
//
// 统一防护（§9.2、§10.3）：JSON 编解码、8 KB 请求体、Host/Origin 同源校验、
// 登录失败统一模糊、敏感接口 no-store、请求 ID 由外层中间件提供。
package httpx

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/weddinggift/wedding/internal/auth"
	"github.com/weddinggift/wedding/internal/domain"
	"github.com/weddinggift/wedding/internal/gift"
)

// sessionCookieName 是会话 Cookie 名称（技术架构 §9.4）。
const sessionCookieName = "wedding_session"

// maxAPIBodyBytes 是 JSON 请求体的应用层硬上限（§9.2；nginx 2m 为外层粗防）。
const maxAPIBodyBytes = 8 * 1024

// ---------- 请求/响应 DTO ----------

type apiError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type apiErrorBody struct {
	Error apiError `json:"error"`
}

type serviceTexts struct {
	CoupleNames     string `json:"coupleNames"`
	WeddingDate     string `json:"weddingDate"`
	HomeIntro       string `json:"homeIntro"`
	LoginHint       string `json:"loginHint"`
	ThanksMessage   string `json:"thanksMessage"`
	PrivacyNote     string `json:"privacyNote"`
	ContactHint     string `json:"contactHint"`
	IcpFilingText   string `json:"icpFilingText"`
	IcpFilingURL    string `json:"icpFilingUrl"`
	MaintenanceText string `json:"maintenanceText"`
}

type giftSummaryDTO struct {
	ID            string `json:"id"`
	TypeKey       string `json:"typeKey"`
	Title         string `json:"title"`
	Subtitle      string `json:"subtitle,omitempty"`
	ThemeVariant  string `json:"themeVariant,omitempty"`
	Instructions  string `json:"instructions"`
	ValidityText  string `json:"validityText,omitempty"`
	RedemptionURL string `json:"redemptionUrl,omitempty"`
	Ordinal       int    `json:"ordinal"`
}

type sessionDTO struct {
	Salutation    string           `json:"salutation"`
	CustomMessage string           `json:"customMessage,omitempty"`
	ThanksMessage string           `json:"thanksMessage"`
	ExpiresAt     string           `json:"expiresAt"`
	Gifts         []giftSummaryDTO `json:"gifts"`
}

type bootstrapResponse struct {
	Service serviceTexts `json:"service"`
	Session *sessionDTO  `json:"session"`
}

type loginRequest struct {
	LoginName string `json:"loginName"`
	KeyCode   string `json:"keyCode"`
}

type loginResponse struct {
	Session sessionDTO `json:"session"`
}

// ---------- 处理器 ----------

// handleBootstrap 返回服务文案与当前会话（无有效会话时 session 为 null）。
// 持有无效/过期 Cookie 时顺手清除，让客户端回到干净登录态。
func (s *Server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	resp := bootstrapResponse{Service: s.texts}

	token, ok := readSessionCookie(r)
	if !ok {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	payload, err := s.auth.ResolveSession(r.Context(), token)
	if err != nil {
		if !errors.Is(err, auth.ErrSessionInvalid) {
			s.logger.Error("bootstrap_session_error",
				slog.String("request_id", requestIDFromContext(r.Context())),
				slog.String("error_class", "session_resolve"))
			writeError(w, http.StatusInternalServerError, "internal_error", "服务暂时不可用，请稍后再试")
			return
		}
		clearSessionCookie(w)
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Session = s.sessionPayload(payload)
	writeJSON(w, http.StatusOK, resp)
}

// handleLogin 处理登录：格式校验 → 限流 → 绑定事务 → 写 Cookie。
// 失败时按 IP 与密钥摘要双维度计数；一切身份/绑定错误对外完全一致。
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}

	ip := clientIP(r)
	// 密钥格式合法才有摘要可统计；格式非法只按 IP 统计。
	var keyDigest []byte
	if domain.IsValidAccessKeyCode(req.KeyCode) {
		keyDigest = s.crypto.AccessKeyDigest(req.KeyCode)
	}

	if !s.limiter.AllowLogin(ip, keyDigest) {
		s.logger.Info("login_rate_limited",
			slog.String("request_id", requestIDFromContext(r.Context())))
		writeError(w, http.StatusTooManyRequests, "rate_limited", "尝试过于频繁，请稍后再试")
		return
	}

	result, err := s.auth.Login(r.Context(), req.LoginName, req.KeyCode)
	switch {
	case errors.Is(err, auth.ErrInvalidInput):
		s.limiter.RecordLoginFailure(ip, keyDigest)
		writeError(w, http.StatusBadRequest, "invalid_input", "请检查输入格式：名字或昵称不能为空，密钥必须是卡片上的 8 位数字")
		return
	case errors.Is(err, auth.ErrInvalidCredentials):
		s.limiter.RecordLoginFailure(ip, keyDigest)
		s.logger.Info("login_failed",
			slog.String("request_id", requestIDFromContext(r.Context())),
			slog.String("error_class", "invalid_credentials"))
		writeError(w, http.StatusUnauthorized, "auth_failed", "名字或卡片密钥不正确，请检查卡片后重试")
		return
	case err != nil:
		s.logger.Error("login_error",
			slog.String("request_id", requestIDFromContext(r.Context())),
			slog.String("error_class", "internal"))
		writeError(w, http.StatusInternalServerError, "internal_error", "服务暂时不可用，请稍后再试")
		return
	}

	s.logger.Info("login_success",
		slog.String("request_id", requestIDFromContext(r.Context())),
		slog.Bool("first_bind", result.FirstBind),
		slog.String("internal_key_ref", result.AccessKeyRef))
	setSessionCookie(w, result.Token, result.ExpiresAt)
	writeJSON(w, http.StatusOK, loginResponse{Session: *s.sessionPayload(&result.Group)})
}

// handleLogout 删除当前会话并清除 Cookie；无会话时幂等成功。
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if token, ok := readSessionCookie(r); ok {
		if err := s.auth.Logout(r.Context(), token); err != nil {
			s.logger.Error("logout_error",
				slog.String("request_id", requestIDFromContext(r.Context())),
				slog.String("error_class", "internal"))
			writeError(w, http.StatusInternalServerError, "internal_error", "服务暂时不可用，请稍后再试")
			return
		}
	}
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type revealRequest struct {
	GiftID string `json:"giftId"`
}

type revealResponse struct {
	Code string `json:"code"`
}

// maxGiftIDLen 是 giftId 的长度兜底（内部 ID 为 32 位 hex，留一倍余量防滥用）。
const maxGiftIDLen = 64

// handleRevealGift 按 giftId 返回本组某份礼品的明文激活码（WG-501，§9.1）。
// 必须持有有效会话；giftId 再次校验归属当前会话同行组，不信任前端列表。
// 不存在/跨组/已停用统一 404 gift_unavailable，不区分原因；响应一律 no-store。
func (s *Server) handleRevealGift(w http.ResponseWriter, r *http.Request) {
	token, ok := readSessionCookie(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "session_invalid", "登录状态已失效，请重新登录")
		return
	}
	payload, err := s.auth.ResolveSession(r.Context(), token)
	if err != nil {
		if !errors.Is(err, auth.ErrSessionInvalid) {
			s.logger.Error("reveal_session_error",
				slog.String("request_id", requestIDFromContext(r.Context())),
				slog.String("error_class", "session_resolve"))
			writeError(w, http.StatusInternalServerError, "internal_error", "服务暂时不可用，请稍后再试")
			return
		}
		clearSessionCookie(w)
		writeError(w, http.StatusUnauthorized, "session_invalid", "登录状态已失效，请重新登录")
		return
	}

	var req revealRequest
	if !decodeJSONBody(w, r, &req) {
		return
	}
	if req.GiftID == "" || len(req.GiftID) > maxGiftIDLen {
		writeError(w, http.StatusBadRequest, "bad_request", "请求不合法")
		return
	}

	code, err := s.gift.RevealCode(r.Context(), payload.GroupID, req.GiftID)
	switch {
	case errors.Is(err, gift.ErrUnavailable):
		writeError(w, http.StatusNotFound, "gift_unavailable", "礼物信息暂时无法读取，请刷新后重试")
		return
	case err != nil:
		s.logger.Error("gift_reveal_error",
			slog.String("request_id", requestIDFromContext(r.Context())),
			slog.String("error_class", "gift_reveal"))
		writeError(w, http.StatusInternalServerError, "internal_error", "服务暂时不可用，请稍后再试")
		return
	}

	s.logger.Info("gift_revealed",
		slog.String("request_id", requestIDFromContext(r.Context())))
	writeJSON(w, http.StatusOK, revealResponse{Code: code})
}

// sessionPayload 把 auth.GroupPayload 转成对外 DTO；thanksMessage 为全局致谢语
// 与组专属短句的拼装（无专属短句时只用全局文案，见产品架构 §8.3）。
func (s *Server) sessionPayload(p *auth.GroupPayload) *sessionDTO {
	dto := &sessionDTO{
		Salutation:    p.Salutation,
		CustomMessage: p.CustomMessage,
		ThanksMessage: s.texts.ThanksMessage,
		ExpiresAt:     p.ExpiresAt.UTC().Format(time.RFC3339),
	}
	for _, g := range p.Gifts {
		dto.Gifts = append(dto.Gifts, giftSummaryDTO{
			ID:            g.ID,
			TypeKey:       g.TypeKey,
			Title:         g.Title,
			Subtitle:      g.Subtitle,
			ThemeVariant:  g.ThemeVariant,
			Instructions:  g.Instructions,
			ValidityText:  g.ValidityText,
			RedemptionURL: g.RedemptionURL,
			Ordinal:       g.Ordinal,
		})
	}
	return dto
}

// ---------- 防护与编解码辅助 ----------

// writeError 写出统一错误体（敏感接口响应全部 no-store，由 writeJSON 保证）。
func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, apiErrorBody{Error: apiError{Code: code, Message: message}})
}

// decodeJSONBody 解码 JSON 请求体：8 KB 上限、拒绝未知字段、拒绝尾随数据。
// 失败时已写出错误响应并返回 false。
func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	// §9.2：API 只接受 application/json（允许 charset 等参数）。
	mediaType, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	if strings.TrimSpace(mediaType) != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "请求必须是 JSON")
		return false
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAPIBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "请求体不是合法 JSON 或超出大小限制")
		return false
	}
	if dec.More() {
		writeError(w, http.StatusBadRequest, "bad_request", "请求体包含多余内容")
		return false
	}
	return true
}

// guardAPI 校验 /api 请求的 Host 与 Origin（§10.3 来源校验）：
//   - Host 必须在配置允许清单内（回环地址总是允许，供本地开发与健康检查）；
//   - 非 GET 请求必须携带 Origin，且 Origin 的 host 与请求 Host 一致。
//
// 校验失败统一 404/403，不返回任何业务信息。
func (s *Server) guardAPI(w http.ResponseWriter, r *http.Request) bool {
	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if !s.hostAllowed(host) {
		writeError(w, http.StatusNotFound, "not_found", "Not Found")
		return false
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		origin := r.Header.Get("Origin")
		if origin == "" {
			writeError(w, http.StatusForbidden, "forbidden", "请求来源不合法")
			return false
		}
		originHost := origin
		if i := strings.Index(origin, "://"); i >= 0 {
			originHost = origin[i+3:]
		}
		originHost = strings.TrimSuffix(originHost, "/")
		if oh, _, err := net.SplitHostPort(originHost); err == nil {
			originHost = oh
		}
		if !strings.EqualFold(originHost, host) {
			writeError(w, http.StatusForbidden, "forbidden", "请求来源不合法")
			return false
		}
	}
	return true
}

// hostAllowed 报告 Host 是否被允许：配置清单 + 回环地址。
func (s *Server) hostAllowed(host string) bool {
	host = strings.ToLower(host)
	if host == "localhost" || host == "127.0.0.1" || host == "::1" || host == "[::1]" {
		return true
	}
	for _, h := range s.allowedHosts {
		if strings.ToLower(h) == host {
			return true
		}
	}
	return false
}

// clientIP 取请求来源 IP：nginx 反代后置信 X-Forwarded-For 首跳，否则 RemoteAddr。
// 仅用于内存限流键，不写入日志（§15.1 禁止记录完整 IP）。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, _ := strings.Cut(xff, ","); strings.TrimSpace(first) != "" {
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// ---------- 会话 Cookie ----------

// readSessionCookie 读取并解码会话令牌；Cookie 缺失或编码非法都返回 false。
func readSessionCookie(r *http.Request) ([]byte, bool) {
	c, err := r.Cookie(sessionCookieName)
	if err != nil || c.Value == "" {
		return nil, false
	}
	token, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil || len(token) != 32 {
		return nil, false
	}
	return token, true
}

// setSessionCookie 写入会话 Cookie（§9.4）：Secure/HttpOnly/SameSite=Lax。
// Path 设为 /，由 nginx proxy_cookie_path 统一收编到 /wedding/（部署片段已配置）；
// 本地开发无 nginx 时 Path=/ 也能正常工作。
func setSessionCookie(w http.ResponseWriter, token []byte, expiresAt time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    base64.RawURLEncoding.EncodeToString(token),
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  expiresAt,
		MaxAge:   int(time.Until(expiresAt).Seconds()),
	})
}

// clearSessionCookie 让浏览器立即删除会话 Cookie（属性须与设置时一致）。
func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    "",
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}
