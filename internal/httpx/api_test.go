package httpx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weddinggift/wedding/internal/admin"
	"github.com/weddinggift/wedding/internal/auth"
	"github.com/weddinggift/wedding/internal/domain"
	"github.com/weddinggift/wedding/internal/store"
)

// newAPITestServer 与 newTestServer 相同，但允许注入自定义限流器并返回 DB 便于断言。
func newAPITestServer(t *testing.T, limiter *RateLimiter) (http.Handler, *store.DB) {
	t.Helper()
	ctx := context.Background()

	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "wedding.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ds, errs := admin.LoadDataset(fixturesDir)
	if len(errs) > 0 {
		t.Fatalf("fixtures 解析失败: %v", errs)
	}
	if valErrs := domain.Validate(ds); len(valErrs) > 0 {
		t.Fatalf("fixtures 校验失败: %v", valErrs)
	}
	cx := testCrypto(t)
	if err := db.ImportDataset(ctx, ds, cx); err != nil {
		t.Fatalf("导入 fixtures 失败: %v", err)
	}

	s, err := New(Deps{
		DB:           db,
		Auth:         auth.New(db, cx),
		Crypto:       cx,
		AllowedHosts: []string{"example.com"},
		Limiter:      limiter,
		Texts: Texts{
			CoupleNames:   "示例新人",
			ThanksMessage: "谢谢你的祝福",
			IcpFilingText: "沪ICP备2026017253号-2",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler(), db
}

// loginRequest 发起一次带合法 Host/Origin 的登录请求。
func doLogin(h http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func sessionCookieOf(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	t.Fatal("响应缺少会话 Cookie")
	return nil
}

// TestLoginSuccess 覆盖 WG-403/404 主路径：首次绑定成功、Cookie 属性、礼品摘要下发。
func TestLoginSuccess(t *testing.T) {
	h, _ := newAPITestServer(t, nil)

	rec := doLogin(h, `{"loginName":"王芳","keyCode":"20260001"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	c := sessionCookieOf(t, rec)
	if !c.HttpOnly || !c.Secure || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("Cookie 属性不符合 §9.4: %+v", c)
	}
	if c.Path != "/" {
		t.Errorf("Cookie Path = %q, want /（由 nginx 收编到 /wedding/）", c.Path)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("登录响应必须 no-store，实际 %q", cc)
	}

	var body struct {
		Session struct {
			Salutation    string `json:"salutation"`
			ThanksMessage string `json:"thanksMessage"`
			ExpiresAt     string `json:"expiresAt"`
			Gifts         []struct {
				ID           string `json:"id"`
				Title        string `json:"title"`
				Instructions string `json:"instructions"`
			} `json:"gifts"`
		} `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Session.Salutation != "王芳" {
		t.Errorf("salutation = %q, want 王芳", body.Session.Salutation)
	}
	if len(body.Session.Gifts) != 1 || body.Session.Gifts[0].Title != "咖啡券" {
		t.Errorf("G001 应只有一份咖啡券，实际 %+v", body.Session.Gifts)
	}
	if strings.Contains(rec.Body.String(), "FAKECOFFEE001") {
		t.Error("摘要响应绝不允许包含明文礼品码")
	}
}

// TestLoginUnifiedFailure 是 G4 关键安全测试：名称错 / 密钥错 / 跨组 / 组已有密钥，
// 外部响应（状态码 + 响应体）必须完全一致，不可区分原因。
func TestLoginUnifiedFailure(t *testing.T) {
	h, _ := newAPITestServer(t, nil)

	// 先建立基线：K001 绑定 G001。
	if rec := doLogin(h, `{"loginName":"王芳","keyCode":"20260001"}`); rec.Code != http.StatusOK {
		t.Fatalf("基线登录失败: %s", rec.Body.String())
	}

	cases := map[string]string{
		"名称不存在":    `{"loginName":"不存在的人","keyCode":"20260002"}`,
		"密钥不存在":    `{"loginName":"张伟","keyCode":"99999999"}`,
		"名称与密钥都无效": `{"loginName":"不存在的人","keyCode":"99999999"}`,
		"密钥已绑定其他组": `{"loginName":"张伟","keyCode":"20260001"}`,
		"组已有其他密钥":  `{"loginName":"王芳","keyCode":"20260002"}`,
		"全角数字密钥":   `{"loginName":"张伟","keyCode":"２０２６０００２"}`,
	}
	var baseline string
	var baselineCode int
	for name, payload := range cases {
		rec := doLogin(h, payload)
		if name == "全角数字密钥" {
			// 全角数字违反公开格式规则，允许返回 400 格式错误（不泄漏身份信息）。
			if rec.Code != http.StatusBadRequest {
				t.Errorf("%s: status = %d, want 400", name, rec.Code)
			}
			continue
		}
		if baseline == "" {
			baseline = rec.Body.String()
			baselineCode = rec.Code
			continue
		}
		if rec.Code != baselineCode || rec.Body.String() != baseline {
			t.Errorf("%s: 响应与基线不一致（%d %s vs %d %s），可能泄漏失败原因",
				name, rec.Code, rec.Body.String(), baselineCode, baseline)
		}
		for _, c := range rec.Result().Cookies() {
			if c.Name == sessionCookieName && c.MaxAge > 0 {
				t.Errorf("%s: 失败响应不得设置会话 Cookie", name)
			}
		}
	}
	if baselineCode != http.StatusUnauthorized {
		t.Errorf("统一鉴权失败应为 401，实际 %d", baselineCode)
	}
}

// TestLoginInvalidInput 格式错误（公开规则）：400，不计入身份失败语义。
func TestLoginInvalidInput(t *testing.T) {
	h, _ := newAPITestServer(t, nil)

	for name, payload := range map[string]string{
		"密钥不足 8 位": `{"loginName":"王芳","keyCode":"1234567"}`,
		"密钥含字母":    `{"loginName":"王芳","keyCode":"2026000a"}`,
		"密钥含空格":    `{"loginName":"王芳","keyCode":"2026 001"}`,
		"名称为空":     `{"loginName":"   ","keyCode":"20260002"}`,
	} {
		rec := doLogin(h, payload)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, rec.Code)
		}
	}
}

// TestBootstrapSessionLifecycle 覆盖 bootstrap 无会话/有会话与 logout 后失效。
func TestBootstrapSessionLifecycle(t *testing.T) {
	h, _ := newAPITestServer(t, nil)

	// 无会话：session 为 null，服务文案存在。
	req := httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("bootstrap status = %d", rec.Code)
	}
	var boot struct {
		Service struct {
			CoupleNames   string `json:"coupleNames"`
			IcpFilingText string `json:"icpFilingText"`
		} `json:"service"`
		Session json.RawMessage `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &boot); err != nil {
		t.Fatal(err)
	}
	if string(boot.Session) != "null" {
		t.Errorf("无会话时 session 应为 null，实际 %s", boot.Session)
	}
	if boot.Service.CoupleNames == "" || boot.Service.IcpFilingText == "" {
		t.Error("bootstrap 应携带文案配置（新人称呼与备案号）")
	}

	// 登录获得会话。
	loginRec := doLogin(h, `{"loginName":"阿伟","keyCode":"20260002"}`)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("登录失败: %s", loginRec.Body.String())
	}
	cookie := sessionCookieOf(t, loginRec)

	// 有会话：bootstrap 返回同行组与三份礼品（G002 含实例覆盖）。
	req = httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var boot2 struct {
		Session struct {
			Salutation    string `json:"salutation"`
			CustomMessage string `json:"customMessage"`
			Gifts         []struct {
				Title         string `json:"title"`
				Instructions  string `json:"instructions"`
				RedemptionURL string `json:"redemptionUrl"`
			} `json:"gifts"`
		} `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &boot2); err != nil {
		t.Fatal(err)
	}
	if boot2.Session.Salutation != "张伟 & 李娜 一家" {
		t.Errorf("salutation = %q", boot2.Session.Salutation)
	}
	if boot2.Session.CustomMessage == "" {
		t.Error("G002 配置了专属短句，应下发")
	}
	if len(boot2.Session.Gifts) != 3 {
		t.Fatalf("G002 应有 3 份礼品，实际 %d", len(boot2.Session.Gifts))
	}
	// 第三份（咖啡券）有实例级覆盖：兑换说明与链接必须来自实例而非类型默认。
	g3 := boot2.Session.Gifts[2]
	if !strings.Contains(g3.Instructions, "限定联名") || !strings.Contains(g3.RedemptionURL, "coffee-special") {
		t.Errorf("实例级覆盖未生效: %+v", g3)
	}

	// logout：会话删除 + Cookie 清除；再次 bootstrap 回到 null。
	req = httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.Header.Set("Origin", "http://example.com")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("logout status = %d", rec.Code)
	}
	var cleared bool
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("logout 应清除会话 Cookie")
	}

	req = httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var boot3 struct {
		Session json.RawMessage `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &boot3); err != nil {
		t.Fatal(err)
	}
	if string(boot3.Session) != "null" {
		t.Error("logout 后旧 Cookie 不应再解析出会话")
	}
}

// TestAPIGuards 来源校验：Host 白名单、Origin 必需与一致、Content-Type、请求体上限。
func TestAPIGuards(t *testing.T) {
	h, _ := newAPITestServer(t, nil)

	// Host 不在白名单 → 404。
	req := httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	req.Host = "evil.example.org"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("非法 Host 应 404，实际 %d", rec.Code)
	}

	// POST 缺 Origin → 403。
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"loginName":"王芳","keyCode":"20260001"}`))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("缺 Origin 的 POST 应 403，实际 %d", rec.Code)
	}

	// Origin 与 Host 不一致 → 403。
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"loginName":"王芳","keyCode":"20260001"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example.org")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("跨源 POST 应 403，实际 %d", rec.Code)
	}

	// 错误 Content-Type → 415。
	req = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"loginName":"王芳","keyCode":"20260001"}`))
	req.Header.Set("Content-Type", "text/plain")
	req.Header.Set("Origin", "http://example.com")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Errorf("非 JSON Content-Type 应 415，实际 %d", rec.Code)
	}

	// 未知字段 → 400。
	rec = doLogin(h, `{"loginName":"王芳","keyCode":"20260001","extra":1}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("未知字段应 400，实际 %d", rec.Code)
	}

	// 超 8KB 请求体 → 400。
	big := `{"loginName":"` + strings.Repeat("x", 9*1024) + `","keyCode":"20260001"}`
	rec = doLogin(h, big)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("超限请求体应 400，实际 %d", rec.Code)
	}

	// 错误方法：GET 打登录端点会被 "GET /" 子树兜底成 404（不暴露 API 存在性）；
	// POST 打 bootstrap 端点则为标准 405。
	req = httptest.NewRequest(http.MethodGet, "/api/auth/login", nil)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET 打 POST 端点应 404，实际 %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/bootstrap", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.com")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST 打 GET 端点应 405，实际 %d", rec.Code)
	}
}

