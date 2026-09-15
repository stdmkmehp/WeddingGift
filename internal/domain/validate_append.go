package domain

import (
	"fmt"
	"strings"
)

// ExistingState 是追加模式校验所需的库内现状基线（数据契约 §2.8）。
// 纯数据载体：由 store 层一次性读出并填充（既有礼品凭证明文由 store 用
// app.key 解密后放入，仅存在于调用栈内）；本包不感知数据库与加密。
type ExistingState struct {
	GroupRefs          map[string]string         // 同行组 external_ref -> status
	MemberRefs         map[string]bool           // 成员 external_ref 集合
	DisplayNameNorms   map[string]string         // member.display_name_norm -> 成员 external_ref
	LoginNameNorms     map[string]string         // member_login_name.normalized_value -> 定位描述（external_ref/kind）
	TypeKeys           map[string]bool           // gift_type.type_key 集合
	GiftItemRefs       map[string]bool           // gift_item.external_ref 集合
	OrdinalsByGroup    map[string]map[int]string // 同行组 external_ref -> ordinal -> 礼品 external_ref
	GiftCodePlaintexts map[string]string         // 激活码/卡号明文 -> 礼品 external_ref
}

// ValidateAppend 对追加数据包执行开放世界校验（数据契约 §2.8）：契约 §2 的
// 全部规则在「库内现状 ∪ 本批新增」上成立，并叠加追加语义约束：
//   - 五类实体的 external_ref / type_key 不得已存在于库中——追加只能新增，
//     重新声明既有数据即拒绝（机制上保证不修改、不删除老数据）；
//   - 引用（成员→组、登录名称→成员、礼品→组/类型）可指向库内已有或本批新增；
//   - 既有成员只允许追加 nickname（real_name 会破坏「恰好一条」不变量）；
//   - 「active 组至少一名可登录成员 / 一份 active 礼品」只约束本批新组
//     （既有组的不变量在首次导入时已成立，纯新增不会使其变坏）。
//
// 返回全部问题（不 fail-fast），空切片表示校验通过。本函数只读。
func ValidateAppend(ds *Dataset, ex *ExistingState) []ValidationError {
	var errs []ValidationError
	add := func(file string, row int, format string, args ...any) {
		errs = append(errs, ValidationError{File: file, Row: row, Message: fmt.Sprintf(format, args...)})
	}

	v := &appendValidator{validator: validator{add: add, ds: ds}, ex: ex}
	v.checkGroups()
	v.checkMembers()
	v.checkLoginNames()
	v.checkGiftTypes()
	v.checkGiftItems()
	return errs
}

// appendValidator 嵌入 validator 复用字段长度与兑换入口校验，校验方法按
// 追加语义覆盖（开放世界：库内现状 ∪ 本批新增）。
type appendValidator struct {
	validator
	ex *ExistingState
}

// groupExists 报告同行组引用在库内或本批中存在。
func (v *appendValidator) groupExists(ref string) bool {
	if _, ok := v.ex.GroupRefs[ref]; ok {
		return true
	}
	for _, g := range v.ds.Groups {
		if g.ExternalRef == ref {
			return true
		}
	}
	return false
}

// typeExists 报告礼品类型引用在库内或本批中存在。
func (v *appendValidator) typeExists(key string) bool {
	if v.ex.TypeKeys[key] {
		return true
	}
	for _, gt := range v.ds.GiftTypes {
		if gt.TypeKey == key {
			return true
		}
	}
	return false
}

func (v *appendValidator) checkGroups() {
	seen := make(map[string]int, len(v.ds.Groups))
	for i, g := range v.ds.Groups {
		row := i + 1
		if g.ExternalRef == "" {
			v.add(FileGroups, row, "external_ref 不能为空")
		} else {
			v.checkFieldLen(FileGroups, row, "external_ref", g.ExternalRef, MaxExternalRefLen)
			if _, dup := v.ex.GroupRefs[g.ExternalRef]; dup {
				v.add(FileGroups, row, "external_ref %q 已存在于数据库，追加不允许重新声明或修改既有数据", g.ExternalRef)
			}
			if prev, dup := seen[g.ExternalRef]; dup {
				v.add(FileGroups, row, "external_ref %q 与第 %d 行重复", g.ExternalRef, prev)
			}
			seen[g.ExternalRef] = row
		}
		if strings.TrimSpace(g.Salutation) == "" {
			v.add(FileGroups, row, "同行组 %s 称呼 salutation 不能为空", g.ExternalRef)
		}
		v.checkFieldLen(FileGroups, row, "salutation", g.Salutation, MaxSalutationLen)
		v.checkFieldLen(FileGroups, row, "custom_message", g.CustomMessage, MaxCustomMessageLen)
		if !IsValidStatus(g.Status) {
			v.add(FileGroups, row, "同行组 %s status 非法（%q，仅支持 active/disabled）", g.ExternalRef, g.Status)
		}
	}
}

