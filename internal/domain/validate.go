package domain

import (
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"
)

// ValidationError 描述数据包中的一处校验失败，带定位信息（文件名与行号）。
type ValidationError struct {
	File    string // 来源文件名，如 members.csv
	Row     int    // 数据行号（从 1 起，不含表头）；0 表示文件级或跨表问题
	Message string
}

// Error 实现 error 接口。
func (e ValidationError) Error() string {
	if e.File == "" {
		return e.Message
	}
	if e.Row == 0 {
		return e.File + ": " + e.Message
	}
	return fmt.Sprintf("%s 第 %d 行: %s", e.File, e.Row, e.Message)
}

// 数据文件名常量，与 testdata/fixtures/ 及契约 §2 一致。
const (
	FileGroups     = "recipient_groups.csv"
	FileMembers    = "members.csv"
	FileLoginNames = "member_login_names.csv"
	FileGiftTypes  = "gift_types.csv"
	FileGiftItems  = "gift_items.csv"
	FileAccessKeys = "access_keys.csv"
)

// Validate 对完整数据包执行数据契约 §2 的全部校验（WG-303）。
// 返回全部问题（不 fail-fast），空切片表示校验通过。
// 本函数只读，不做任何写入。
func Validate(ds *Dataset) []ValidationError {
	var errs []ValidationError
	add := func(file string, row int, format string, args ...any) {
		errs = append(errs, ValidationError{File: file, Row: row, Message: fmt.Sprintf(format, args...)})
	}

	v := &validator{add: add, ds: ds}
	v.checkGroups()
	v.checkMembers()
	v.checkLoginNames()
	v.checkGiftTypes()
	v.checkGiftItems()
	v.checkAccessKeys()
	return errs
}

// validator 持有校验过程中的派生索引，避免重复构建。
type validator struct {
	add func(file string, row int, format string, args ...any)
	ds  *Dataset
}

func runeLen(s string) int { return utf8.RuneCountInString(s) }

// checkFieldLen 校验字段长度，空值由调用方先行判断必填性。
func (v *validator) checkFieldLen(file string, row int, name, value string, max int) {
	if runeLen(value) > max {
		v.add(file, row, "%s 超长（%d 字符，上限 %d）", name, runeLen(value), max)
	}
}

// checkRedemptionURL 校验可选兑换入口字段：非空时必须是合法 HTTPS URL
// 或微信小程序口令（#小程序://名称/路径，契约 §2.5 修订）。
func (v *validator) checkRedemptionURL(file string, row int, name, raw string) {
	if raw == "" {
		return
	}
	if strings.HasPrefix(raw, MiniProgramLinkPrefix) {
		rest := strings.TrimPrefix(raw, MiniProgramLinkPrefix)
		appName, path, ok := strings.Cut(rest, "/")
		if !ok || strings.TrimSpace(appName) == "" || strings.TrimSpace(path) == "" {
			v.add(file, row, "%s 小程序口令格式非法（应为 %s名称/路径）", name, MiniProgramLinkPrefix)
			return
		}
		v.checkFieldLen(file, row, name, raw, MaxURLLen)
		return
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		v.add(file, row, "%s 必须是 HTTPS 链接或小程序口令（%s名称/路径）", name, MiniProgramLinkPrefix)
		return
	}
	v.checkFieldLen(file, row, name, raw, MaxURLLen)
}