// TestLoginRateLimit 限流：同一密钥摘要连续失败达到阈值后拒绝，且成功路径不受失败计数影响。
func TestLoginRateLimit(t *testing.T) {
	// 密钥维度阈值 2、窗口 10 分钟：两次失败后第三次必须 429。
	limiter := NewRateLimiter(10*time.Minute, 60, 2)
	h, _ := newAPITestServer(t, limiter)

	// 同密钥失败两次（名称无效）。
	for i := 0; i < 2; i++ {
		rec := doLogin(h, `{"loginName":"不存在的人","keyCode":"20260002"}`)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("第 %d 次应 401，实际 %d", i+1, rec.Code)
		}
	}
	// 第三次：即使名称与密钥本来有效也被限流。
	rec := doLogin(h, `{"loginName":"王芳","keyCode":"20260002"}`)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("达到密钥阈值应 429，实际 %d", rec.Code)
	}

	// 另一个密钥不受影响。
	rec = doLogin(h, `{"loginName":"王芳","keyCode":"20260001"}`)
	if rec.Code != http.StatusOK {
		t.Errorf("其他密钥不应被限流，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

// TestRateLimiterWindow 滑动窗口：窗口外历史不再计入。
func TestRateLimiterWindow(t *testing.T) {
	l := NewRateLimiter(time.Minute, 2, 1)
	base := time.Now()
	l.now = func() time.Time { return base }

	l.RecordLoginFailure("1.1.1.1", []byte("digest-a"))
	l.RecordLoginFailure("1.1.1.1", []byte("digest-a"))
	if l.AllowLogin("1.1.1.1", []byte("digest-a")) {
		t.Fatal("窗口内达到阈值应拒绝")
	}
	// 时间推进超过窗口后恢复允许。
	l.now = func() time.Time { return base.Add(2 * time.Minute) }
	if !l.AllowLogin("1.1.1.1", []byte("digest-a")) {
		t.Fatal("窗口滑过后应恢复允许")
	}
}

// TestExpiredSessionRejectedByAPI 会话过期后 bootstrap 返回 null 并清 Cookie。
func TestExpiredSessionRejectedByAPI(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "wedding.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ds, errs := admin.LoadDataset(fixturesDir)
	if len(errs) > 0 {
		t.Fatal(errs)
	}
	cx := testCrypto(t)
	if err := db.ImportDataset(ctx, ds, cx); err != nil {
		t.Fatal(err)
	}

	// 可控时钟：登录在 T0，bootstrap 在 T0+31 天。
	now := time.Date(2026, 9, 12, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	s, err := New(Deps{
		DB:           db,
		Auth:         auth.NewWithClock(db, cx, clock),
		Crypto:       cx,
		AllowedHosts: []string{"example.com"},
	})
	if err != nil {
		t.Fatal(err)
	}
	h := s.Handler()

	rec := doLogin(h, `{"loginName":"王芳","keyCode":"20260001"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("登录失败: %s", rec.Body.String())
	}
	cookie := sessionCookieOf(t, rec)

	now = now.Add(31 * 24 * time.Hour)
	req := httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var boot struct {
		Session json.RawMessage `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &boot); err != nil {
		t.Fatal(err)
	}
	if string(boot.Session) != "null" {
		t.Error("过期会话不应再解析成功")
	}
}
