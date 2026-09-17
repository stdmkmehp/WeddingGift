package auth_test

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weddinggift/wedding/internal/admin"
	"github.com/weddinggift/wedding/internal/auth"
	"github.com/weddinggift/wedding/internal/cryptox"
	"github.com/weddinggift/wedding/internal/domain"
	"github.com/weddinggift/wedding/internal/report"
	"github.com/weddinggift/wedding/internal/store"
)

const fixturesDir = "../../testdata/fixtures"

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

// newEnv 装配测试环境：临时库 + fixtures + auth 服务（可控时钟）。
func newEnv(t *testing.T) (context.Context, *store.DB, *auth.Service, *cryptox.Service, *time.Time) {
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
	current := time.Date(2026, 9, 12, 12, 0, 0, 0, time.UTC)
	return ctx, db, auth.NewWithClock(db, cx, func() time.Time { return current }), cx, &current
}

// TestLoginInputValidation 公开格式规则：全角数字、分隔符、长度不符、空名称、超长名称。
func TestLoginInputValidation(t *testing.T) {
	ctx, _, svc, _, _ := newEnv(t)

	cases := map[string][2]string{
		"全角数字":   {"王芳", "２０２６０００１"},
		"密钥含空格":  {"王芳", "2026 001"},
		"密钥含连字符": {"王芳", "2026-001"},
		"密钥 7 位": {"王芳", "2026001"},
		"密钥 9 位": {"王芳", "202600011"},
		"密钥含字母":  {"王芳", "2026000a"},
		"空名称":    {"   ", "20260001"},
		"超长名称":   {strings.Repeat("名", 65), "20260001"},
	}
	for name, c := range cases {
		_, err := svc.Login(ctx, c[0], c[1])
		if !errors.Is(err, auth.ErrInvalidInput) {
			t.Errorf("%s: 应返回 ErrInvalidInput，实际 %v", name, err)
		}
	}
}

// TestLoginUnifiedError 名称无效或密钥无效/停用统一归一到 ErrInvalidCredentials，
// 外部无法区分具体原因。
func TestLoginUnifiedError(t *testing.T) {
	ctx, _, svc, _, _ := newEnv(t)

	if _, err := svc.Login(ctx, "王芳", "20260001"); err != nil {
		t.Fatalf("首次绑定应成功: %v", err)
	}
	cases := map[string][2]string{
		"名称无效": {"不存在的人", "20260002"},
		"密钥无效": {"张伟", "99999999"},
	}
	for name, c := range cases {
		_, err := svc.Login(ctx, c[0], c[1])
		if !errors.Is(err, auth.ErrInvalidCredentials) {
			t.Errorf("%s: 应返回 ErrInvalidCredentials，实际 %v", name, err)
		}
	}
}

// TestLoginKeyBoundElsewhere 名称与密钥各自有效、但密钥已绑定其他同行组时，
// 返回明确的 ErrKeyBoundElsewhere（引导持卡本人联系新郎处理），而非统一模糊错误。
func TestLoginKeyBoundElsewhere(t *testing.T) {
	ctx, _, svc, _, _ := newEnv(t)

	// K001 绑定 G001（王芳）。
	if _, err := svc.Login(ctx, "王芳", "20260001"); err != nil {
		t.Fatalf("首次绑定应成功: %v", err)
	}
	// 已绑定的 K001 搭配 G002 的登录名称：明确的已绑定错误。
	if _, err := svc.Login(ctx, "张伟", "20260001"); !errors.Is(err, auth.ErrKeyBoundElsewhere) {
		t.Errorf("跨组已绑定密钥应返回 ErrKeyBoundElsewhere，实际 %v", err)
	}
}