func (v *validator) checkGroups() {
	seen := make(map[string]int, len(v.ds.Groups))
	for i, g := range v.ds.Groups {
		row := i + 1
		if g.ExternalRef == "" {
			v.add(FileGroups, row, "external_ref 不能为空")
		} else {
			v.checkFieldLen(FileGroups, row, "external_ref", g.ExternalRef, MaxExternalRefLen)
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

func (v *validator) checkMembers() {
	groupOK := make(map[string]bool, len(v.ds.Groups))
	groupStatus := make(map[string]string, len(v.ds.Groups))
	for _, g := range v.ds.Groups {
		groupOK[g.ExternalRef] = true
		groupStatus[g.ExternalRef] = g.Status
	}

	seenRef := make(map[string]int, len(v.ds.Members))
	seenNorm := make(map[string]string) // display_name_norm -> external_ref
	canLoginCount := make(map[string]int)

	for i, m := range v.ds.Members {
		row := i + 1
		if m.ExternalRef == "" {
			v.add(FileMembers, row, "external_ref 不能为空")
		} else {
			v.checkFieldLen(FileMembers, row, "external_ref", m.ExternalRef, MaxExternalRefLen)
			if prev, dup := seenRef[m.ExternalRef]; dup {
				v.add(FileMembers, row, "external_ref %q 与第 %d 行重复", m.ExternalRef, prev)
			}
			seenRef[m.ExternalRef] = row
		}
		if !groupOK[m.GroupExternalRef] {
			v.add(FileMembers, row, "成员 %s 引用的同行组 %q 不存在", m.ExternalRef, m.GroupExternalRef)
		}
		norm := NormalizeLoginName(m.DisplayName)
		if norm == "" {
			v.add(FileMembers, row, "成员 %s 本名 display_name 不能为空", m.ExternalRef)
		} else {
			v.checkFieldLen(FileMembers, row, "display_name", norm, MaxNameLen)
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

	// 每个 active 同行组至少一名可登录成员（契约 §2.3）。
	for _, g := range v.ds.Groups {
		if g.Status == StatusActive && canLoginCount[g.ExternalRef] == 0 {
			v.add(FileGroups, 0, "active 同行组 %s 至少需要一名 can_login=true 成员", g.ExternalRef)
		}
	}
}

func (v *validator) checkLoginNames() {
	memberOK := make(map[string]bool, len(v.ds.Members))
	memberCanLogin := make(map[string]bool, len(v.ds.Members))
	memberDisplayNorm := make(map[string]string, len(v.ds.Members))
	for _, m := range v.ds.Members {
		memberOK[m.ExternalRef] = true
		memberCanLogin[m.ExternalRef] = m.CanLogin
		memberDisplayNorm[m.ExternalRef] = NormalizeLoginName(m.DisplayName)
	}

	// 登录名称规范化后全局唯一（契约 §2.4）；冲突含跨成员与跨本名/昵称。
	seenNorm := make(map[string]string) // norm -> 定位描述
	realNameCount := make(map[string]int)
	realNameValue := make(map[string]string)

	for i, ln := range v.ds.LoginNames {
		row := i + 1
		if !memberOK[ln.MemberExternalRef] {
			v.add(FileLoginNames, row, "登录名称引用的成员 %q 不存在", ln.MemberExternalRef)
		}
		if !IsValidLoginNameKind(ln.Kind) {
			v.add(FileLoginNames, row, "登录名称 %q kind 非法（%q，仅支持 real_name/nickname）", ln.Value, ln.Kind)
		}
		norm := NormalizeLoginName(ln.Value)
		if norm == "" {
			v.add(FileLoginNames, row, "登录名称规范化后为空")
		} else {
			v.checkFieldLen(FileLoginNames, row, "登录名称", norm, MaxNameLen)
			loc := ln.MemberExternalRef + "/" + ln.Kind
			if prev, dup := seenNorm[norm]; dup {
				v.add(FileLoginNames, row, "登录名称 %q 与 %s 规范化后冲突（所有本名与昵称必须全局唯一）", norm, prev)
			}
			seenNorm[norm] = loc
		}
		if ln.Kind == LoginNameRealName {
			realNameCount[ln.MemberExternalRef]++
			realNameValue[ln.MemberExternalRef] = norm
		}
	}

	// 每位 can_login=true 成员必须恰好一条 real_name，且与 display_name 一致（契约 §2.4）。
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

func (v *validator) checkGiftTypes() {
	seen := make(map[string]int, len(v.ds.GiftTypes))
	for i, gt := range v.ds.GiftTypes {
		row := i + 1
		if !IsValidGiftTypeKey(gt.TypeKey) {
			v.add(FileGiftTypes, row, "type_key %q 非法（小写字母开头的字母/数字/下划线，最长 32）", gt.TypeKey)
		} else if prev, dup := seen[gt.TypeKey]; dup {
			v.add(FileGiftTypes, row, "type_key %q 与第 %d 行重复", gt.TypeKey, prev)
		}
		seen[gt.TypeKey] = row
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

	// 礼品类型完全由数据文件驱动（D-13 修订）：无内置必备清单，
	// 新增或细化类型只需改数据文件，不修改鉴权与校验逻辑。
}

func (v *validator) checkGiftItems() {
	groupOK := make(map[string]bool, len(v.ds.Groups))
	groupStatus := make(map[string]string, len(v.ds.Groups))
	for _, g := range v.ds.Groups {
		groupOK[g.ExternalRef] = true
		groupStatus[g.ExternalRef] = g.Status
	}
	typeOK := make(map[string]bool, len(v.ds.GiftTypes))
	for _, gt := range v.ds.GiftTypes {
		typeOK[gt.TypeKey] = true
	}

	seenRef := make(map[string]int, len(v.ds.GiftItems))
	seenCode := make(map[string]string)            // code_plaintext（激活码或卡号） -> external_ref
	seenOrdinal := make(map[string]map[int]string) // group -> ordinal -> external_ref
	activeGiftCount := make(map[string]int)

	for i, gi := range v.ds.GiftItems {
		row := i + 1
		if gi.ExternalRef == "" {
			v.add(FileGiftItems, row, "external_ref 不能为空")
		} else {
			v.checkFieldLen(FileGiftItems, row, "external_ref", gi.ExternalRef, MaxExternalRefLen)
			if prev, dup := seenRef[gi.ExternalRef]; dup {
				v.add(FileGiftItems, row, "external_ref %q 与第 %d 行重复", gi.ExternalRef, prev)
			}
			seenRef[gi.ExternalRef] = row
		}
		if !groupOK[gi.GroupExternalRef] {
			v.add(FileGiftItems, row, "礼品 %s 引用的同行组 %q 不存在", gi.ExternalRef, gi.GroupExternalRef)
		}
		if !typeOK[gi.TypeKey] {
			v.add(FileGiftItems, row, "礼品 %s 引用的礼品类型 %q 不存在", gi.ExternalRef, gi.TypeKey)
		}
		if !IsValidCredentialMode(gi.CredentialMode) {
			v.add(FileGiftItems, row, "礼品 %s credential_mode 非法（%q，仅支持 code/card_secret）", gi.ExternalRef, gi.CredentialMode)
		}
		// code_plaintext 在两种模式下分别为激活码/卡号，全局唯一约束一致；
		// 密码（secret_plaintext）不做唯一性要求。
		codeLabel := "激活码"
		if gi.CredentialMode == CredentialModeCardSecret {
			codeLabel = "卡号"
		}
		if strings.TrimSpace(gi.CodePlaintext) == "" {
			v.add(FileGiftItems, row, "礼品 %s %s不能为空", gi.ExternalRef, codeLabel)
		} else {
			v.checkFieldLen(FileGiftItems, row, codeLabel, gi.CodePlaintext, MaxGiftCodeLen)
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

	// 每个 active 同行组至少一份 active 礼品（契约 §2.6）。
	for _, g := range v.ds.Groups {
		if g.Status == StatusActive && activeGiftCount[g.ExternalRef] == 0 {
			v.add(FileGiftItems, 0, "active 同行组 %s 至少需要一份 active 礼品", g.ExternalRef)
		}
	}
}

func (v *validator) checkAccessKeys() {
	seenRef := make(map[string]int, len(v.ds.AccessKeys))
	seenCode := make(map[string]string) // code_plaintext -> external_ref

	for i, k := range v.ds.AccessKeys {
		row := i + 1
		if k.ExternalRef == "" {
			v.add(FileAccessKeys, row, "external_ref 不能为空")
		} else {
			v.checkFieldLen(FileAccessKeys, row, "external_ref", k.ExternalRef, MaxExternalRefLen)
			if prev, dup := seenRef[k.ExternalRef]; dup {
				v.add(FileAccessKeys, row, "external_ref %q 与第 %d 行重复", k.ExternalRef, prev)
			}
			seenRef[k.ExternalRef] = row
		}
		if !IsValidAccessKeyCode(k.CodePlaintext) {
			v.add(FileAccessKeys, row, "密钥 %s 格式非法：必须严格为 8 位 ASCII 数字", k.ExternalRef)
		} else if prevRef, dup := seenCode[k.CodePlaintext]; dup {
			v.add(FileAccessKeys, row, "密钥 %s 与密钥 %s 的数字重复", k.ExternalRef, prevRef)
		}
		seenCode[k.CodePlaintext] = k.ExternalRef
		if !IsValidStatus(k.Status) {
			v.add(FileAccessKeys, row, "密钥 %s status 非法（%q）", k.ExternalRef, k.Status)
		}
		// 契约 §2.7：导入不预绑定，该字段一般为空。
		if strings.TrimSpace(k.BindGroupExternalRef) != "" {
			v.add(FileAccessKeys, row, "密钥 %s 不允许预绑定同行组（bind_group_external_ref 必须为空）", k.ExternalRef)
		}
	}
}
