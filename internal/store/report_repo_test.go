package store_test

import (
	"strings"
	"testing"

	"github.com/weddinggift/wedding/internal/admin"
	"github.com/weddinggift/wedding/internal/domain"
	"github.com/weddinggift/wedding/internal/store"
)

// TestLookupAccessKeyByDigest 密钥摘要归属查询：未绑定/已绑定/停用密钥均命中
// （停用不影响「这是哪张卡」的识别），未知摘要不命中。
func TestLookupAccessKeyByDigest(t *testing.T) {
	db, ctx := openTestDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	importFixtures(t, db, ctx)
	cx := testCrypto(t)

	// 未绑定密钥：ref 命中、绑定组为空。
	ref, boundRef, boundSal, ok, err := db.LookupAccessKeyByDigest(ctx, cx.AccessKeyDigest("20260001"))
	if err != nil || !ok {
		t.Fatalf("K001 应命中: ok=%v err=%v", ok, err)
	}
	if ref != "K001" || boundRef != "" || boundSal != "" {
		t.Errorf("K001 未绑定时应只有 ref，实际 ref=%q bound=(%q, %q)", ref, boundRef, boundSal)
	}

	// 绑定后：返回已绑组编号与称呼。
	mustLogin(t, db, cx, "王芳", "20260001")
	ref, boundRef, boundSal, ok, err = db.LookupAccessKeyByDigest(ctx, cx.AccessKeyDigest("20260001"))
	if err != nil || !ok {
		t.Fatalf("绑定后 K001 应命中: ok=%v err=%v", ok, err)
	}
	if ref != "K001" || boundRef != "G001" || boundSal != "王芳" {
		t.Errorf("K001 绑定信息不对: ref=%q bound=(%q, %q)", ref, boundRef, boundSal)
	}

	// 停用后仍命中（识别用）。
	if _, err := db.RevokeAccessKey(ctx, "K001"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, ok, err = db.LookupAccessKeyByDigest(ctx, cx.AccessKeyDigest("20260001")); err != nil || !ok {
		t.Errorf("停用密钥仍应可识别: ok=%v err=%v", ok, err)
	}

	// 未知摘要：不命中、不报错。
	if _, _, _, ok, err = db.LookupAccessKeyByDigest(ctx, cx.AccessKeyDigest("99999999")); err != nil || ok {
		t.Errorf("未知摘要应不命中: ok=%v err=%v", ok, err)
	}
}

// TestStatsBoundKeyRefs 组统计应给出已绑定密钥编号（字典序、", " 分隔），
// 供日报与 status 直接阅读：一个同行组可绑定多个密钥。
func TestStatsBoundKeyRefs(t *testing.T) {
	db, ctx := openTestDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	importFixtures(t, db, ctx)
	cx := testCrypto(t)

	// 绑定前：G001 无密钥、编号为空串。
	stats, err := db.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, gs := range stats.GroupStats {
		if gs.BoundKeys != 0 || gs.BoundKeyRefs != "" {
			t.Errorf("%s 绑定前应为空: keys=%d refs=%q", gs.ExternalRef, gs.BoundKeys, gs.BoundKeyRefs)
		}
	}

	// G001 绑定两个密钥（K002 先绑、K001 后绑，验证编号按字典序输出）。
	mustLogin(t, db, cx, "王芳", "20260002")
	mustLogin(t, db, cx, "王芳", "20260001")

	stats, err = db.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var g001 *store.GroupStat
	for i := range stats.GroupStats {
		if stats.GroupStats[i].ExternalRef == "G001" {
			g001 = &stats.GroupStats[i]
		}
	}
	if g001 == nil {
		t.Fatal("组统计缺少 G001")
	}
	if g001.BoundKeys != 2 || g001.BoundKeyRefs != "K001, K002" {
		t.Errorf("G001 已绑定密钥 = %d/%q, want 2/\"K001, K002\"", g001.BoundKeys, g001.BoundKeyRefs)
	}
	if text := admin.StatusText(stats); !strings.Contains(text, "K001, K002") {
		t.Errorf("status 文本应含已绑定密钥编号:\n%s", text)
	}
}

// TestGroupSalutationByLoginName 登录名称归属查询：本名/昵称命中、规范化命中、
// 不可登录成员与未知名称不命中。
func TestGroupSalutationByLoginName(t *testing.T) {
	db, ctx := openTestDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	importFixtures(t, db, ctx)

	// 本名与昵称（含首尾空白，入参契约要求先规范化）都解析到 G001。
	for _, name := range []string{"王芳", "芳芳", "  小芳  "} {
		norm := domain.NormalizeLoginName(name)
		ref, sal, ok, err := db.GroupSalutationByLoginName(ctx, norm)
		if err != nil || !ok {
			t.Fatalf("%q 应命中: ok=%v err=%v", name, ok, err)
		}
		if ref != "G001" || sal != "王芳" {
			t.Errorf("%q 归属不对: (%q, %q)", name, ref, sal)
		}
	}

	// 未知名称与不可登录成员（陈小满无登录名称）不命中。
	for _, name := range []string{"不存在的人", "陈小满"} {
		if _, _, ok, err := db.GroupSalutationByLoginName(ctx, name); err != nil || ok {
			t.Errorf("%q 应不命中: ok=%v err=%v", name, ok, err)
		}
	}
}
