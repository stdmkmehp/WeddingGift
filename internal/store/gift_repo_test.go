package store_test

import (
	"testing"

	"github.com/weddinggift/wedding/internal/store"
)

func TestFixGiftCode(t *testing.T) {
	db, ctx := openTestDB(t)
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	importFixtures(t, db, ctx)

	// code 模式（GI001 原值 FAKECOFFEE001）修正成功，新值可解密回读、旧值消失。
	res, err := db.FixGiftCode(ctx, testCrypto(t), "GI001", "NEWCOFFEE9X")
	if err != nil {
		t.Fatal(err)
	}
	if res != store.FixGiftCodeDone {
		t.Fatalf("修正 code 模式礼品应返回 FixGiftCodeDone，实际 %s", res)
	}
	state, err := db.LoadExistingState(ctx, testCrypto(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := state.GiftCodePlaintexts["NEWCOFFEE9X"]; got != "GI001" {
		t.Errorf("新激活码 -> %q, want GI001", got)
	}
	if _, ok := state.GiftCodePlaintexts["FAKECOFFEE001"]; ok {
		t.Error("旧激活码 FAKECOFFEE001 应已消失")
	}

	// 幂等：同一值再次修正仍成功。
	res, err = db.FixGiftCode(ctx, testCrypto(t), "GI001", "NEWCOFFEE9X")
	if err != nil || res != store.FixGiftCodeDone {
		t.Errorf("重复修正应幂等成功: res=%s err=%v", res, err)
	}

	// 不存在的礼品。
	res, err = db.FixGiftCode(ctx, testCrypto(t), "GI999", "WHATEVER001")
	if err != nil {
		t.Fatal(err)
	}
	if res != store.FixGiftCodeNotFound {
		t.Errorf("不存在的礼品应返回 FixGiftCodeNotFound，实际 %s", res)
	}

	// card_secret 模式（GI003）明确拒绝。
	res, err = db.FixGiftCode(ctx, testCrypto(t), "GI003", "WHATEVER001")
	if err != nil {
		t.Fatal(err)
	}
	if res != store.FixGiftCodeNotCodeMode {
		t.Errorf("card_secret 礼品应返回 FixGiftCodeNotCodeMode，实际 %s", res)
	}

	// 空值与含换行的值在写库前拒绝。
	if _, err = db.FixGiftCode(ctx, testCrypto(t), "GI001", ""); err == nil {
		t.Error("空激活码应被拒绝")
	}
	if _, err = db.FixGiftCode(ctx, testCrypto(t), "GI001", "ABC\nDEF"); err == nil {
		t.Error("含换行的激活码应被拒绝")
	}

	// 全程零增删：计数与首导后完全一致。
	stats, err := db.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.GiftItems != 6 || stats.GiftItemsActive != 6 {
		t.Errorf("修正不应改变礼品计数: %+v", stats)
	}
}
