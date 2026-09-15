package domain

import (
	"strings"
	"testing"
)

// appendBaseline 构造一份「库内已首导」的现状基线：单身组 G001（王芳，1 份
// 咖啡券 GI001 ordinal 1），供追加校验用例作为对照世界。
func appendBaseline() *ExistingState {
	return &ExistingState{
		GroupRefs:        map[string]string{"G001": StatusActive},
		MemberRefs:       map[string]bool{"M001": true},
		DisplayNameNorms: map[string]string{"王芳": "M001"},
		LoginNameNorms: map[string]string{
			"王芳": "M001/real_name",
			"芳芳": "M001/nickname",
		},
		TypeKeys:           map[string]bool{"coffee_voucher": true},
		GiftItemRefs:       map[string]bool{"GI001": true},
		OrdinalsByGroup:    map[string]map[int]string{"G001": {1: "GI001"}},
		GiftCodePlaintexts: map[string]string{"CODE001": "GI001"},
	}
}

// validAppendDataset 构造一份合法的追加数据包，覆盖全部开放形态：
// 新组 + 新成员 + 新登录名称、给既有组补成员、给既有成员加昵称、
// 新礼品类型、新礼品发新组、给既有组补发既有类型礼品。
func validAppendDataset() *Dataset {
	return &Dataset{
		Groups: []GroupImport{
			{ExternalRef: "G101", Salutation: "周斌 & 吴倩 一家", Status: StatusActive},
		},
		Members: []MemberImport{
			{ExternalRef: "M101", GroupExternalRef: "G101", DisplayName: "周斌", CanLogin: true, SortOrder: 1},
			{ExternalRef: "M102", GroupExternalRef: "G101", DisplayName: "吴倩", CanLogin: true, SortOrder: 2},
			{ExternalRef: "M103", GroupExternalRef: "G001", DisplayName: "王小果", CanLogin: false, SortOrder: 2},
		},
		LoginNames: []LoginNameImport{
			{MemberExternalRef: "M101", Value: "周斌", Kind: LoginNameRealName},
			{MemberExternalRef: "M101", Value: "斌斌", Kind: LoginNameNickname},
			{MemberExternalRef: "M102", Value: "吴倩", Kind: LoginNameRealName},
			{MemberExternalRef: "M001", Value: "小芳", Kind: LoginNameNickname},
		},
		GiftTypes: []GiftTypeImport{
			{TypeKey: "cake_card", Title: "蛋糕卡", DefaultInstructions: "凭码兑换"},
		},
		GiftItems: []GiftItemImport{
			{ExternalRef: "GI101", GroupExternalRef: "G101", TypeKey: "cake_card", CredentialMode: CredentialModeCode, CodePlaintext: "CODE101", Ordinal: 1, Status: StatusActive},
			{ExternalRef: "GI102", GroupExternalRef: "G001", TypeKey: "coffee_voucher", CredentialMode: CredentialModeCode, CodePlaintext: "CODE102", Ordinal: 2, Status: StatusActive},
		},
	}
}

func TestValidateAppendValid(t *testing.T) {
	if errs := ValidateAppend(validAppendDataset(), appendBaseline()); len(errs) > 0 {
		t.Fatalf("合法追加包不应产生校验错误: %v", errs)
	}
}