// TestLoginSecondKeySameGroup 一个同行组可绑定多个密钥（2026-09-13 决策调整）：
// 同组使用不同密钥登录，解析到同一同行组、看到同一份回礼。
func TestLoginSecondKeySameGroup(t *testing.T) {
	ctx, _, svc, _, _ := newEnv(t)

	first, err := svc.Login(ctx, "王芳", "20260001")
	if err != nil {
		t.Fatalf("首个密钥绑定应成功: %v", err)
	}
	second, err := svc.Login(ctx, "王芳", "20260002")
	if err != nil {
		t.Fatalf("第二个密钥绑定同组应成功: %v", err)
	}
	if !second.FirstBind {
		t.Error("第二个密钥首次绑定应标记 FirstBind")
	}
	if first.Group.GroupID != second.Group.GroupID {
		t.Errorf("两个密钥应解析到同一同行组: %q vs %q", first.Group.GroupID, second.Group.GroupID)
	}
	if len(first.Group.Gifts) != len(second.Group.Gifts) {
		t.Fatalf("同组不同密钥看到的礼品数量应一致: %d vs %d", len(first.Group.Gifts), len(second.Group.Gifts))
	}
	for i := range first.Group.Gifts {
		if first.Group.Gifts[i].ID != second.Group.Gifts[i].ID {
			t.Errorf("第 %d 份礼品不一致: %q vs %q", i+1, first.Group.Gifts[i].ID, second.Group.Gifts[i].ID)
		}
	}
}

// TestLoginResultAndSessionRoundTrip 登录成功返回明文令牌与组数据；令牌可解析会话。
func TestLoginResultAndSessionRoundTrip(t *testing.T) {
	ctx, _, svc, _, clock := newEnv(t)

	res, err := svc.Login(ctx, "张伟", "20260002")
	if err != nil {
		t.Fatal(err)
	}
	if !res.FirstBind {
		t.Error("首次登录应标记 FirstBind")
	}
	if len(res.Token) != 32 {
		t.Errorf("会话令牌应为 256 bit，实际 %d 字节", len(res.Token))
	}
	// 有效期以注入时钟为基准断言，避免与真实时间漂移耦合。
	if want := clock.Add(auth.SessionTTL); !res.ExpiresAt.Equal(want) {
		t.Errorf("会话有效期应为登录时刻 + %v（%s），实际 %s", auth.SessionTTL, want, res.ExpiresAt)
	}
	if len(res.Group.Gifts) != 3 {
		t.Errorf("G002 应返回 3 份礼品摘要，实际 %d", len(res.Group.Gifts))
	}

	payload, err := svc.ResolveSession(ctx, res.Token)
	if err != nil {
		t.Fatalf("会话解析失败: %v", err)
	}
	if payload.Salutation != "张伟 & 李娜 一家" {
		t.Errorf("salutation = %q", payload.Salutation)
	}
	if payload.CustomMessage == "" {
		t.Error("G002 专属短句应随会话下发")
	}
}

// TestSessionExpiry 固定 30 天有效期：T+30 天内有效，T+30 天后失效。
func TestSessionExpiry(t *testing.T) {
	ctx, _, svc, _, clock := newEnv(t)

	res, err := svc.Login(ctx, "王芳", "20260001")
	if err != nil {
		t.Fatal(err)
	}
	*clock = clock.Add(29 * 24 * time.Hour)
	if _, err := svc.ResolveSession(ctx, res.Token); err != nil {
		t.Errorf("29 天后会话应仍有效: %v", err)
	}
	*clock = clock.Add(2 * 24 * time.Hour)
	if _, err := svc.ResolveSession(ctx, res.Token); !errors.Is(err, auth.ErrSessionInvalid) {
		t.Errorf("31 天后会话应失效，实际 %v", err)
	}
}

// TestLogoutAndTokenTamper 退出后会话失效；伪造令牌永远无效。
func TestLogoutAndTokenTamper(t *testing.T) {
	ctx, _, svc, _, _ := newEnv(t)

	res, err := svc.Login(ctx, "王芳", "20260001")
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Logout(ctx, res.Token); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveSession(ctx, res.Token); !errors.Is(err, auth.ErrSessionInvalid) {
		t.Errorf("退出后会话应失效，实际 %v", err)
	}
	// 重复退出幂等。
	if err := svc.Logout(ctx, res.Token); err != nil {
		t.Errorf("重复退出不应报错: %v", err)
	}

	// 伪造令牌与长度非法令牌。
	if _, err := svc.ResolveSession(ctx, make([]byte, 32)); !errors.Is(err, auth.ErrSessionInvalid) {
		t.Errorf("伪造令牌应无效，实际 %v", err)
	}
	if _, err := svc.ResolveSession(ctx, []byte("short")); !errors.Is(err, auth.ErrSessionInvalid) {
		t.Errorf("长度非法令牌应无效，实际 %v", err)
	}
}

