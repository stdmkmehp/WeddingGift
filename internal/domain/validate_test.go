package domain

import (
	"strings"
	"testing"
)

// validDataset 构造一份覆盖主要业务形态的合法数据包：
// 单身组、夫妻带娃组、多昵称成员、三种礼品类型、一组多份礼品、实例级覆盖、
// 两种凭证形态（单一激活码 + 卡号密码）、一对一密钥。
func validDataset() *Dataset {
	return &Dataset{
		Groups: []GroupImport{
			{ExternalRef: "G001", Salutation: "王芳", Status: StatusActive},
			{ExternalRef: "G002", Salutation: "张伟 & 李娜 一家", CustomMessage: "谢谢陪伴", Status: StatusActive},
		},
		Members: []MemberImport{
			{ExternalRef: "M001", GroupExternalRef: "G001", DisplayName: "王芳", CanLogin: true, SortOrder: 1},
			{ExternalRef: "M002", GroupExternalRef: "G002", DisplayName: "张伟", CanLogin: true, SortOrder: 1},
			{ExternalRef: "M003", GroupExternalRef: "G002", DisplayName: "李娜", CanLogin: true, SortOrder: 2},
			{ExternalRef: "M004", GroupExternalRef: "G002", DisplayName: "张小宝", CanLogin: false, SortOrder: 3},
		},
		LoginNames: []LoginNameImport{
			{MemberExternalRef: "M001", Value: "王芳", Kind: LoginNameRealName},
			{MemberExternalRef: "M001", Value: "芳芳", Kind: LoginNameNickname},
			{MemberExternalRef: "M001", Value: "小芳", Kind: LoginNameNickname},
			{MemberExternalRef: "M002", Value: "张伟", Kind: LoginNameRealName},
			{MemberExternalRef: "M002", Value: "阿伟", Kind: LoginNameNickname},
			{MemberExternalRef: "M003", Value: "李娜", Kind: LoginNameRealName},
		},
		GiftTypes: []GiftTypeImport{
			{TypeKey: "travel_csj_first", Title: "长三角旅游年卡", DefaultInstructions: "凭码兑换", DefaultRedemptionURL: "#小程序://示例旅游年卡/FAKETRAVEL000"},
			{TypeKey: "movie_voucher", Title: "电影券", DefaultInstructions: "凭码兑换"},
			{TypeKey: "coffee_voucher", Title: "咖啡券", DefaultInstructions: "凭码兑换"},
		},
		GiftItems: []GiftItemImport{
			{ExternalRef: "GI001", GroupExternalRef: "G001", TypeKey: "coffee_voucher", CredentialMode: CredentialModeCode, CodePlaintext: "CODE001", Ordinal: 1, Status: StatusActive},
			{ExternalRef: "GI002", GroupExternalRef: "G002", TypeKey: "travel_csj_first", CredentialMode: CredentialModeCode, CodePlaintext: "CODE002", Ordinal: 1, Status: StatusActive},
			{ExternalRef: "GI003", GroupExternalRef: "G002", TypeKey: "movie_voucher", CredentialMode: CredentialModeCardSecret, CodePlaintext: "CODE003", SecretPlaintext: "PW003", Ordinal: 2, Status: StatusActive,
				RedemptionURLOverride: "https://example.com/movie-special"},
		},
		AccessKeys: []AccessKeyImport{
			{ExternalRef: "K001", CodePlaintext: "12345678", Status: StatusActive},
			{ExternalRef: "K002", CodePlaintext: "87654321", Status: StatusActive},
		},
	}
}

func TestValidateValidDataset(t *testing.T) {
	if errs := Validate(validDataset()); len(errs) > 0 {
		t.Fatalf("合法数据包不应产生校验错误: %v", errs)
	}
}