// TestValidateAppendViolations 逐项覆盖追加语义（数据契约 §2.8）的校验规则。
func TestValidateAppendViolations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(ds *Dataset)
		want   string
	}{
		{"同行组已存在", func(ds *Dataset) {
			ds.Groups = append(ds.Groups, GroupImport{ExternalRef: "G001", Salutation: "重复组", Status: StatusActive})
		}, "已存在于数据库"},
		{"成员已存在", func(ds *Dataset) {
			ds.Members = append(ds.Members, MemberImport{ExternalRef: "M001", GroupExternalRef: "G101", DisplayName: "某人", SortOrder: 9})
		}, "已存在于数据库"},
		{"新成员本名与库内重复", func(ds *Dataset) {
			ds.Members[2].DisplayName = "王芳"
		}, "与库内成员"},
		{"登录名称与库内冲突", func(ds *Dataset) {
			ds.LoginNames = append(ds.LoginNames, LoginNameImport{MemberExternalRef: "M101", Value: "芳芳", Kind: LoginNameNickname})
		}, "与库内"},
		{"既有成员追加本名", func(ds *Dataset) {
			ds.LoginNames = append(ds.LoginNames, LoginNameImport{MemberExternalRef: "M001", Value: "王某某", Kind: LoginNameRealName})
		}, "仅允许 nickname"},
		{"礼品类型已存在", func(ds *Dataset) {
			ds.GiftTypes = append(ds.GiftTypes, GiftTypeImport{TypeKey: "coffee_voucher", Title: "重复类型", DefaultInstructions: "x"})
		}, "已存在于数据库"},
		{"礼品已存在", func(ds *Dataset) {
			ds.GiftItems = append(ds.GiftItems, GiftItemImport{ExternalRef: "GI001", GroupExternalRef: "G101", TypeKey: "cake_card", CredentialMode: CredentialModeCode, CodePlaintext: "CODE201", Ordinal: 2, Status: StatusActive})
		}, "已存在于数据库"},
		{"凭证与库内重复", func(ds *Dataset) {
			ds.GiftItems[1].CodePlaintext = "CODE001"
		}, "与库内既有礼品"},
		{"ordinal 与库内冲突", func(ds *Dataset) {
			ds.GiftItems[1].Ordinal = 1
		}, "ordinal"},
		{"成员引用不存在的组", func(ds *Dataset) {
			ds.Members[2].GroupExternalRef = "G999"
		}, "不存在"},
		{"礼品引用不存在的组", func(ds *Dataset) {
			ds.GiftItems[1].GroupExternalRef = "G999"
		}, "不存在"},
		{"礼品引用不存在的类型", func(ds *Dataset) {
			ds.GiftItems[1].TypeKey = "no_such_type"
		}, "不存在"},
		{"登录名称引用不存在的成员", func(ds *Dataset) {
			ds.LoginNames = append(ds.LoginNames, LoginNameImport{MemberExternalRef: "M999", Value: "某人", Kind: LoginNameNickname})
		}, "不存在"},
		{"新 active 组缺可登录成员", func(ds *Dataset) {
			ds.Groups = append(ds.Groups, GroupImport{ExternalRef: "G200", Salutation: "空组", Status: StatusActive})
			ds.GiftItems = append(ds.GiftItems, GiftItemImport{ExternalRef: "GI201", GroupExternalRef: "G200", TypeKey: "cake_card", CredentialMode: CredentialModeCode, CodePlaintext: "CODE201", Ordinal: 1, Status: StatusActive})
		}, "can_login"},
		{"新 active 组缺 active 礼品", func(ds *Dataset) {
			ds.Groups = append(ds.Groups, GroupImport{ExternalRef: "G201", Salutation: "有成员没礼品", Status: StatusActive})
			ds.Members = append(ds.Members, MemberImport{ExternalRef: "M201", GroupExternalRef: "G201", DisplayName: "测试一", CanLogin: true, SortOrder: 1})
			ds.LoginNames = append(ds.LoginNames, LoginNameImport{MemberExternalRef: "M201", Value: "测试一", Kind: LoginNameRealName})
		}, "至少需要一份 active 礼品"},
		{"新成员可登录缺本名登录名称", func(ds *Dataset) {
			ds.LoginNames = nil // 两名可登录新成员均缺 real_name
		}, "real_name"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ds := validAppendDataset()
			c.mutate(ds)
			errs := ValidateAppend(ds, appendBaseline())
			if len(errs) == 0 {
				t.Fatalf("应产生校验错误（%s）", c.want)
			}
			found := false
			for _, e := range errs {
				if strings.Contains(e.Error(), c.want) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("错误中未找到含 %q 的条目: %v", c.want, errs)
			}
		})
	}
}