func (v *appendValidator) checkMembers() {
	seenRef := make(map[string]int, len(v.ds.Members))
	seenNorm := make(map[string]string) // display_name_norm -> external_ref（本批）
	canLoginCount := make(map[string]int)

	for i, m := range v.ds.Members {
		row := i + 1
		if m.ExternalRef == "" {
			v.add(FileMembers, row, "external_ref 不能为空")
		} else {
			v.checkFieldLen(FileMembers, row, "external_ref", m.ExternalRef, MaxExternalRefLen)
			if v.ex.MemberRefs[m.ExternalRef] {
				v.add(FileMembers, row, "external_ref %q 已存在于数据库，追加不允许重新声明或修改既有数据", m.ExternalRef)
			}
			if prev, dup := seenRef[m.ExternalRef]; dup {
				v.add(FileMembers, row, "external_ref %q 与第 %d 行重复", m.ExternalRef, prev)
			}
			seenRef[m.ExternalRef] = row
		}
		if !v.groupExists(m.GroupExternalRef) {
			v.add(FileMembers, row, "成员 %s 引用的同行组 %q 不存在（库内与本批均无）", m.ExternalRef, m.GroupExternalRef)
		}
		norm := NormalizeLoginName(m.DisplayName)
		if norm == "" {
			v.add(FileMembers, row, "成员 %s 本名 display_name 不能为空", m.ExternalRef)
		} else {
			v.checkFieldLen(FileMembers, row, "display_name", norm, MaxNameLen)
			if prevRef, dup := v.ex.DisplayNameNorms[norm]; dup {
				v.add(FileMembers, row, "本名 %q 与库内成员 %s 的本名规范化后重复", norm, prevRef)
			}
			if prevRef, dup := seenNorm[norm]; dup {
				v.add(FileMembers, row, "本名 %q 与成员 %s 的本名规范化后重复", norm, prevRef)
			}
			seenNorm[norm] = m.ExternalRef
		}
		if m.SortOrder < 1 {
			v.add(FileMembers, row, "成员 %s sort_order 必须从 1 起", m.ExternalRef)
		}
		if m.CanLogin {
			canLoginCount[m.GroupExternalRef]++
		}
	}

	// 每个本批新增 active 同行组至少一名可登录成员（既有组已满足，不重查）。
	for _, g := range v.ds.Groups {
		if g.Status == StatusActive && canLoginCount[g.ExternalRef] == 0 {
			v.add(FileGroups, 0, "active 同行组 %s 至少需要一名 can_login=true 成员", g.ExternalRef)
		}
	}
}

func (v *appendValidator) checkLoginNames() {
	batchMember := make(map[string]bool, len(v.ds.Members))
	memberCanLogin := make(map[string]bool, len(v.ds.Members))
	memberDisplayNorm := make(map[string]string, len(v.ds.Members))
	for _, m := range v.ds.Members {
		batchMember[m.ExternalRef] = true
		memberCanLogin[m.ExternalRef] = m.CanLogin
		memberDisplayNorm[m.ExternalRef] = NormalizeLoginName(m.DisplayName)
	}

	// 登录名称规范化后全局唯一（库内 ∪ 本批）；冲突含跨成员与跨本名/昵称。
	seenNorm := make(map[string]string) // norm -> 定位描述（本批）
	realNameCount := make(map[string]int)
	realNameValue := make(map[string]string)

	for i, ln := range v.ds.LoginNames {
		row := i + 1
		inBatch := batchMember[ln.MemberExternalRef]
		if !inBatch && !v.ex.MemberRefs[ln.MemberExternalRef] {
			v.add(FileLoginNames, row, "登录名称引用的成员 %q 不存在（库内与本批均无）", ln.MemberExternalRef)
		}
		if !inBatch && v.ex.MemberRefs[ln.MemberExternalRef] && ln.Kind == LoginNameRealName {
			v.add(FileLoginNames, row, "不允许为既有成员 %s 追加 real_name 登录名称（仅允许 nickname）", ln.MemberExternalRef)
		}
		if !IsValidLoginNameKind(ln.Kind) {
			v.add(FileLoginNames, row, "登录名称 %q kind 非法（%q，仅支持 real_name/nickname）", ln.Value, ln.Kind)
		}
		norm := NormalizeLoginName(ln.Value)
		if norm == "" {
			v.add(FileLoginNames, row, "登录名称规范化后为空")
		} else {
			v.checkFieldLen(FileLoginNames, row, "登录名称", norm, MaxNameLen)
			if prev, dup := v.ex.LoginNameNorms[norm]; dup {
				v.add(FileLoginNames, row, "登录名称 %q 与库内 %s 规范化后冲突（所有本名与昵称必须全局唯一）", norm, prev)
			}
			loc := ln.MemberExternalRef + "/" + ln.Kind
			if prev, dup := seenNorm[norm]; dup {
				v.add(FileLoginNames, row, "登录名称 %q 与 %s 规范化后冲突（所有本名与昵称必须全局唯一）", norm, prev)
			}
			seenNorm[norm] = loc
		}
		if inBatch && ln.Kind == LoginNameRealName {
			realNameCount[ln.MemberExternalRef]++
			realNameValue[ln.MemberExternalRef] = norm
		}
	}

	// 每位本批 can_login=true 成员必须恰好一条 real_name，且与 display_name 一致。
	for _, m := range v.ds.Members {
		if !m.CanLogin {
			continue
		}
		switch realNameCount[m.ExternalRef] {
		case 0:
			v.add(FileLoginNames, 0, "可登录成员 %s（%s）缺少 kind=real_name 的登录名称", m.ExternalRef, m.DisplayName)
		case 1:
			if realNameValue[m.ExternalRef] != memberDisplayNorm[m.ExternalRef] {
				v.add(FileLoginNames, 0, "成员 %s 的 real_name 登录名称 %q 与本名 %q 不一致",
					m.ExternalRef, realNameValue[m.ExternalRef], memberDisplayNorm[m.ExternalRef])
			}
		default:
			v.add(FileLoginNames, 0, "可登录成员 %s 有 %d 条 real_name 登录名称，必须恰好一条", m.ExternalRef, realNameCount[m.ExternalRef])
		}
	}
}

