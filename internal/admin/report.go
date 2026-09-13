package admin

import (
	"fmt"
	"sort"
	"strings"

	"github.com/weddinggift/wedding/internal/domain"
	"github.com/weddinggift/wedding/internal/store"
)

// DatasetReport 生成数据包的脱敏核对报告（产品架构 §15）：
// 组数、成员数、登录名称数、礼品数、卡片数、各礼品类型数量、每组礼品数量。
// 报告只含内部编号与数量，不含明文密钥、激活码、姓名或昵称。
func DatasetReport(ds *domain.Dataset) string {
	var b strings.Builder

	fmt.Fprintf(&b, "同行组: %d（active %d）\n", len(ds.Groups), countGroups(ds, domain.StatusActive))
	fmt.Fprintf(&b, "成员: %d（可登录 %d）\n", len(ds.Members), countCanLogin(ds))
	fmt.Fprintf(&b, "登录名称: %d\n", len(ds.LoginNames))
	fmt.Fprintf(&b, "礼品类型: %d\n", len(ds.GiftTypes))
	fmt.Fprintf(&b, "礼品实例: %d（active %d）\n", len(ds.GiftItems), countActiveGifts(ds))
	fmt.Fprintf(&b, "卡片密钥: %d\n", len(ds.AccessKeys))

	// 各礼品类型实例数量（按 type_key 排序，输出稳定）。
	byType := make(map[string]int)
	for _, gi := range ds.GiftItems {
		byType[gi.TypeKey]++
	}
	if len(byType) > 0 {
		keys := make([]string, 0, len(byType))
		for k := range byType {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		var parts []string
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", k, byType[k]))
		}
		fmt.Fprintf(&b, "各类型礼品: %s\n", strings.Join(parts, ", "))
	}

	// 每组礼品数量（按 external_ref 排序）。
	byGroup := make(map[string]int)
	for _, gi := range ds.GiftItems {
		byGroup[gi.GroupExternalRef]++
	}
	if len(byGroup) > 0 {
		refs := make([]string, 0, len(byGroup))
		for r := range byGroup {
			refs = append(refs, r)
		}
		sort.Strings(refs)
		var parts []string
		for _, r := range refs {
			parts = append(parts, fmt.Sprintf("%s=%d", r, byGroup[r]))
		}
		fmt.Fprintf(&b, "每组礼品: %s\n", strings.Join(parts, ", "))
	}
	return b.String()
}

func countGroups(ds *domain.Dataset, status string) int {
	n := 0
	for _, g := range ds.Groups {
		if g.Status == status {
			n++
		}
	}
	return n
}

func countCanLogin(ds *domain.Dataset) int {
	n := 0
	for _, m := range ds.Members {
		if m.CanLogin {
			n++
		}
	}
	return n
}

func countActiveGifts(ds *domain.Dataset) int {
	n := 0
	for _, gi := range ds.GiftItems {
		if gi.Status == domain.StatusActive {
			n++
		}
	}
	return n
}

// StatusText 渲染 store.Stats 为脱敏文本报告（status 子命令输出）。
// 只含内部编号与数量，不显示明文密钥或礼品码。
func StatusText(s *store.Stats) string {
	var b strings.Builder

	fmt.Fprintf(&b, "schema 版本: %d\n", s.SchemaVersion)
	fmt.Fprintf(&b, "同行组: %d（active %d）\n", s.Groups, s.GroupsActive)
	fmt.Fprintf(&b, "成员: %d（可登录 %d）\n", s.Members, s.MembersCanLogin)
	fmt.Fprintf(&b, "登录名称: %d\n", s.LoginNames)
	fmt.Fprintf(&b, "礼品类型: %d\n", s.GiftTypes)
	fmt.Fprintf(&b, "礼品实例: %d（active %d）\n", s.GiftItems, s.GiftItemsActive)
	fmt.Fprintf(&b, "卡片密钥: %d（active %d / disabled %d，已绑定 %d / 未绑定 %d）\n",
		s.AccessKeys, s.KeysActive, s.KeysDisabled, s.KeysBound, s.AccessKeys-s.KeysBound)
	fmt.Fprintf(&b, "活跃会话: %d\n", s.Sessions)

	if len(s.GroupStats) > 0 {
		b.WriteString("\n同行组明细（external_ref | 状态 | 成员数 | active 礼品数 | 已绑定密钥数）:\n")
		for _, gs := range s.GroupStats {
			fmt.Fprintf(&b, "  %s | %s | %d | %d | %d\n", gs.ExternalRef, gs.Status, gs.MemberCount, gs.GiftCount, gs.BoundKeys)
		}
	}
	return b.String()
}
