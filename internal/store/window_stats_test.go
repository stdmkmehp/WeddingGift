package store_test

import (
	"testing"
)

// TestWindowStats 覆盖滚动窗口统计的区间语义：
// [since, until) 左闭右开；bound_at 为 NULL 的未绑定密钥不计入；
// 边界恰好等于 since 计入、恰好等于 until 不计入。
func TestWindowStats(t *testing.T) {
	db, ctx := openTestDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	importFixtures(t, db, ctx)

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("准备数据失败: %v", err)
		}
	}

	// 绑定：K001 在窗口内（恰等于左边界），K002 在窗口外（恰等于右边界，不计入），
	// K003 保持未绑定（bound_at NULL，不计入）。
	exec(`UPDATE access_key SET group_id = (SELECT id FROM recipient_group WHERE external_ref = 'G001'),
		bound_at = '2026-09-16T07:17:00Z' WHERE external_ref = 'K001'`)
	exec(`UPDATE access_key SET group_id = (SELECT id FROM recipient_group WHERE external_ref = 'G002'),
		bound_at = '2026-09-17T07:17:00Z' WHERE external_ref = 'K002'`)

	// 会话：两条在窗口内，一条在窗口外（早于左边界）。
	exec(`INSERT INTO session (token_digest, group_id, access_key_id, expires_at, created_at)
		SELECT x'0102030405', group_id, id, '2099-01-01T00:00:00Z', '2026-09-16T08:00:00Z'
		FROM access_key WHERE external_ref = 'K001'`)
	exec(`INSERT INTO session (token_digest, group_id, access_key_id, expires_at, created_at)
		SELECT x'0102030406', group_id, id, '2099-01-01T00:00:00Z', '2026-09-17T07:16:59Z'
		FROM access_key WHERE external_ref = 'K001'`)
	exec(`INSERT INTO session (token_digest, group_id, access_key_id, expires_at, created_at)
		SELECT x'0102030407', group_id, id, '2099-01-01T00:00:00Z', '2026-09-15T12:00:00Z'
		FROM access_key WHERE external_ref = 'K002'`)

	w, err := db.WindowStats(ctx, "2026-09-16T07:17:00Z", "2026-09-17T07:17:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if w.Bindings != 1 {
		t.Errorf("窗口绑定数 = %d, want 1（K001；K002 恰在右边界、K003 未绑定）", w.Bindings)
	}
	if w.Logins != 2 {
		t.Errorf("窗口登录数 = %d, want 2（窗口外一条不计入）", w.Logins)
	}

	// 空窗口（无任何数据落入）应为零值而非报错。
	w, err = db.WindowStats(ctx, "2026-09-10T00:00:00Z", "2026-09-11T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if w.Bindings != 0 || w.Logins != 0 {
		t.Errorf("空窗口应全零，实际 bindings=%d logins=%d", w.Bindings, w.Logins)
	}
}
