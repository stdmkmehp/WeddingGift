package report

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/weddinggift/wedding/internal/store"
)

func testSummary() *Summary {
	return &Summary{
		GeneratedAt: time.Date(2026, 9, 17, 23, 17, 0, 0, time.UTC), // 北京 2026-09-18 07:17
		Window:      24 * time.Hour,
		Bindings:    3,
		Logins:      5,
		Totals: &store.Stats{
			Groups: 2, GroupsActive: 2,
			Members: 4, MembersCanLogin: 4,
			LoginNames: 5,
			GiftTypes:  3,
			GiftItems:  6, GiftItemsActive: 6,
			AccessKeys: 4, KeysActive: 3, KeysDisabled: 1,
			KeysBound: 1, Sessions: 2,
			GroupStats: []store.GroupStat{
				{ExternalRef: "G001", Salutation: "张伟 & 李娜 一家", Status: "active", MemberCount: 2, GiftCount: 3, GiftTypes: "coffee, movie", BoundKeys: 1},
				{ExternalRef: "G002", Salutation: "王芳", Status: "active", MemberCount: 2, GiftCount: 3, GiftTypes: "travel", BoundKeys: 0},
			},
		},
	}
}

func TestRenderDaily(t *testing.T) {
	failures := []LoginFailure{
		{
			At:      time.Date(2026, 9, 17, 13, 3, 0, 0, time.UTC), // 北京 21:03，窗口内
			Outcome: FailureKeyBoundElsewhere, NameInput: "王芳", NameGroup: "G001 王芳",
			KeyMasked: "12****78", KeyLookedUp: true, KeyRef: "K002", KeyBoundGroup: "G003 陈强 & 刘洋 一家",
		},
		{
			At:      time.Date(2026, 9, 16, 14, 5, 0, 0, time.UTC), // 北京 22:05，窗口外
			Outcome: FailureNameUnknown, NameInput: "王彷",
			KeyMasked: "12****78", KeyLookedUp: true, KeyRef: "K002",
		},
		{
			At:      time.Date(2026, 9, 17, 15, 10, 0, 0, time.UTC), // 北京 23:10，窗口内
			Outcome: FailureInvalidInput, NameInput: "", KeyMasked: "****",
		},
	}
	subject, body := RenderDaily(testSummary(), failures)

	if subject != "WeddingGift 日报 2026-09-18" {
		t.Errorf("主题应按北京时间取日期，实际 %q", subject)
	}
	for _, want := range []string{
		"统计窗口：2026-09-17 07:17 ～ 2026-09-18 07:17（北京时间，滚动 24 小时）",
		"新绑定密钥：3 个",
		"成功登录：5 人次",
		"卡片密钥：4 张（active 3 / disabled 1）",
		"已绑定 1 / 未绑定 3（绑定率 25.0%）",
		"尚未绑定密钥的同行组（1 个）",
		"- G002 王芳：成员 2 人，active 礼品 3 份（travel）",
		"登录失败明细（内存记录 3 条，其中窗口内 2 条；进程重启即清空）",
		"09-17 21:03｜名字 王芳（属 G001 王芳）｜密钥 12****78（有效 K002，已绑定 G003 陈强 & 刘洋 一家）｜密钥已绑定其他同行组",
		"09-16 22:05｜名字 王彷（无匹配）｜密钥 12****78（有效 K002，未绑定）｜名字无效（无匹配）",
		"09-17 23:10｜名字 （空）（无匹配）｜密钥 ****｜输入格式不正确",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("正文缺少 %q:\n%s", want, body)
		}
	}
	// 已绑定组不应出现在未绑定明细中。
	if strings.Contains(body, "- G001") {
		t.Errorf("G001 已有绑定密钥，不应列入未绑定明细:\n%s", body)
	}
}

func TestRenderDailyNoFailures(t *testing.T) {
	_, body := RenderDaily(testSummary(), nil)
	if !strings.Contains(body, "登录失败明细（内存记录 0 条，其中窗口内 0 条；进程重启即清空）") ||
		!strings.Contains(body, "当前无登录失败记录。") {
		t.Errorf("无失败记录时应明确展示:\n%s", body)
	}
}

func TestRenderDailyStatsOnly(t *testing.T) {
	_, body := RenderDailyStatsOnly(testSummary())
	if !strings.Contains(body, "仅存在于 serve 进程内存") {
		t.Errorf("纯统计版应注明失败明细仅 serve 内存可见:\n%s", body)
	}
	if strings.Contains(body, "内存记录") {
		t.Errorf("纯统计版不应包含缓冲计数:\n%s", body)
	}
}

func TestRenderOverflowAlert(t *testing.T) {
	failures := []LoginFailure{{
		At:      time.Date(2026, 9, 17, 13, 3, 0, 0, time.UTC),
		Outcome: FailureKeyUnknown, NameInput: "李四", NameGroup: "G002 李四",
		KeyMasked: "99****99", KeyLookedUp: true,
	}}
	subject, body := RenderOverflowAlert(failures, 100, 50)
	for _, want := range []string{
		"WeddingGift 登录失败告警",
		"已达上限 100 条",
		"当前全部 1 条记录",
		"清理最早 50 条",
		"09-17 21:03｜名字 李四（属 G002 李四）｜密钥 99****99（无匹配）｜密钥无效或已停用",
	} {
		if !strings.Contains(subject+body, want) {
			t.Errorf("告警邮件缺少 %q:\n%s\n%s", want, subject, body)
		}
	}
}