// TestRevokedKeyInvalidatesSession 密钥停用后既有会话失效、不能再登录（PA-12）。
func TestRevokedKeyInvalidatesSession(t *testing.T) {
	ctx, db, svc, _, _ := newEnv(t)

	res, err := svc.Login(ctx, "王芳", "20260001")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.RevokeAccessKey(ctx, "K001"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ResolveSession(ctx, res.Token); !errors.Is(err, auth.ErrSessionInvalid) {
		t.Errorf("密钥停用后会话应失效，实际 %v", err)
	}
	if _, err := svc.Login(ctx, "王芳", "20260001"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("停用密钥再登录应统一失败，实际 %v", err)
	}
}

// TestLoginFailureRecording 登录失败写入内存缓冲：类别、原始输入、掩码与
// 归属注解齐全；成功登录不记录；未注入缓冲时登录流程不受影响。
func TestLoginFailureRecording(t *testing.T) {
	ctx, _, svc, _, _ := newEnv(t)
	log := report.NewFailureLog(100, 50)
	svc.SetFailureLog(log)

	// 成功登录不记录。
	if _, err := svc.Login(ctx, "王芳", "20260001"); err != nil {
		t.Fatal(err)
	}
	if log.Len() != 0 {
		t.Fatalf("成功登录不应记录，实际 %d 条", log.Len())
	}

	// 密钥已绑定其他同行组：注解应同时识别名字归属与密钥已绑组。
	if _, err := svc.Login(ctx, "张伟", "20260001"); !errors.Is(err, auth.ErrKeyBoundElsewhere) {
		t.Fatalf("应返回 ErrKeyBoundElsewhere: %v", err)
	}
	// 名字无效：密钥注解应识别出有效未绑定密钥。
	if _, err := svc.Login(ctx, "不存在的人", "20260002"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("应返回 ErrInvalidCredentials: %v", err)
	}
	// 密钥无效：名字注解应识别归属组。
	if _, err := svc.Login(ctx, "李娜", "99999999"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Fatalf("应返回 ErrInvalidCredentials: %v", err)
	}
	// 格式非法（密钥非 8 位数字）：不做摘要归属查询。
	if _, err := svc.Login(ctx, "王芳", "1234"); !errors.Is(err, auth.ErrInvalidInput) {
		t.Fatalf("应返回 ErrInvalidInput: %v", err)
	}

	items := log.Snapshot()
	if len(items) != 4 {
		t.Fatalf("应记录 4 条失败，实际 %d", len(items))
	}

	bound := items[0]
	if bound.Outcome != report.FailureKeyBoundElsewhere ||
		bound.NameInput != "张伟" || bound.NameGroup != "G002 张伟 & 李娜 一家" ||
		bound.KeyMasked != "20****01" || !bound.KeyLookedUp ||
		bound.KeyRef != "K001" || bound.KeyBoundGroup != "G001 王芳" {
		t.Errorf("跨组绑定失败记录不对: %+v", bound)
	}

	nameUnknown := items[1]
	if nameUnknown.Outcome != report.FailureNameUnknown ||
		nameUnknown.NameInput != "不存在的人" || nameUnknown.NameGroup != "" ||
		nameUnknown.KeyMasked != "20****02" || !nameUnknown.KeyLookedUp ||
		nameUnknown.KeyRef != "K002" || nameUnknown.KeyBoundGroup != "" {
		t.Errorf("名字无效记录不对: %+v", nameUnknown)
	}

	keyUnknown := items[2]
	if keyUnknown.Outcome != report.FailureKeyUnknown ||
		keyUnknown.NameInput != "李娜" || keyUnknown.NameGroup != "G002 张伟 & 李娜 一家" ||
		keyUnknown.KeyMasked != "99****99" || !keyUnknown.KeyLookedUp || keyUnknown.KeyRef != "" {
		t.Errorf("密钥无效记录不对: %+v", keyUnknown)
	}

	invalid := items[3]
	if invalid.Outcome != report.FailureInvalidInput ||
		invalid.KeyMasked != "****" || invalid.KeyLookedUp {
		t.Errorf("格式非法记录不对: %+v", invalid)
	}
	if invalid.KeyMasked == "1234" {
		t.Error("掩码不应包含原始输入")
	}

	// 未注入缓冲（nil）：失败登录语义不变、不 panic。
	svc.SetFailureLog(nil)
	if _, err := svc.Login(ctx, "不存在的人", "20260002"); !errors.Is(err, auth.ErrInvalidCredentials) {
		t.Errorf("未注入缓冲时登录失败语义不变，实际 %v", err)
	}
}
