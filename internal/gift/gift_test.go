package gift_test

import (
	"context"
	"encoding/hex"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/weddinggift/wedding/internal/admin"
	"github.com/weddinggift/wedding/internal/cryptox"
	"github.com/weddinggift/wedding/internal/domain"
	"github.com/weddinggift/wedding/internal/gift"
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

// newRevealEnv 导入 fixtures 并用 name+keyCode 登录，返回该组的 reveal 环境与礼品摘要。
func newRevealEnv(t *testing.T, name, keyCode string) (*gift.Service, string, []store.GiftSummary) {
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

	token, err := cryptox.NewSessionToken()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	outcome, _, err := db.LoginWithCredentials(ctx, domain.NormalizeLoginName(name),
		cx.AccessKeyDigest(keyCode), cryptox.SessionTokenDigest(token),
		now.Add(30*24*time.Hour).Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil || outcome == nil {
		t.Fatalf("登录失败: outcome=%v err=%v", outcome, err)
	}
	summaries, err := db.GiftSummariesForGroup(ctx, outcome.GroupID)
	if err != nil {
		t.Fatal(err)
	}
	return gift.New(db, cx), outcome.GroupID, summaries
}

// TestRevealCode 主路径：本组每份礼品均可解密出导入时的明文（G002 三份含实例覆盖）。
func TestRevealCode(t *testing.T) {
	svc, groupID, summaries := newRevealEnv(t, "阿伟", "20260002")
	if len(summaries) != 3 {
		t.Fatalf("G002 应有 3 份礼品，实际 %d", len(summaries))
	}
	want := []string{"FAKETRAVEL002", "FAKEMOVIE003", "FAKECOFFEE004"}
	for i, g := range summaries {
		code, err := svc.RevealCode(context.Background(), groupID, g.ID)
		if err != nil {
			t.Fatalf("reveal %s 失败: %v", g.Title, err)
		}
		if code != want[i] {
			t.Errorf("第 %d 份礼品码 = %q，期望 %q", i+1, code, want[i])
		}
	}
}

// TestRevealCodeForeignGroup 跨组隔离：持有有效会话也不能读取其他组的礼品。
func TestRevealCodeForeignGroup(t *testing.T) {
	// G002 的礼品 ID。
	_, _, foreign := newRevealEnv(t, "阿伟", "20260002")
	if len(foreign) == 0 {
		t.Fatal("G002 应有礼品")
	}
	// 王芳登录 G001，尝试 reveal G002 的礼品。
	svc, groupID, _ := newRevealEnv(t, "王芳", "20260001")
	_, err := svc.RevealCode(context.Background(), groupID, foreign[0].ID)
	if !errors.Is(err, gift.ErrUnavailable) {
		t.Errorf("跨组 reveal 应返回 ErrUnavailable，实际 %v", err)
	}
}

// TestRevealCodeUnknownID 不存在的 giftId 与跨组返回同一错误，不区分原因。
func TestRevealCodeUnknownID(t *testing.T) {
	svc, groupID, _ := newRevealEnv(t, "王芳", "20260001")
	_, err := svc.RevealCode(context.Background(), groupID, "00000000000000000000000000000000")
	if !errors.Is(err, gift.ErrUnavailable) {
		t.Errorf("未知 giftId 应返回 ErrUnavailable，实际 %v", err)
	}
}
