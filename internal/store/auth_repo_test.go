package store_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/weddinggift/wedding/internal/cryptox"
	"github.com/weddinggift/wedding/internal/domain"
	"github.com/weddinggift/wedding/internal/store"
)

// loginAttempt 是测试侧发起一次登录（绑定事务）的便捷封装。
// 返回 (outcome, reason, error)；会话摘要为每次随机生成。
func loginAttempt(t *testing.T, db *store.DB, cx *cryptox.Service, name, keyCode string) (*store.LoginOutcome, store.LoginDenyReason, error) {
	t.Helper()
	token, err := cryptox.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	return db.LoginWithCredentials(t.Context(), domain.NormalizeLoginName(name),
		cx.AccessKeyDigest(keyCode), cryptox.SessionTokenDigest(token),
		now.Add(30*24*time.Hour).Format(time.RFC3339), now.Format(time.RFC3339))
}

func mustLogin(t *testing.T, db *store.DB, cx *cryptox.Service, name, keyCode string) *store.LoginOutcome {
	t.Helper()
	out, _, err := loginAttempt(t, db, cx, name, keyCode)
	if err != nil {
		t.Fatalf("登录事务错误: %v", err)
	}
	if out == nil {
		t.Fatalf("%s + %s 应登录成功", name, keyCode)
	}
	return out
}

func mustDenyLogin(t *testing.T, db *store.DB, cx *cryptox.Service, name, keyCode string, want store.LoginDenyReason) {
	t.Helper()
	out, reason, err := loginAttempt(t, db, cx, name, keyCode)
	if err != nil {
		t.Fatalf("登录事务错误: %v", err)
	}
	if out != nil {
		t.Fatalf("%s + %s 应登录失败", name, keyCode)
	}
	if reason != want {
		t.Fatalf("%s + %s 拒绝原因 = %v, want %v", name, keyCode, reason, want)
	}
}

// TestBindingDecisionMatrix 覆盖产品架构 §7.2 决策表全部行（G4 门禁核心）。
func TestBindingDecisionMatrix(t *testing.T) {
	db, ctx := openTestDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	importFixtures(t, db, ctx)
	cx := testCrypto(t)

	// 无效名称 + 任意密钥：统一模糊失败。
	mustDenyLogin(t, db, cx, "不存在的人", "20260001", store.LoginDenyGeneric)
	// 有效名称 + 不存在的密钥摘要：统一模糊失败。
	mustDenyLogin(t, db, cx, "王芳", "99999999", store.LoginDenyGeneric)

	// 首次绑定：本名成功。
	out := mustLogin(t, db, cx, "王芳", "20260001")
	if !out.FirstBind {
		t.Error("首次登录应标记 FirstBind")
	}
	if out.GroupSalutation != "王芳" || out.AccessKeyRef != "K001" {
		t.Errorf("绑定结果不正确: %+v", out)
	}

	// 同组再登录：同一成员的昵称、同密钥 → 成功且非首绑。
	out = mustLogin(t, db, cx, "芳芳", "20260001")
	if out.FirstBind {
		t.Error("同组再登录不应标记 FirstBind")
	}
	// 规范化幂等：首尾空白不影响匹配。
	mustLogin(t, db, cx, "  小芳  ", "20260001")

	// 跨组：K001 已绑 G001，搭配 G002 的登录名称 → 明确「密钥已绑定其他同行组」。
	mustDenyLogin(t, db, cx, "张伟", "20260001", store.LoginDenyKeyBoundElsewhere)

	// 一个同行组可绑定多个密钥（2026-09-13 决策调整）：未绑定的 K002 搭配
	// G001 名称 → 绑定成功，与 K001 归属同一同行组、看到同一份回礼。
	out2 := mustLogin(t, db, cx, "王芳", "20260002")
	if !out2.FirstBind {
		t.Error("第二个密钥首次绑定应标记 FirstBind")
	}
	if out2.GroupID != out.GroupID {
		t.Errorf("同组两个密钥应解析到同一同行组: %q vs %q", out2.GroupID, out.GroupID)
	}
	stats, err := db.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.KeysBound != 2 {
		t.Errorf("G001 应有 2 个已绑定密钥，实际 %d", stats.KeysBound)
	}

	// 断网重试语义：同组信息重试同一请求仍然成功（幂等）。
	mustLogin(t, db, cx, "王芳", "20260001")

	// 带娃组：可登录成员登录，can_login=false 的儿童没有登录名称。
	mustLogin(t, db, cx, "陈强", "20260003")
	mustDenyLogin(t, db, cx, "陈小满", "20260003", store.LoginDenyGeneric)
}