// TestValidateViolations 逐项覆盖数据契约的校验规则（WG-303 数据校验测试域）。
func TestValidateViolations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(ds *Dataset)
		want   string // 错误信息应包含的子串
	}{
		{"本名重复", func(ds *Dataset) {
			ds.Members[1].DisplayName = "王芳"
		}, "本名"},
		{"昵称与他人本名冲突", func(ds *Dataset) {
			ds.LoginNames = append(ds.LoginNames, LoginNameImport{MemberExternalRef: "M002", Value: "王芳", Kind: LoginNameNickname})
		}, "冲突"},
		{"昵称与他人昵称冲突", func(ds *Dataset) {
			ds.LoginNames = append(ds.LoginNames, LoginNameImport{MemberExternalRef: "M002", Value: "芳芳", Kind: LoginNameNickname})
		}, "冲突"},
		{"昵称规范化后冲突", func(ds *Dataset) {
			ds.LoginNames = append(ds.LoginNames, LoginNameImport{MemberExternalRef: "M002", Value: " 芳芳 ", Kind: LoginNameNickname})
		}, "冲突"},
		{"可登录成员缺本名登录名称", func(ds *Dataset) {
			ds.LoginNames = ds.LoginNames[:5] // 移除李娜的 real_name
		}, "real_name"},
		{"本名登录名称与本名不一致", func(ds *Dataset) {
			ds.LoginNames[3].Value = "张四"
		}, "不一致"},
		{"可登录成员多条本名登录名称", func(ds *Dataset) {
			ds.LoginNames = append(ds.LoginNames, LoginNameImport{MemberExternalRef: "M002", Value: "张伟", Kind: LoginNameRealName})
		}, "恰好一条"},
		{"成员引用不存在的组", func(ds *Dataset) {
			ds.Members[0].GroupExternalRef = "G999"
		}, "不存在"},
		{"登录名称引用不存在的成员", func(ds *Dataset) {
			ds.LoginNames = append(ds.LoginNames, LoginNameImport{MemberExternalRef: "M999", Value: "某人", Kind: LoginNameNickname})
		}, "不存在"},
		{"active 组无可登录成员", func(ds *Dataset) {
			ds.Members[0].CanLogin = false
		}, "can_login=true"},
		{"密钥少于 8 位", func(ds *Dataset) {
			ds.AccessKeys[0].CodePlaintext = "1234567"
		}, "8 位"},
		{"密钥含字母", func(ds *Dataset) {
			ds.AccessKeys[0].CodePlaintext = "1234567a"
		}, "8 位"},
		{"密钥为全角数字", func(ds *Dataset) {
			ds.AccessKeys[0].CodePlaintext = "１２３４５６７８"
		}, "8 位"},
		{"密钥重复", func(ds *Dataset) {
			ds.AccessKeys[1].CodePlaintext = "12345678"
		}, "重复"},
		{"密钥预绑定", func(ds *Dataset) {
			ds.AccessKeys[0].BindGroupExternalRef = "G001"
		}, "预绑定"},
		{"active 组无礼品", func(ds *Dataset) {
			ds.GiftItems = ds.GiftItems[:1] // 只保留 G001 的礼品
		}, "礼品"},
		{"礼品引用未知类型", func(ds *Dataset) {
			ds.GiftItems[0].TypeKey = "spa_voucher"
		}, "不存在"},
		{"礼品激活码重复", func(ds *Dataset) {
			ds.GiftItems[1].CodePlaintext = "CODE001"
		}, "激活码"},
		{"卡号与他人激活码重复", func(ds *Dataset) {
			ds.GiftItems[2].CodePlaintext = "CODE001"
		}, "重复"},
		{"凭证形态非法", func(ds *Dataset) {
			ds.GiftItems[0].CredentialMode = "qrcode"
		}, "credential_mode"},
		{"code 模式多填密码", func(ds *Dataset) {
			ds.GiftItems[0].SecretPlaintext = "PW999"
		}, "必须为空"},
		{"card_secret 模式缺密码", func(ds *Dataset) {
			ds.GiftItems[2].SecretPlaintext = " "
		}, "密码"},
		{"同组 ordinal 重复", func(ds *Dataset) {
			ds.GiftItems[2].Ordinal = 1
		}, "ordinal"},
		{"类型默认链接非 HTTPS", func(ds *Dataset) {
			ds.GiftTypes[0].DefaultRedemptionURL = "http://example.com/x"
		}, "HTTPS"},
		{"实例覆盖链接非 HTTPS", func(ds *Dataset) {
			ds.GiftItems[2].RedemptionURLOverride = "javascript:alert(1)"
		}, "HTTPS"},
		{"小程序口令缺路径", func(ds *Dataset) {
			ds.GiftTypes[0].DefaultRedemptionURL = "#小程序://仅名称无路径"
		}, "小程序口令"},
		{"小程序口令名称为空", func(ds *Dataset) {
			ds.GiftItems[2].RedemptionURLOverride = "#小程序:///SomePath"
		}, "小程序口令"},
		{"组 status 非法", func(ds *Dataset) {
			ds.Groups[0].Status = "archived"
		}, "status"},
		{"组称呼为空", func(ds *Dataset) {
			ds.Groups[0].Salutation = "  "
		}, "salutation"},
		{"礼品引用不存在的组", func(ds *Dataset) {
			ds.GiftItems[0].GroupExternalRef = "G999"
		}, "不存在"},
		{"礼品激活码为空", func(ds *Dataset) {
			ds.GiftItems[0].CodePlaintext = " "
		}, "激活码"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ds := validDataset()
			c.mutate(ds)
			errs := Validate(ds)
			if len(errs) == 0 {
				t.Fatalf("应产生校验错误（%s）", c.want)
			}
			found := false
			for _, e := range errs {
				if strings.Contains(e.Message, c.want) {
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