func (v *appendValidator) checkGiftTypes() {
	seen := make(map[string]int, len(v.ds.GiftTypes))
	for i, gt := range v.ds.GiftTypes {
		row := i + 1
		if !IsValidGiftTypeKey(gt.TypeKey) {
			v.add(FileGiftTypes, row, "type_key %q 非法（小写字母开头的字母/数字/下划线，最长 32）", gt.TypeKey)
		} else {
			if v.ex.TypeKeys[gt.TypeKey] {
				v.add(FileGiftTypes, row, "type_key %q 已存在于数据库，追加不允许重新声明或修改既有数据", gt.TypeKey)
			}
			if prev, dup := seen[gt.TypeKey]; dup {
				v.add(FileGiftTypes, row, "type_key %q 与第 %d 行重复", gt.TypeKey, prev)
			}
			seen[gt.TypeKey] = row
		}
		if strings.TrimSpace(gt.Title) == "" {
			v.add(FileGiftTypes, row, "礼品类型 %s title 不能为空", gt.TypeKey)
		}
		if strings.TrimSpace(gt.DefaultInstructions) == "" {
			v.add(FileGiftTypes, row, "礼品类型 %s default_instructions 不能为空", gt.TypeKey)
		}
		v.checkFieldLen(FileGiftTypes, row, "title", gt.Title, MaxTitleLen)
		v.checkFieldLen(FileGiftTypes, row, "subtitle", gt.Subtitle, MaxSubtitleLen)
		v.checkFieldLen(FileGiftTypes, row, "default_instructions", gt.DefaultInstructions, MaxInstructionsLen)
		v.checkFieldLen(FileGiftTypes, row, "default_validity_text", gt.DefaultValidityText, MaxValidityTextLen)
		v.checkFieldLen(FileGiftTypes, row, "theme_variant", gt.ThemeVariant, MaxThemeVariantLen)
		v.checkRedemptionURL(FileGiftTypes, row, "default_redemption_url", gt.DefaultRedemptionURL)
	}

	// 礼品类型完全由数据文件驱动（D-13 修订）：新增类型只需追加数据文件。
}

