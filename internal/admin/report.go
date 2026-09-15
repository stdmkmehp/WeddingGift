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
	writeGiftDistribution(&b, ds)
	return b.String()
}

// AppendReport 生成追加数据包的脱敏核对报告（数据契约 §2.8）：五类实体的新增
// 数量、各类型/各组礼品分布，以及对库内既有同行组/成员/礼品类型的引用清单
// （仅 external_ref，字典序）。报告只含内部编号与数量，不含明文礼品凭证、
// 姓名或昵称。
func AppendReport(ds *domain.Dataset, ex *domain.ExistingState) string {
	var b strings.Builder

	fmt.Fprintf(&b, "同行组（新增）: %d（active %d）\n", len(ds.Groups), countGroups(ds, domain.StatusActive))
	fmt.Fprintf(&b, "成员（新增）: %d（可登录 %d）\n", len(ds.Members), countCanLogin(ds))
	fmt.Fprintf(&b, "登录名称（新增）: %d\n", len(ds.LoginNames))
	fmt.Fprintf(&b, "礼品类型（新增）: %d\n", len(ds.GiftTypes))
	fmt.Fprintf(&b, "礼品实例（新增）: %d（active %d）\n", len(ds.GiftItems), countActiveGifts(ds))
	writeGiftDistribution(&b, ds)

	// 引用既有实体清单：追加包引用了库内哪些组/成员/类型（校验通过后，
	// 这些引用必然不是重新声明）。
	writeRefList(&b, "引用既有同行组", referencedExistingGroups(ds, ex))
	writeRefList(&b, "引用既有成员", referencedExistingMembers(ds, ex))
	writeRefList(&b, "引用既有礼品类型", referencedExistingTypes(ds, ex))
	return b.String()
}

// writeGiftDistribution 输出各类型礼品与各组礼品的数量分布（输出稳定有序）。
func writeGiftDistribution(b *strings.Builder, ds *domain.Dataset) {
	byType := make(map[string]int)
	byGroup := make(map[string]int)
	for _, gi := range ds.GiftItems {
		byType[gi.TypeKey]++
		byGroup[gi.GroupExternalRef]++
	}
	writeCountMap(b, "各类型礼品", byType)
	writeCountMap(b, "每组礼品", byGroup)
}

func writeCountMap(b *strings.Builder, label string, counts map[string]int) {
	keys := sortedKeys(counts)
	if len(keys) == 0 {
		return
	}
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	fmt.Fprintf(b, "%s: %s\n", label, strings.Join(parts, ", "))
}

func writeRefList(b *strings.Builder, label string, refs []string) {
	if len(refs) == 0 {
		return
	}
	fmt.Fprintf(b, "%s: %s\n", label, strings.Join(refs, ", "))
}

// referencedExistingGroups 返回追加包引用的库内既有同行组（来自成员与礼品的引用）。
func referencedExistingGroups(ds *domain.Dataset, ex *domain.ExistingState) []string {
	set := make(map[string]bool)
	for _, m := range ds.Members {
		if _, ok := ex.GroupRefs[m.GroupExternalRef]; ok {
			set[m.GroupExternalRef] = true
		}
	}
	for _, gi := range ds.GiftItems {
		if _, ok := ex.GroupRefs[gi.GroupExternalRef]; ok {
			set[gi.GroupExternalRef] = true
		}
	}
	return sortedKeys(set)
}

// referencedExistingMembers 返回追加包引用的库内既有成员（来自登录名称的引用）。
func referencedExistingMembers(ds *domain.Dataset, ex *domain.ExistingState) []string {
	set := make(map[string]bool)
	for _, ln := range ds.LoginNames {
		if ex.MemberRefs[ln.MemberExternalRef] {
			set[ln.MemberExternalRef] = true
		}
	}
	return sortedKeys(set)
}

// referencedExistingTypes 返回追加包引用的库内既有礼品类型（来自礼品的引用）。
func referencedExistingTypes(ds *domain.Dataset, ex *domain.ExistingState) []string {
	set := make(map[string]bool)
	for _, gi := range ds.GiftItems {
		if ex.TypeKeys[gi.TypeKey] {
			set[gi.TypeKey] = true
		}
	}
	return sortedKeys(set)
}

// sortedKeys 返回 map 键的字典序切片（计数语义下值为非空集合）。
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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
// 组明细带组称呼，便于在服务器本地直观识别同行组；仍不显示成员姓名、
// 登录名称、明文密钥与礼品码。
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
		b.WriteString("\n同行组明细（external_ref | 称呼 | 状态 | 成员数 | active 礼品数 | 礼品类型 | 已绑定密钥数）:\n")
		for _, gs := range s.GroupStats {
			fmt.Fprintf(&b, "  %s | %s | %s | %d | %d | %s | %d\n",
				gs.ExternalRef, gs.Salutation, gs.Status, gs.MemberCount, gs.GiftCount,
				orDash(gs.GiftTypes), gs.BoundKeys)
		}
	}
	return b.String()
}

// orDash 把空串渲染为 "-"，避免明细表出现空列难以阅读。
func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