// TestConcurrentBindingContention 验证 §8.2 不变量：两个请求争抢同一密钥时
// 最多一个成功（一个密钥最多绑定一个同行组）；不同密钥绑定同一同行组互不
// 阻塞（一个同行组允许绑定多个密钥）。BEGIN IMMEDIATE + 条件更新共同保证。
func TestConcurrentBindingContention(t *testing.T) {
	cx := testCrypto(t)

	run := func(t *testing.T, attempts [][2]string) int {
		db, ctx := openTestDB(t)
		if _, err := db.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		importFixtures(t, db, ctx)

		var wg sync.WaitGroup
		successes := make(chan bool, len(attempts))
		for _, a := range attempts {
			wg.Add(1)
			go func(name, key string) {
				defer wg.Done()
				out, _, err := loginAttempt(t, db, cx, name, key)
				if err != nil {
					t.Errorf("并发登录事务错误: %v", err)
					successes <- false
					return
				}
				successes <- out != nil
			}(a[0], a[1])
		}
		wg.Wait()
		close(successes)
		n := 0
		for ok := range successes {
			if ok {
				n++
			}
		}
		return n
	}

	// 两个组争抢同一密钥 K001：一个密钥最多绑定一个同行组。
	if n := run(t, [][2]string{{"王芳", "20260001"}, {"张伟", "20260001"}}); n != 1 {
		t.Errorf("同一密钥两个组争抢应恰好一个成功，实际 %d", n)
	}
	// 两个不同密钥绑定同一组 G002（两名成员同时登录）：都应成功。
	if n := run(t, [][2]string{{"张伟", "20260002"}, {"李娜", "20260003"}}); n != 2 {
		t.Errorf("同一组绑定两个不同密钥应两个都成功，实际 %d", n)
	}
}

// TestLoginRejectsDisabledStates 停用密钥与停用组都不能登录。
func TestLoginRejectsDisabledStates(t *testing.T) {
	db, ctx := openTestDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	importFixtures(t, db, ctx)
	cx := testCrypto(t)

	// 停用密钥 K001：登录失败。
	if _, err := db.RevokeAccessKey(ctx, "K001"); err != nil {
		t.Fatal(err)
	}
	mustDenyLogin(t, db, cx, "王芳", "20260001", store.LoginDenyGeneric)

	// 停用组 G003：成员登录失败。
	if _, err := db.ExecContext(ctx,
		"UPDATE recipient_group SET status = 'disabled' WHERE external_ref = 'G003'"); err != nil {
		t.Fatal(err)
	}
	mustDenyLogin(t, db, cx, "陈强", "20260003", store.LoginDenyGeneric)
}