func TestRenderDailyAllBound(t *testing.T) {
	s := testSummary()
	s.Totals.GroupStats[1].BoundKeys = 2
	s.Totals.KeysBound = 3

	_, body := RenderDaily(s, nil)
	if !strings.Contains(body, "尚未绑定密钥的同行组（0 个）") ||
		!strings.Contains(body, "全部 active 同行组均已有密钥绑定。") {
		t.Errorf("全部绑定时应显示零待办口径:\n%s", body)
	}
	if !strings.Contains(body, "绑定率 75.0%") {
		t.Errorf("绑定率应按 3/4 渲染:\n%s", body)
	}
}

// TestCollect 用真实 SQLite 验证 Collect 的窗口换算与累积复用：
// 库内造一条窗口内绑定与会话，窗口外一条会话。
func TestCollect(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "wedding.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("准备数据失败: %v", err)
		}
	}
	exec(`INSERT INTO recipient_group (id, external_ref, salutation, custom_message, status, created_at)
		VALUES ('g1', 'G001', '测试组', '', 'active', '2026-09-01T00:00:00Z')`)
	exec(`INSERT INTO access_key (id, external_ref, key_digest, group_id, status, bound_at, created_at)
		VALUES ('k1', 'K001', x'0102', 'g1', 'active', '2026-09-16T12:00:00Z', '2026-09-01T00:00:00Z')`)
	exec(`INSERT INTO session (token_digest, group_id, access_key_id, expires_at, created_at)
		VALUES (x'0102030405', 'g1', 'k1', '2099-01-01T00:00:00Z', '2026-09-16T13:00:00Z')`)
	exec(`INSERT INTO session (token_digest, group_id, access_key_id, expires_at, created_at)
		VALUES (x'0102030406', 'g1', 'k1', '2099-01-01T00:00:00Z', '2026-09-10T13:00:00Z')`)

	now := time.Date(2026, 9, 16, 23, 17, 0, 0, time.UTC) // 北京 2026-09-17 07:17
	s, err := Collect(ctx, db, now, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if s.Bindings != 1 || s.Logins != 1 {
		t.Errorf("窗口统计不对: bindings=%d logins=%d, want 1/1", s.Bindings, s.Logins)
	}
	if s.Totals.Groups != 1 || s.Totals.AccessKeys != 1 || s.Totals.KeysBound != 1 || s.Totals.Sessions != 2 {
		t.Errorf("累积统计不对: %+v", s.Totals)
	}
}

func TestBuildMessage(t *testing.T) {
	cfg := SMTPConfig{
		Host: "smtp.qq.com", Port: 465,
		From: "1628284520@qq.com", To: []string{"1628284520@qq.com"},
	}
	msg := buildMessage(cfg, "WeddingGift 日报 2026-09-17", "第一行\n第二行\n")

	for _, want := range []string{
		"From: =?UTF-8?B?V2VkZGluZ0dpZnQg5pel5oql?= <1628284520@qq.com>\r\n",
		"To: 1628284520@qq.com\r\n",
		"Subject: =?UTF-8?B?V2VkZGluZ0dpZnQg5pel5oqlIDIwMjYtMDktMTc=?=\r\n",
		"MIME-Version: 1.0\r\n",
		"Content-Type: text/plain; charset=UTF-8\r\n",
		"\r\n第一行\r\n第二行\r\n",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("报文缺少 %q:\n%q", want, msg)
		}
	}
	// 正文不应残留裸 LF（SMTP 要求 CRLF）。
	body := msg[strings.Index(msg, "\r\n\r\n")+4:]
	if strings.Contains(strings.ReplaceAll(body, "\r\n", ""), "\n") {
		t.Errorf("正文存在未转换的 LF: %q", body)
	}
}

func TestSMTPConfigEnabled(t *testing.T) {
	full := SMTPConfig{Host: "smtp.qq.com", Port: 465, From: "a@qq.com", To: []string{"b@qq.com"}}
	if !full.Enabled() {
		t.Error("完整配置应 Enabled")
	}
	for name, c := range map[string]SMTPConfig{
		"缺 Host": {Port: 465, From: "a@qq.com", To: []string{"b@qq.com"}},
		"缺 Port": {Host: "smtp.qq.com", From: "a@qq.com", To: []string{"b@qq.com"}},
		"缺 From": {Host: "smtp.qq.com", Port: 465, To: []string{"b@qq.com"}},
		"缺 To":   {Host: "smtp.qq.com", Port: 465, From: "a@qq.com"},
	} {
		if c.Enabled() {
			t.Errorf("%s 不应 Enabled", name)
		}
	}

	if err := SendMail(context.Background(), SMTPConfig{}, "s", "b"); err == nil ||
		!strings.Contains(err.Error(), "未配置") {
		t.Errorf("未配置时 SendMail 应报配置错误，实际 %v", err)
	}
}
