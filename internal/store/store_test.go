package store_test

import (
	"context"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/weddinggift/wedding/internal/admin"
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

func openTestDB(t *testing.T) (*store.DB, context.Context) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "wedding.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, ctx
}

func loadFixtures(t *testing.T) *domain.Dataset {
	t.Helper()
	ds, errs := admin.LoadDataset(fixturesDir)
	if len(errs) > 0 {
		t.Fatalf("fixtures 解析失败: %v", errs)
	}
	if valErrs := domain.Validate(ds); len(valErrs) > 0 {
		t.Fatalf("fixtures 校验失败: %v", valErrs)
	}
	return ds
}

func importFixtures(t *testing.T, db *store.DB, ctx context.Context) {
	t.Helper()
	if err := db.ImportDataset(ctx, loadFixtures(t), testCrypto(t)); err != nil {
		t.Fatalf("导入 fixtures 失败: %v", err)
	}
}

func TestMigrateIdempotent(t *testing.T) {
	db, ctx := openTestDB(t)

	applied, err := db.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 1 {
		t.Fatalf("首次 migrate 应应用 1 个 migration，实际 %d", applied)
	}
	v, err := db.SchemaVersion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v != 1 {
		t.Fatalf("schema 版本应为 1，实际 %d", v)
	}

	// 重复执行不再应用任何 migration。
	applied, err = db.Migrate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if applied != 0 {
		t.Fatalf("重复 migrate 应应用 0 个，实际 %d", applied)
	}
	if err := db.RequireSchema(ctx); err != nil {
		t.Fatalf("RequireSchema 应通过: %v", err)
	}
}

func TestRequireSchemaBeforeMigrate(t *testing.T) {
	db, ctx := openTestDB(t)
	if err := db.RequireSchema(ctx); err == nil {
		t.Fatal("未 migrate 的数据库 RequireSchema 应报错")
	}
}

func TestImportStatsAndReload(t *testing.T) {
	db, ctx := openTestDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	importFixtures(t, db, ctx)

	stats, err := db.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assert := func(name string, got, want int) {
		t.Helper()
		if got != want {
			t.Errorf("%s = %d, want %d", name, got, want)
		}
	}
	assert("同行组", stats.Groups, 3)
	assert("active 同行组", stats.GroupsActive, 3)
	assert("成员", stats.Members, 6)
	assert("可登录成员", stats.MembersCanLogin, 5)
	assert("登录名称", stats.LoginNames, 9)
	assert("礼品类型", stats.GiftTypes, 3)
	assert("礼品", stats.GiftItems, 6)
	assert("密钥", stats.AccessKeys, 3)
	assert("active 密钥", stats.KeysActive, 3)
	assert("已绑定密钥", stats.KeysBound, 0)
	assert("会话", stats.Sessions, 0)
	assert("组明细条数", len(stats.GroupStats), 3)

	// 组明细须带组称呼（status 直观识别是哪个同行组），与 fixtures 逐条对照。
	ds := loadFixtures(t)
	wantSalutation := make(map[string]string, len(ds.Groups))
	for _, g := range ds.Groups {
		wantSalutation[g.ExternalRef] = g.Salutation
	}
	// 组明细须列出该组 active 礼品覆盖的类型（去重、字典序、", " 分隔）。
	typesByGroup := make(map[string]map[string]bool)
	for _, gi := range ds.GiftItems {
		if gi.Status != domain.StatusActive {
			continue
		}
		if typesByGroup[gi.GroupExternalRef] == nil {
			typesByGroup[gi.GroupExternalRef] = make(map[string]bool)
		}
		typesByGroup[gi.GroupExternalRef][gi.TypeKey] = true
	}
	for _, gs := range stats.GroupStats {
		want, ok := wantSalutation[gs.ExternalRef]
		if !ok {
			t.Errorf("组明细出现 fixtures 之外的组: %s", gs.ExternalRef)
			continue
		}
		if gs.Salutation != want {
			t.Errorf("组 %s 称呼 = %q, want %q", gs.ExternalRef, gs.Salutation, want)
		}
		keys := make([]string, 0, len(typesByGroup[gs.ExternalRef]))
		for k := range typesByGroup[gs.ExternalRef] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if got, want := gs.GiftTypes, strings.Join(keys, ", "); got != want {
			t.Errorf("组 %s 礼品类型 = %q, want %q", gs.ExternalRef, got, want)
		}
	}
	if text := admin.StatusText(stats); !strings.Contains(text, ds.Groups[0].Salutation) {
		t.Errorf("status 文本缺少组称呼 %q:\n%s", ds.Groups[0].Salutation, text)
	}
}

func TestImportTwiceRefused(t *testing.T) {
	db, ctx := openTestDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	importFixtures(t, db, ctx)

	err := db.ImportDataset(ctx, loadFixtures(t), testCrypto(t))
	if err == nil || !strings.Contains(err.Error(), "非空") {
		t.Fatalf("非空库重复导入应被拒绝，实际: %v", err)
	}
}