func (v *appendValidator) checkGiftItems() {
	seenRef := make(map[string]int, len(v.ds.GiftItems))
	seenCode := make(map[string]string)            // code_plaintext（激活码或卡号） -> external_ref（本批）
	seenOrdinal := make(map[string]map[int]string) // group -> ordinal -> external_ref（本批）
	activeGiftCount := make(map[string]int)

	for i, gi := range v.ds.GiftItems {
		row := i + 1
		if gi.ExternalRef == "" {
			v.add(FileGiftItems, row, "external_ref 不能为空")
		} else {
			v.checkFieldLen(FileGiftItems, row, "external_ref", gi.ExternalRef, MaxExternalRefLen)
			if v.ex.GiftItemRefs[gi.ExternalRef] {
				v.add(FileGiftItems, row, "external_ref %q 已存在于数据库，追加不允许重新声明或修改既有数据", gi.ExternalRef)
			}
			if prev, dup := seenRef[gi.ExternalRef]; dup {
				v.add(FileGiftItems, row, "external_ref %q 与第 %d 行重复", gi.ExternalRef, prev)
			}
			seenRef[gi.ExternalRef] = row
		}
		if !v.groupExists(gi.GroupExternalRef) {
			v.add(FileGiftItems, row, "礼品 %s 引用的同行组 %q 不存在（库内与本批均无）", gi.ExternalRef, gi.GroupExternalRef)
		}
		if !v.typeExists(gi.TypeKey) {
			v.add(FileGiftItems, row, "礼品 %s 引用的礼品类型 %q 不存在（库内与本批均无）", gi.ExternalRef, gi.TypeKey)
		}
		if !IsValidCredentialMode(gi.CredentialMode) {
			v.add(FileGiftItems, row, "礼品 %s credential_mode 非法（%q，仅支持 code/card_secret）", gi.ExternalRef, gi.CredentialMode)
		}
		// code_plaintext 在两种模式下分别为激活码/卡号，全局唯一约束一致
		//（库内 ∪ 本批）；密码（secret_plaintext）不做唯一性要求。
		codeLabel := "激活码"
		if gi.CredentialMode == CredentialModeCardSecret {
			codeLabel = "卡号"
		}
		if strings.TrimSpace(gi.CodePlaintext) == "" {
			v.add(FileGiftItems, row, "礼品 %s %s不能为空", gi.ExternalRef, codeLabel)
		} else {
			v.checkFieldLen(FileGiftItems, row, codeLabel, gi.CodePlaintext, MaxGiftCodeLen)
			if prevRef, dup := v.ex.GiftCodePlaintexts[gi.CodePlaintext]; dup {
				v.add(FileGiftItems, row, "礼品 %s %s与库内既有礼品 %s 重复", gi.ExternalRef, codeLabel, prevRef)
			}
			if prevRef, dup := seenCode[gi.CodePlaintext]; dup {
				v.add(FileGiftItems, row, "礼品 %s %s与礼品 %s 重复", gi.ExternalRef, codeLabel, prevRef)
			}
			seenCode[gi.CodePlaintext] = gi.ExternalRef
		}
		switch gi.CredentialMode {
		case CredentialModeCode:
			if strings.TrimSpace(gi.SecretPlaintext) != "" {
				v.add(FileGiftItems, row, "礼品 %s credential_mode=code 时 secret_plaintext 必须为空", gi.ExternalRef)
			}
		case CredentialModeCardSecret:
			if strings.TrimSpace(gi.SecretPlaintext) == "" {
				v.add(FileGiftItems, row, "礼品 %s 密码不能为空（card_secret 模式）", gi.ExternalRef)
			} else {
				v.checkFieldLen(FileGiftItems, row, "密码", gi.SecretPlaintext, MaxGiftSecretLen)
			}
		}
		if gi.Ordinal < 1 {
			v.add(FileGiftItems, row, "礼品 %s ordinal 必须从 1 起", gi.ExternalRef)
		} else {
			if prevRef, dup := v.ex.OrdinalsByGroup[gi.GroupExternalRef][gi.Ordinal]; dup {
				v.add(FileGiftItems, row, "礼品 %s 与库内既有礼品 %s 在组 %s 内 ordinal %d 重复", gi.ExternalRef, prevRef, gi.GroupExternalRef, gi.Ordinal)
			}
			if seenOrdinal[gi.GroupExternalRef] == nil {
				seenOrdinal[gi.GroupExternalRef] = make(map[int]string)
			}
			if prevRef, dup := seenOrdinal[gi.GroupExternalRef][gi.Ordinal]; dup {
				v.add(FileGiftItems, row, "礼品 %s 与礼品 %s 在组 %s 内 ordinal %d 重复", gi.ExternalRef, prevRef, gi.GroupExternalRef, gi.Ordinal)
			}
			seenOrdinal[gi.GroupExternalRef][gi.Ordinal] = gi.ExternalRef
		}
		if !IsValidStatus(gi.Status) {
			v.add(FileGiftItems, row, "礼品 %s status 非法（%q）", gi.ExternalRef, gi.Status)
		}
		v.checkFieldLen(FileGiftItems, row, "instructions_override", gi.InstructionsOverride, MaxInstructionsLen)
		v.checkFieldLen(FileGiftItems, row, "validity_text_override", gi.ValidityTextOverride, MaxValidityTextLen)
		v.checkRedemptionURL(FileGiftItems, row, "redemption_url_override", gi.RedemptionURLOverride)
		if gi.Status == StatusActive {
			activeGiftCount[gi.GroupExternalRef]++
		}
	}

	// 每个本批新增 active 同行组至少一份 active 礼品（既有组已满足，不重查）。
	for _, g := range v.ds.Groups {
		if g.Status == StatusActive && activeGiftCount[g.ExternalRef] == 0 {
			v.add(FileGiftItems, 0, "active 同行组 %s 至少需要一份 active 礼品", g.ExternalRef)
		}
	}
}
