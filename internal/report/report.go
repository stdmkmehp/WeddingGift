// Package report 实现每日统计日报：滚动时间窗口统计汇总、纯文本邮件渲染
// 与 SMTP 发送。邮件只发往项目所有者本人邮箱，正文含同行组称呼明细，
// 不做脱敏（收件人即管理员）。
package report

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/weddinggift/wedding/internal/domain"
	"github.com/weddinggift/wedding/internal/store"
)

// beijing 是固定的 UTC+8 展示时区。运行镜像为 scratch、无 tzdata，
// 且中国无夏令时，固定偏移即正确。
var beijing = time.FixedZone("CST", 8*3600)

// Summary 汇总一份日报所需的全部数据。
type Summary struct {
	// GeneratedAt 是报告生成时刻（UTC）。
	GeneratedAt time.Time
	// Window 是滚动统计窗口宽度；区间为 [GeneratedAt-Window, GeneratedAt)。
	Window time.Duration
	// Bindings 是窗口内新绑定的密钥数。
	Bindings int
	// Logins 是窗口内成功登录创建会话的次数。
	Logins int
	// Totals 是截至生成时刻的累积统计。
	Totals *store.Stats
}

// Collect 以 now 为窗口右边界、window 为宽度收集日报数据。
func Collect(ctx context.Context, db *store.DB, now time.Time, window time.Duration) (*Summary, error) {
	until := now.UTC()
	since := until.Add(-window)
	w, err := db.WindowStats(ctx, since.Format(time.RFC3339), until.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	totals, err := db.Stats(ctx)
	if err != nil {
		return nil, err
	}
	return &Summary{
		GeneratedAt: until,
		Window:      window,
		Bindings:    w.Bindings,
		Logins:      w.Logins,
		Totals:      totals,
	}, nil
}

// RenderDaily 渲染 serve 进程内调度的日报：统计 + 内存缓冲中的全部登录失败
// 明细。failures 为 nil 时表示缓冲当前为空。正文面向项目所有者本人，
// 含同行组绑定情况明细（已绑定组列密钥编号、未绑定组列待办）与失败输入情况，
// 不做脱敏。
func RenderDaily(s *Summary, failures []LoginFailure) (subject, body string) {
	var fb strings.Builder
	windowFailures := 0
	for _, f := range failures {
		if !f.At.Before(s.GeneratedAt.Add(-s.Window)) && f.At.Before(s.GeneratedAt) {
			windowFailures++
		}
	}
	fmt.Fprintf(&fb, "\n■ 登录失败明细（内存记录 %d 条，其中窗口内 %d 条；进程重启即清空）\n",
		len(failures), windowFailures)
	if len(failures) == 0 {
		fb.WriteString("  当前无登录失败记录。\n")
	} else {
		renderFailures(&fb, failures)
	}
	subject, body = renderDaily(s, fb.String())
	return subject, body
}

// RenderDailyStatsOnly 渲染 CLI（wedding report daily）的纯统计版日报：
// 登录失败缓冲只存在于 serve 进程内存，独立 CLI 进程不可见，正文如实注明。
// 用于人工核对统计口径与验证 SMTP 出网链路。
func RenderDailyStatsOnly(s *Summary) (subject, body string) {
	return renderDaily(s, "\n■ 登录失败明细\n  （仅存在于 serve 进程内存，由其内置日报包含；本手动报告不含）\n")
}

// RenderOverflowAlert 渲染登录失败缓冲溢出告警邮件：缓冲超过上限时的即时
// 通知，发送后缓冲清理最早 evicted 条。正文面向项目所有者本人，不做脱敏。
func RenderOverflowAlert(failures []LoginFailure, limit, evicted int) (subject, body string) {
	subject = fmt.Sprintf("WeddingGift 登录失败告警 %s", time.Now().In(beijing).Format("2006-01-02 15:04"))
	var b strings.Builder
	fmt.Fprintf(&b, "登录失败内存缓冲已达上限 %d 条，触发即时告警。\n", limit)
	fmt.Fprintf(&b, "以下为当前全部 %d 条记录（北京时间，升序）；本邮件发送后缓冲将清理最早 %d 条，保留最新记录。\n",
		len(failures), evicted)
	b.WriteString("\n■ 登录失败明细\n")
	renderFailures(&b, failures)
	return subject, b.String()
}

// renderDaily 渲染日报的统计部分，并把给定的失败明细小节拼入正文。
func renderDaily(s *Summary, failuresSection string) (subject, body string) {
	bj := func(t time.Time) string { return t.In(beijing).Format("2006-01-02 15:04") }
	subject = fmt.Sprintf("WeddingGift 日报 %s", s.GeneratedAt.In(beijing).Format("2006-01-02"))

	t := s.Totals
	unbound := t.AccessKeys - t.KeysBound
	bindRate := 0.0
	if t.AccessKeys > 0 {
		bindRate = float64(t.KeysBound) / float64(t.AccessKeys) * 100
	}

	var b strings.Builder
	fmt.Fprintf(&b, "统计窗口：%s ～ %s（北京时间，滚动 %g 小时）\n",
		bj(s.GeneratedAt.Add(-s.Window)), bj(s.GeneratedAt), s.Window.Hours())

	b.WriteString("\n■ 窗口内新增\n")
	fmt.Fprintf(&b, "  新绑定密钥：%d 个\n", s.Bindings)
	fmt.Fprintf(&b, "  成功登录：%d 人次\n", s.Logins)

	b.WriteString("\n■ 累计数据\n")
	fmt.Fprintf(&b, "  同行组：%d（active %d）\n", t.Groups, t.GroupsActive)
	fmt.Fprintf(&b, "  成员：%d（可登录 %d）\n", t.Members, t.MembersCanLogin)
	fmt.Fprintf(&b, "  登录名称：%d\n", t.LoginNames)
	fmt.Fprintf(&b, "  礼品类型：%d 种\n", t.GiftTypes)
	fmt.Fprintf(&b, "  礼品实例：%d 份（active %d）\n", t.GiftItems, t.GiftItemsActive)
	fmt.Fprintf(&b, "  卡片密钥：%d 张（active %d / disabled %d）\n", t.AccessKeys, t.KeysActive, t.KeysDisabled)
	fmt.Fprintf(&b, "    已绑定 %d / 未绑定 %d（绑定率 %.1f%%）\n", t.KeysBound, unbound, bindRate)
	fmt.Fprintf(&b, "  当前有效会话：%d\n", t.Sessions)

	// 绑定情况两组明细：已绑定组列出绑定的密钥编号（含已停用组，标注状态）；
	// 未绑定组只列 active 组——disabled 组不再引导来宾，不属于待办。
	var bound, pending []store.GroupStat
	for _, gs := range t.GroupStats {
		if gs.BoundKeys > 0 {
			bound = append(bound, gs)
		} else if gs.Status == domain.StatusActive {
			pending = append(pending, gs)
		}
	}
	writeGroupSection(&b, "已绑定密钥的同行组", bound, true)
	writeGroupSection(&b, "尚未绑定密钥的同行组", pending, false)

	b.WriteString(failuresSection)
	return subject, b.String()
}

// writeGroupSection 渲染一组同行组明细：bound=true 时列出该组已绑定的密钥
// 编号，否则标注「尚未绑定密钥」作为待办；两组都给出成员数、active 礼品数
// 与覆盖的礼品类型。明细面向项目所有者本人，含组称呼，不做脱敏。
func writeGroupSection(b *strings.Builder, title string, groups []store.GroupStat, bound bool) {
	fmt.Fprintf(b, "\n■ %s（%d 个）\n", title, len(groups))
	if len(groups) == 0 {
		if bound {
			b.WriteString("  尚无同行组绑定密钥。\n")
		} else {
			b.WriteString("  全部 active 同行组均已有密钥绑定。\n")
		}
		return
	}
	for _, gs := range groups {
		if bound {
			status := ""
			if gs.Status != domain.StatusActive {
				status = "（已停用）"
			}
			fmt.Fprintf(b, "  - %s %s%s：密钥 %s｜成员 %d 人｜active 礼品 %d 份（%s）\n",
				gs.ExternalRef, gs.Salutation, status, gs.BoundKeyRefs,
				gs.MemberCount, gs.GiftCount, orDash(gs.GiftTypes))
			continue
		}
		fmt.Fprintf(b, "  - %s %s：尚未绑定密钥｜成员 %d 人｜active 礼品 %d 份（%s）\n",
			gs.ExternalRef, gs.Salutation, gs.MemberCount, gs.GiftCount, orDash(gs.GiftTypes))
	}
}

// orDash 把空串渲染为 "-"，避免明细行出现空段难以阅读。
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