func TestDatabaseConstraints(t *testing.T) {
	db, ctx := openTestDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	importFixtures(t, db, ctx)

	exec := func(query string, args ...any) error {
		_, err := db.ExecContext(ctx, query, args...)
		return err
	}

	// 登录名称规范化值全局唯一。
	if err := exec(`INSERT INTO member_login_name (id, member_id, normalized_value, kind)
		SELECT 'dup1', member_id, normalized_value, kind FROM member_login_name LIMIT 1`); err == nil {
		t.Error("重复 normalized_value 应被唯一约束拒绝")
	}

	// 一个同行组可绑定多个密钥（2026-09-13 起）：两个密钥绑到同一组都应成功。
	for _, ref := range []string{"K001", "K002"} {
		if err := exec(`UPDATE access_key SET group_id = (SELECT id FROM recipient_group WHERE external_ref = 'G001')
			WHERE external_ref = ?`, ref); err != nil {
			t.Errorf("密钥 %s 绑定 G001 应成功: %v", ref, err)
		}
	}

	// 外键：成员必须属于存在的组。
	if err := exec(`INSERT INTO member (id, group_id, external_ref, display_name, display_name_norm, can_login, sort_order)
		VALUES ('x1', 'no-such-group', 'MX', '某人', '某人', 1, 1)`); err == nil {
		t.Error("外键约束应拒绝不存在的 group_id")
	}

	// 组内 ordinal 唯一。
	if err := exec(`INSERT INTO gift_item (id, external_ref, group_id, type_key, code_ciphertext, ordinal, status)
		SELECT 'x2', 'GIX', group_id, type_key, code_ciphertext, ordinal, status FROM gift_item LIMIT 1`); err == nil {
		t.Error("同组重复 ordinal 应被 UNIQUE(group_id, ordinal) 拒绝")
	}
}

func TestRevokeAccessKey(t *testing.T) {
	db, ctx := openTestDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	importFixtures(t, db, ctx)

	// 造一条 K001 的会话，验证停用时被删除。
	if _, err := db.ExecContext(ctx, `
		INSERT INTO session (token_digest, group_id, access_key_id, expires_at, created_at)
		SELECT x'0102030405', (SELECT id FROM recipient_group LIMIT 1), id, '2099-01-01T00:00:00Z', '2026-09-12T00:00:00Z'
		FROM access_key WHERE external_ref = 'K001'`); err != nil {
		t.Fatal(err)
	}

	res, err := db.RevokeAccessKey(ctx, "K001")
	if err != nil {
		t.Fatal(err)
	}
	if res != store.RevokeDone {
		t.Fatalf("首次停用应返回 RevokeDone，实际 %s", res)
	}

	stats, err := db.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.KeysDisabled != 1 || stats.KeysActive != 2 {
		t.Errorf("停用后密钥计数不对: disabled=%d active=%d", stats.KeysDisabled, stats.KeysActive)
	}
	if stats.Sessions != 0 {
		t.Errorf("停用密钥的会话应被删除，剩余 %d", stats.Sessions)
	}

	res, err = db.RevokeAccessKey(ctx, "K001")
	if err != nil {
		t.Fatal(err)
	}
	if res != store.RevokeAlreadyDisabled {
		t.Errorf("重复停用应返回 RevokeAlreadyDisabled，实际 %s", res)
	}

	res, err = db.RevokeAccessKey(ctx, "K999")
	if err != nil {
		t.Fatal(err)
	}
	if res != store.RevokeNotFound {
		t.Errorf("不存在的密钥应返回 RevokeNotFound，实际 %s", res)
	}
}

func TestBackupTo(t *testing.T) {
	db, ctx := openTestDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	importFixtures(t, db, ctx)

	dest := filepath.Join(t.TempDir(), "wedding-backup.db")
	if err := db.BackupTo(ctx, dest); err != nil {
		t.Fatalf("备份失败: %v", err)
	}

	// 目标已存在时拒绝覆盖。
	if err := db.BackupTo(ctx, dest); err == nil {
		t.Fatal("目标已存在的备份应被拒绝")
	}

	// 打开备份文件，确认数据完整一致。
	bak, err := store.Open(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := bak.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Groups != 3 || stats.Members != 6 || stats.GiftItems != 6 || stats.AccessKeys != 3 {
		t.Errorf("备份内容与源库不一致: %+v", stats)
	}
	// 显式关闭（WAL checkpoint 会移除 sidecar），再检查产物为单一文件。
	if err := bak.Close(); err != nil {
		t.Fatal(err)
	}
	matches, _ := filepath.Glob(dest + "*")
	for _, m := range matches {
		if m != dest {
			t.Errorf("备份产物不应有附加文件: %s", m)
		}
	}
}

// TestNoPlaintextLeak 是 G3 门禁的关键测试：导入后数据库文件与备份文件中
// 扫描不到任何明文卡片密钥或礼品激活码。
func TestNoPlaintextLeak(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	dbPath := filepath.Join(dir, "wedding.db")
	db, err := store.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	ds := loadFixtures(t)
	if err := db.ImportDataset(ctx, ds, testCrypto(t)); err != nil {
		t.Fatal(err)
	}

	backupPath := filepath.Join(dir, "backup.db")
	if err := db.BackupTo(ctx, backupPath); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	// 收集全部敏感明文：密钥与礼品码。
	var secrets []string
	for _, k := range ds.AccessKeys {
		secrets = append(secrets, k.CodePlaintext)
	}
	for _, gi := range ds.GiftItems {
		secrets = append(secrets, gi.CodePlaintext)
	}

	// 扫描数据库主文件、可能的 WAL/SHM 文件与备份文件。
	targets := []string{dbPath, dbPath + "-wal", dbPath + "-shm", backupPath}
	for _, path := range targets {
		data, err := os.ReadFile(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, s := range secrets {
			if strings.Contains(string(data), s) {
				t.Errorf("文件 %s 中发现明文敏感值（密钥或礼品码）", filepath.Base(path))
			}
		}
	}
}