// TestSessionLifecycle 会话的创建、解析、过期、删除与批量清理。
func TestSessionLifecycle(t *testing.T) {
	db, ctx := openTestDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	importFixtures(t, db, ctx)
	cx := testCrypto(t)

	token, err := cryptox.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	digest := cryptox.SessionTokenDigest(token)
	now := time.Now().UTC().Truncate(time.Second)
	expires := now.Add(time.Hour)

	out, _, err := db.LoginWithCredentials(ctx, "王芳", cx.AccessKeyDigest("20260001"),
		digest, expires.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil || out == nil {
		t.Fatalf("登录失败: out=%v err=%v", out, err)
	}

	// 有效期内可解析。
	info, err := db.FindSessionByTokenDigest(ctx, digest, now.Format(time.RFC3339))
	if err != nil || info == nil {
		t.Fatalf("会话应可解析: info=%v err=%v", info, err)
	}
	if info.GroupSalutation != "王芳" {
		t.Errorf("会话组称呼错误: %q", info.GroupSalutation)
	}

	// 过期后不可解析，且被顺手删除。
	info, err = db.FindSessionByTokenDigest(ctx, digest, expires.Add(time.Second).Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Error("过期会话应返回 nil")
	}
	stats, err := db.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Sessions != 0 {
		t.Errorf("过期会话应被删除，剩余 %d", stats.Sessions)
	}

	// 新建两条会话：DeleteSession 精确删除其一；DeleteExpiredSessions 清理过期。
	t1, _ := cryptox.NewSessionToken()
	t2, _ := cryptox.NewSessionToken()
	for i, tk := range [][]byte{t1, t2} {
		exp := now.Add(time.Hour)
		if i == 1 {
			exp = now.Add(-time.Hour) // 已过期
		}
		if out, _, err := db.LoginWithCredentials(ctx, "王芳", cx.AccessKeyDigest("20260001"),
			cryptox.SessionTokenDigest(tk), exp.Format(time.RFC3339), now.Format(time.RFC3339)); err != nil || out == nil {
			t.Fatalf("建会话失败: out=%v err=%v", out, err)
		}
	}
	if err := db.DeleteSession(ctx, cryptox.SessionTokenDigest(t1)); err != nil {
		t.Fatal(err)
	}
	if info, _ := db.FindSessionByTokenDigest(ctx, cryptox.SessionTokenDigest(t1), now.Format(time.RFC3339)); info != nil {
		t.Error("DeleteSession 后会话不应存在")
	}
	n, err := db.DeleteExpiredSessions(ctx, now.Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("应清理 1 条过期会话，实际 %d", n)
	}
}

// TestSessionInvalidatedByRevoke 密钥停用后既有会话立即失效（PA-12）。
func TestSessionInvalidatedByRevoke(t *testing.T) {
	db, ctx := openTestDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	importFixtures(t, db, ctx)
	cx := testCrypto(t)

	token, _ := cryptox.NewSessionToken()
	now := time.Now().UTC()
	if out, _, err := db.LoginWithCredentials(ctx, "王芳", cx.AccessKeyDigest("20260001"),
		cryptox.SessionTokenDigest(token), now.Add(time.Hour).Format(time.RFC3339), now.Format(time.RFC3339)); err != nil || out == nil {
		t.Fatalf("登录失败: out=%v err=%v", out, err)
	}

	if _, err := db.RevokeAccessKey(ctx, "K001"); err != nil {
		t.Fatal(err)
	}
	info, err := db.FindSessionByTokenDigest(ctx, cryptox.SessionTokenDigest(token), now.Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	if info != nil {
		t.Error("密钥停用后会话必须失效")
	}
}

// TestGiftSummariesForGroup 礼品摘要：按 ordinal 排序、实例覆盖生效、不含密文。
func TestGiftSummariesForGroup(t *testing.T) {
	db, ctx := openTestDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	importFixtures(t, db, ctx)

	// 通过登录事务拿到 G002 的内部 ID（无公开 ID 查询接口，复用业务路径）。
	cx := testCrypto(t)
	out := mustLogin(t, db, cx, "李娜", "20260002")
	groupID := out.GroupID

	gifts, err := db.GiftSummariesForGroup(ctx, groupID)
	if err != nil {
		t.Fatal(err)
	}
	if len(gifts) != 3 {
		t.Fatalf("G002 应有 3 份礼品，实际 %d", len(gifts))
	}
	wantTitles := []string{"长三角旅游年卡", "电影券", "咖啡券"}
	for i, g := range gifts {
		if g.Title != wantTitles[i] {
			t.Errorf("第 %d 份礼品标题 = %q, want %q", i, g.Title, wantTitles[i])
		}
		if g.Ordinal != i+1 {
			t.Errorf("第 %d 份礼品 ordinal = %d", i, g.Ordinal)
		}
	}
	// 实例覆盖（GI004）：说明、有效期、链接均来自实例。
	g3 := gifts[2]
	if !strings.Contains(g3.Instructions, "限定联名") ||
		!strings.Contains(g3.ValidityText, "45 天") ||
		!strings.Contains(g3.RedemptionURL, "coffee-special") {
		t.Errorf("实例覆盖未生效: %+v", g3)
	}
	// 未覆盖的字段取类型默认（旅游年卡默认为小程序口令）。
	if !strings.Contains(gifts[0].RedemptionURL, "#小程序://示例旅游年卡") {
		t.Errorf("默认兑换入口未生效: %+v", gifts[0])
	}
}
