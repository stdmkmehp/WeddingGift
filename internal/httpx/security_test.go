// M6（WG-601/WG-602）安全回归测试：SQL 注入、XSS 转义、日志泄密、
// healthz 异常分支、密钥停用后的 HTTP 层会话失效，以及完整来宾旅程 E2E。
// 这些用例是 docs/06-m6-security-review.md 安全清单的自动化证据。
package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
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

// newSecServer 装配日志可注入的测试服务；limiter 为 nil 时使用默认阈值。
func newSecServer(t *testing.T, logger *slog.Logger, limiter *RateLimiter) (http.Handler, *store.DB) {
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
		Logger:       logger,
		DB:           db,
		Auth:         auth.New(db, cx),
		Crypto:       cx,
		AllowedHosts: []string{"example.com"},
		Limiter:      limiter,
		Texts:        Texts{CoupleNames: "示例新人", ThanksMessage: "谢谢你的祝福"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return s.Handler(), db
}

// doBootstrap 发起一次 bootstrap 请求；cookie 可为 nil。
func doBootstrap(h http.Handler, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/bootstrap", nil)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// sessionOf 解析 bootstrap 响应的 session 字段原文（"null" 或对象）。
func sessionOf(t *testing.T, rec *httptest.ResponseRecorder) json.RawMessage {
	t.Helper()
	var body struct {
		Session json.RawMessage `json:"session"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Session
}

// TestLoginSQLInjection 登录名称注入串：参数化查询下统一 401 且与"名称不存在"
// 基线逐字节一致；注入后正常登录仍可用（数据库未被破坏）。
func TestLoginSQLInjection(t *testing.T) {
	h, _ := newSecServer(t, slog.Default(), nil)

	baseline := doLogin(h, `{"loginName":"不存在的人","keyCode":"20260002"}`)
	if baseline.Code != http.StatusUnauthorized {
		t.Fatalf("基线应 401，实际 %d", baseline.Code)
	}

	injections := map[string]string{
		"恒真注入":  `{"loginName":"' OR '1'='1","keyCode":"20260002"}`,
		"注释截断":  `{"loginName":"王芳' OR 1=1 --","keyCode":"20260002"}`,
		"堆叠删除":  `{"loginName":"王芳'; DROP TABLE member; --","keyCode":"20260002"}`,
		"密钥位注入": `{"loginName":"王芳","keyCode":"20260002' OR '1'='1"}`,
	}
	for name, payload := range injections {
		rec := doLogin(h, payload)
		if rec.Code == http.StatusBadRequest {
			// 密钥位注入触发公开格式规则，允许 400（不泄漏身份信息）。
			continue
		}
		if rec.Code != baseline.Code || rec.Body.String() != baseline.Body.String() {
			t.Errorf("%s: 响应与统一失败基线不一致（%d %s），可能泄漏注入结果", name, rec.Code, rec.Body.String())
		}
	}

	// 注入尝试后数据库与正常流程不受影响。
	if rec := doLogin(h, `{"loginName":"王芳","keyCode":"20260001"}`); rec.Code != http.StatusOK {
		t.Errorf("注入尝试后正常登录应成功，实际 %d: %s", rec.Code, rec.Body.String())
	}
}

// TestBootstrapEscapesHTML 数据中的 HTML/脚本内容经 JSON 响应输出时必须被转义，
// 浏览器端不会得到可执行标记（配合前端 textContent 渲染与 CSP 形成纵深）。
func TestBootstrapEscapesHTML(t *testing.T) {
	h, db := newSecServer(t, slog.Default(), nil)

	payload := `<script>alert(1)</script>&"onload"`
	if _, err := db.ExecContext(context.Background(),
		`UPDATE recipient_group SET salutation = ? WHERE external_ref = 'G001'`, payload); err != nil {
		t.Fatal(err)
	}

	rec := doLogin(h, `{"loginName":"王芳","keyCode":"20260001"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("登录失败: %s", rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, "<script>") || strings.Contains(body, `&"onload"`) {
		t.Errorf("响应包含未转义的 HTML 内容: %s", body)
	}
	if !strings.Contains(body, `\u003cscript\u003e`) {
		t.Errorf("响应应包含 JSON 转义后的内容（\\u003c 等），实际: %s", body)
	}

	// bootstrap 会话通道同样转义。
	cookie := sessionCookieOf(t, rec)
	boot := doBootstrap(h, cookie)
	if strings.Contains(boot.Body.String(), "<script>") {
		t.Error("bootstrap 响应包含未转义的 HTML 内容")
	}
}

// TestLogsDoNotLeakSecrets 是 WG-602 日志泄密核对的核心证据：跑完整业务流程
// （登录成功/失败/限流/bootstrap/reveal/logout），结构化日志中不得出现
// 姓名、昵称、明文密钥、明文礼品码、会话令牌或 Cookie 值。
func TestLogsDoNotLeakSecrets(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	// 密钥维度阈值 1，便于在一次失败登录后触发限流日志。
	h, _ := newSecServer(t, logger, NewRateLimiter(10*time.Minute, 60, 1))

	// 1. 登录成功（姓名 + 密钥 + 后续 Cookie）。
	loginOK := doLogin(h, `{"loginName":"王芳","keyCode":"20260001"}`)
	if loginOK.Code != http.StatusOK {
		t.Fatalf("登录失败: %s", loginOK.Body.String())
	}
	cookie := sessionCookieOf(t, loginOK)

	// 2. 登录失败（错误姓名、错误密钥、错误昵称）。
	doLogin(h, `{"loginName":"张伟","keyCode":"99999999"}`)
	doLogin(h, `{"loginName":"阿伟","keyCode":"20260001"}`)

	// 3. 限流（密钥维度阈值 1：20260001 已失败一次，再次尝试触发 429）。
	doLogin(h, `{"loginName":"王芳","keyCode":"20260001"}`)

	// 4. bootstrap（有会话与无会话）。
	doBootstrap(h, cookie)
	doBootstrap(h, nil)

	// 5. reveal 本组礼品码（明文只出现在响应体，绝不许进日志）。
	_, ids := loginGiftIDs(t, h, "李娜", "20260002")
	cookie2Rec := doLogin(h, `{"loginName":"李娜","keyCode":"20260002"}`)
	cookie2 := sessionCookieOf(t, cookie2Rec)
	for _, id := range ids {
		doReveal(h, cookie2, `{"giftId":"`+id+`"}`)
	}
	// 跨组 reveal（失败路径日志）。
	doReveal(h, cookie2, `{"giftId":"00000000000000000000000000000000"}`)

	// 6. logout。
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.Header.Set("Origin", "http://example.com")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// ---- 断言日志无敏感值 ----
	logs := logBuf.String()
	if logs == "" {
		t.Fatal("测试流程应产生日志")
	}
	forbidden := []string{
		// 姓名与昵称（fixtures 全部登录名称）。
		"王芳", "芳芳", "小芳", "张伟", "阿伟", "伟哥", "李娜", "陈强", "刘洋",
		// 明文卡片密钥。
		"20260001", "20260002", "20260003", "99999999",
		// 明文礼品码。
		"FAKECOFFEE001", "FAKETRAVEL002", "FAKEMOVIE003", "FAKECOFFEE004", "FAKETRAVEL005", "FAKEMOVIE006",
		// 会话令牌 / Cookie 值。
		cookie.Value, cookie2.Value,
	}
	for _, f := range forbidden {
		if f != "" && strings.Contains(logs, f) {
			t.Errorf("日志泄漏敏感值 %q", f)
		}
	}
}

// TestHealthzUnhealthyWhenDBDown 数据库不可用时 healthz 必须 503（§15.2），
// Docker healthcheck 据此判定容器不健康。
func TestHealthzUnhealthyWhenDBDown(t *testing.T) {
	h, db := newSecServer(t, slog.Default(), nil)

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("健康时应 200，实际 %d", rec.Code)
	}

	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("数据库断开后应 503，实际 %d", rec.Code)
	}
	var body struct {
		Status   string `json:"status"`
		Database string `json:"database"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "unhealthy" || body.Database == "ok" {
		t.Errorf("异常 healthz 响应不正确: %+v", body)
	}
}

// TestBootstrapAfterKeyRevoke 密钥停用后（PA-12 的 HTTP 层）：既有会话的
// bootstrap 立即返回 session null 并清除 Cookie，reveal 同样失效。
func TestBootstrapAfterKeyRevoke(t *testing.T) {
	h, db := newSecServer(t, slog.Default(), nil)

	rec := doLogin(h, `{"loginName":"王芳","keyCode":"20260001"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("登录失败: %s", rec.Body.String())
	}
	cookie := sessionCookieOf(t, rec)

	if _, err := db.RevokeAccessKey(context.Background(), "K001"); err != nil {
		t.Fatal(err)
	}

	boot := doBootstrap(h, cookie)
	if string(sessionOf(t, boot)) != "null" {
		t.Error("密钥停用后 bootstrap 不应再解析出会话")
	}
	var cleared bool
	for _, c := range boot.Result().Cookies() {
		if c.Name == sessionCookieName && c.MaxAge < 0 {
			cleared = true
		}
	}
	if !cleared {
		t.Error("密钥停用后的 bootstrap 应清除会话 Cookie")
	}

	if rec := doReveal(h, cookie, `{"giftId":"abc"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("密钥停用后 reveal 应 401，实际 %d", rec.Code)
	}
}

// TestGuestJourney 完整来宾旅程 E2E（WG-601）：把 PA-02/04/05/06/07/10 串成
// 一条不依赖其他测试执行顺序的独立链路——初始化、多登录名称、首绑、同组
// 再登录、跨组拒绝、第二密钥拒绝、多礼品逐份揭示、退出与旧会话失效。
func TestGuestJourney(t *testing.T) {
	h, _ := newSecServer(t, slog.Default(), nil)

	// 1. 初始化：无会话时 bootstrap 返回服务文案 + session null。
	boot := doBootstrap(h, nil)
	if boot.Code != http.StatusOK || string(sessionOf(t, boot)) != "null" {
		t.Fatalf("初始 bootstrap 异常: %d %s", boot.Code, boot.Body.String())
	}

	// 2. 首绑（PA-04）：张伟用本名 + K002 登录，进入 G002 礼品列表。
	loginA := doLogin(h, `{"loginName":"张伟","keyCode":"20260002"}`)
	if loginA.Code != http.StatusOK {
		t.Fatalf("首绑登录失败: %s", loginA.Body.String())
	}
	cookieA := sessionCookieOf(t, loginA)

	// 3. 同组多登录名称（PA-02/PA-05）：昵称"阿伟"解析到同一成员，
	//    同密钥再登录成功，看到与本名相同的同行组与礼品列表。
	loginA2 := doLogin(h, `{"loginName":"阿伟","keyCode":"20260002"}`)
	if loginA2.Code != http.StatusOK {
		t.Fatalf("同组昵称再登录失败: %s", loginA2.Body.String())
	}
	cookieA2 := sessionCookieOf(t, loginA2)
	for i, c := range []*http.Cookie{cookieA, cookieA2} {
		b := doBootstrap(h, c)
		var payload struct {
			Session struct {
				Salutation string `json:"salutation"`
				Gifts      []struct {
					Title         string `json:"title"`
					Instructions  string `json:"instructions"`
					RedemptionURL string `json:"redemptionUrl"`
				} `json:"gifts"`
			} `json:"session"`
		}
		if err := json.Unmarshal(b.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Session.Salutation != "张伟 & 李娜 一家" {
			t.Fatalf("会话 %d 称呼错误: %q", i, payload.Session.Salutation)
		}
		if len(payload.Session.Gifts) != 3 {
			t.Fatalf("G002 应有 3 份礼品，实际 %d", len(payload.Session.Gifts))
		}
	}

	// 4. 同组另一成员（PA-05）：李娜用本名 + 同密钥登录成功。
	loginB := doLogin(h, `{"loginName":"李娜","keyCode":"20260002"}`)
	if loginB.Code != http.StatusOK {
		t.Fatalf("同组成员登录失败: %s", loginB.Body.String())
	}

	// 5. 跨组拒绝（PA-06）：K002 已绑 G002，搭配 G001 的登录名称统一失败。
	if rec := doLogin(h, `{"loginName":"王芳","keyCode":"20260002"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("跨组登录应 401，实际 %d", rec.Code)
	}

	// 6. 第二密钥拒绝（PA-07）：K001 绑定 G001 后，未绑定的 K003 搭配 G001 名称失败。
	if rec := doLogin(h, `{"loginName":"王芳","keyCode":"20260001"}`); rec.Code != http.StatusOK {
		t.Fatalf("G001 首绑失败: %s", rec.Body.String())
	}
	if rec := doLogin(h, `{"loginName":"王芳","keyCode":"20260003"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("组已有密钥时第二密钥应 401，实际 %d", rec.Code)
	}

	// 7. 多礼品逐份揭示（PA-10）：三份均可独立 reveal，实例覆盖的咖啡券内容正确。
	_, ids := loginGiftIDs(t, h, "伟哥", "20260002")
	cookieC := sessionCookieOf(t, doLogin(h, `{"loginName":"伟哥","keyCode":"20260002"}`))
	wantCodes := []string{"FAKETRAVEL002", "FAKEMOVIE003", "FAKECOFFEE004"}
	for i, id := range ids {
		rec := doReveal(h, cookieC, `{"giftId":"`+id+`"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("第 %d 份 reveal 失败: %d", i+1, rec.Code)
		}
		var body struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Code != wantCodes[i] {
			t.Errorf("第 %d 份礼品码 = %q, want %q", i+1, body.Code, wantCodes[i])
		}
	}

	// 8. 退出：会话删除、Cookie 清除；旧 Cookie 的 bootstrap 回 null、reveal 401。
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.Header.Set("Origin", "http://example.com")
	req.AddCookie(cookieA)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("退出失败: %d", rec.Code)
	}
	if b := doBootstrap(h, cookieA); string(sessionOf(t, b)) != "null" {
		t.Error("退出后旧 Cookie 不应再解析出会话")
	}
	if rec := doReveal(h, cookieA, `{"giftId":"`+ids[0]+`"}`); rec.Code != http.StatusUnauthorized {
		t.Errorf("退出后 reveal 应 401，实际 %d", rec.Code)
	}

	// 9. 另一会话不受退出影响（会话相互独立）。
	if b := doBootstrap(h, cookieA2); string(sessionOf(t, b)) == "null" {
		t.Error("同组另一会话应保持有效")
	}
}
