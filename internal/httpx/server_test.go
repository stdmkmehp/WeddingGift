package httpx

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/weddinggift/wedding/internal/admin"
	"github.com/weddinggift/wedding/internal/auth"
	"github.com/weddinggift/wedding/internal/cryptox"
	"github.com/weddinggift/wedding/internal/domain"
	"github.com/weddinggift/wedding/internal/store"
)

// fixturesDir 指向 M0 虚构数据包（无真实数据）。
const fixturesDir = "../../testdata/fixtures"

// testCrypto 用固定主秘密构造测试加密服务（与 cryptox 测试向量同一主秘密）。
func testCrypto(t *testing.T) *cryptox.Service {
	t.Helper()
	master, err := hex.DecodeString("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f")
	if err != nil {
		t.Fatal(err)
	}
	svc, err := cryptox.New(master)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// newTestServer 装配完整依赖的测试服务：临时库 + fixtures + 真实 auth 服务。
// allowedHosts 固定允许 httptest 默认的 example.com。
func newTestServer(t *testing.T) http.Handler {
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
	})
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	return s.Handler()
}

func TestIndexServesPlaceholderHome(t *testing.T) {
	h := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q, want text/html", ct)
	}
	body, _ := io.ReadAll(rec.Body)
	if len(body) == 0 {
		t.Error("empty body")
	}
}

func TestUnknownPathReturns404(t *testing.T) {
	h := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestHealthzReturnsOK(t *testing.T) {
	h := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if payload["status"] != "ok" {
		t.Errorf("status field = %v, want ok", payload["status"])
	}
	if payload["database"] != "ok" {
		t.Errorf("database field = %v, want ok（M4 起 healthz 含 SQLite 只读检查）", payload["database"])
	}
}

func TestSecurityHeadersPresent(t *testing.T) {
	h := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	wantHeaders := map[string]string{
		"X-Content-Type-Options": "nosniff",
		"Referrer-Policy":        "no-referrer",
		"X-Frame-Options":        "DENY",
	}
	for k, want := range wantHeaders {
		if got := rec.Header().Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}
	if csp := rec.Header().Get("Content-Security-Policy"); csp == "" {
		t.Error("missing Content-Security-Policy header")
	}
	if rid := rec.Header().Get("X-Request-ID"); rid == "" {
		t.Error("missing X-Request-ID header")
	}
}

func TestStaticAssetServed(t *testing.T) {
	h := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/assets/app.css", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	body, _ := io.ReadAll(rec.Body)
	if len(body) == 0 {
		t.Error("empty css body")
	}
}
