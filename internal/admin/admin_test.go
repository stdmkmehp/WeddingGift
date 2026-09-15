package admin_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/weddinggift/wedding/internal/admin"
	"github.com/weddinggift/wedding/internal/domain"
)

const fixturesDir = "../../testdata/fixtures"

func TestLoadFixturesAndValidate(t *testing.T) {
	ds, errs := admin.LoadDataset(fixturesDir)
	if len(errs) > 0 {
		t.Fatalf("fixtures 装载失败: %v", errs)
	}
	if valErrs := domain.Validate(ds); len(valErrs) > 0 {
		t.Fatalf("fixtures 校验失败: %v", valErrs)
	}

	// 装载数量与虚构数据包 README 一致。
	if got := len(ds.Groups); got != 3 {
		t.Errorf("同行组数 = %d, want 3", got)
	}
	if got := len(ds.Members); got != 6 {
		t.Errorf("成员数 = %d, want 6", got)
	}
	if got := len(ds.LoginNames); got != 9 {
		t.Errorf("登录名称数 = %d, want 9", got)
	}
	if got := len(ds.GiftTypes); got != 3 {
		t.Errorf("礼品类型数 = %d, want 3", got)
	}
	if got := len(ds.GiftItems); got != 6 {
		t.Errorf("礼品数 = %d, want 6", got)
	}
	if got := len(ds.AccessKeys); got != 3 {
		t.Errorf("密钥数 = %d, want 3", got)
	}
}

func TestDatasetReport(t *testing.T) {
	ds, _ := admin.LoadDataset(fixturesDir)
	report := admin.DatasetReport(ds)

	for _, want := range []string{
		"同行组: 3", "成员: 6", "登录名称: 9", "礼品实例: 6", "卡片密钥: 3",
		"travel_csj_first=2", "movie_voucher=2", "coffee_voucher=2",
		"G002=3",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("核对报告缺少 %q:\n%s", want, report)
		}
	}
	// 报告不得包含任何明文密钥或礼品凭证（激活码/卡号/密码）。
	for _, gi := range ds.GiftItems {
		if strings.Contains(report, gi.CodePlaintext) {
			t.Errorf("核对报告泄漏礼品凭证 %q", gi.CodePlaintext)
		}
		if gi.SecretPlaintext != "" && strings.Contains(report, gi.SecretPlaintext) {
			t.Errorf("核对报告泄漏礼品密码 %q", gi.SecretPlaintext)
		}
	}
	for _, k := range ds.AccessKeys {
		if strings.Contains(report, k.CodePlaintext) {
			t.Errorf("核对报告泄漏密钥 %q", k.CodePlaintext)
		}
	}
}

func TestLoadDatasetErrors(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(t *testing.T, dir string)
		wantErr string
	}{
		{"缺文件", func(t *testing.T, dir string) {
			if err := os.Remove(filepath.Join(dir, domain.FileAccessKeys)); err != nil {
				t.Fatal(err)
			}
		}, "access_keys.csv"},
		{"表头缺列", func(t *testing.T, dir string) {
			writeCSV(t, filepath.Join(dir, domain.FileGroups), "external_ref,salutation,custom_message\nG001,王芳,\n")
		}, "status"},
		{"表头多列", func(t *testing.T, dir string) {
			writeCSV(t, filepath.Join(dir, domain.FileGroups), "external_ref,salutation,custom_message,status,extra\nG001,王芳,,active,x\n")
		}, "extra"},
		{"布尔非法", func(t *testing.T, dir string) {
			writeCSV(t, filepath.Join(dir, domain.FileMembers), "external_ref,group_external_ref,display_name,can_login,sort_order\nM001,G001,王芳,yes,1\n")
		}, "can_login"},
		{"整数非法", func(t *testing.T, dir string) {
			writeCSV(t, filepath.Join(dir, domain.FileGiftItems), "external_ref,group_external_ref,type_key,credential_mode,code_plaintext,secret_plaintext,instructions_override,validity_text_override,redemption_url_override,ordinal,status\nGI001,G001,coffee_voucher,code,X,,,,,一,active\n")
		}, "ordinal"},
		{"空数据文件", func(t *testing.T, dir string) {
			writeCSV(t, filepath.Join(dir, domain.FileGroups), "external_ref,salutation,custom_message,status\n")
		}, "没有数据行"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := copyFixtures(t)
			c.mutate(t, dir)
			_, errs := admin.LoadDataset(dir)
			if len(errs) == 0 {
				t.Fatalf("应产生解析错误（%s）", c.wantErr)
			}
			found := false
			for _, e := range errs {
				if strings.Contains(e.Error(), c.wantErr) {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("错误中未找到含 %q 的条目: %v", c.wantErr, errs)
			}
		})
	}
}

const appendFixturesDir = "../../testdata/fixtures-append"

func TestLoadAppendFixtures(t *testing.T) {
	ds, errs := admin.LoadAppendDataset(appendFixturesDir)
	if len(errs) > 0 {
		t.Fatalf("追加 fixtures 装载失败: %v", errs)
	}

	// 装载数量与追加虚构数据包 README 一致。
	if got := len(ds.Groups); got != 1 {
		t.Errorf("同行组数 = %d, want 1", got)
	}
	if got := len(ds.Members); got != 3 {
		t.Errorf("成员数 = %d, want 3", got)
	}
	if got := len(ds.LoginNames); got != 4 {
		t.Errorf("登录名称数 = %d, want 4", got)
	}
	if got := len(ds.GiftTypes); got != 1 {
		t.Errorf("礼品类型数 = %d, want 1", got)
	}
	if got := len(ds.GiftItems); got != 2 {
		t.Errorf("礼品数 = %d, want 2", got)
	}
	if got := len(ds.AccessKeys); got != 0 {
		t.Errorf("密钥数 = %d, want 0（追加不装载密钥）", got)
	}
}

// TestLoadAppendMissingAndEmptyFiles 覆盖追加语义的文件在场规则：
// 缺失文件视为零新增；在场文件允许仅表头零数据行。
func TestLoadAppendMissingAndEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	// 只提供一个实体文件；另放一个仅表头的文件。
	writeCSV(t, filepath.Join(dir, domain.FileGiftTypes),
		"type_key,title,subtitle,default_instructions,default_validity_text,default_redemption_url,theme_variant\ncake_card,蛋糕卡,一份甜蜜心意,凭码兑换,60 天,https://example.com/redeem/cake,\n")
	writeCSV(t, filepath.Join(dir, domain.FileGroups),
		"external_ref,salutation,custom_message,status\n")

	ds, errs := admin.LoadAppendDataset(dir)
	if len(errs) > 0 {
		t.Fatalf("缺失/仅表头文件不应产生解析错误: %v", errs)
	}
	if len(ds.GiftTypes) != 1 || len(ds.Groups) != 0 || len(ds.Members) != 0 || len(ds.LoginNames) != 0 || len(ds.GiftItems) != 0 {
		t.Errorf("装载结果不符合预期: %+v", ds)
	}
}

// TestLoadAppendAccessKeys 覆盖追加模式的密钥文件规则：
// 仅表头可容忍；含数据行即拒绝（防误当成新增密钥）。
func TestLoadAppendAccessKeys(t *testing.T) {
	header := "external_ref,code_plaintext,status,bind_group_external_ref\n"

	dir := t.TempDir()
	writeCSV(t, filepath.Join(dir, domain.FileAccessKeys), header)
	if _, errs := admin.LoadAppendDataset(dir); len(errs) > 0 {
		t.Fatalf("仅表头的密钥文件不应报错: %v", errs)
	}

	writeCSV(t, filepath.Join(dir, domain.FileAccessKeys), header+"K901,20260901,active,\n")
	errs := mustLoadAppendErr(t, dir)
	assertErrContains(t, errs, "追加模式不支持新增密钥")

	// 表头不符同样报错（与首次导入契约一致）。
	writeCSV(t, filepath.Join(dir, domain.FileAccessKeys), "external_ref,code_plaintext\n")
	mustLoadAppendErr(t, dir)
}

// TestLoadAppendBadHeader 在场文件表头不符时报错（追加不放宽表头契约）。
func TestLoadAppendBadHeader(t *testing.T) {
	dir := t.TempDir()
	writeCSV(t, filepath.Join(dir, domain.FileGroups), "external_ref,salutation\nG101,某家\n")
	errs := mustLoadAppendErr(t, dir)
	assertErrContains(t, errs, "缺少必需列")
}

func mustLoadAppendErr(t *testing.T, dir string) []error {
	t.Helper()
	_, errs := admin.LoadAppendDataset(dir)
	if len(errs) == 0 {
		t.Fatal("应产生解析错误")
	}
	return errs
}

func assertErrContains(t *testing.T, errs []error, want string) {
	t.Helper()
	for _, e := range errs {
		if strings.Contains(e.Error(), want) {
			return
		}
	}
	t.Fatalf("错误中未找到含 %q 的条目: %v", want, errs)
}

func TestAppendReport(t *testing.T) {
	ds, errs := admin.LoadAppendDataset(appendFixturesDir)
	if len(errs) > 0 {
		t.Fatalf("追加 fixtures 装载失败: %v", errs)
	}
	// 模拟库内已首导主 fixtures 的现状（仅需报告消费的三个集合）。
	ex := &domain.ExistingState{
		GroupRefs:  map[string]string{"G001": "active", "G002": "active"},
		MemberRefs: map[string]bool{"M006": true},
		TypeKeys:   map[string]bool{"movie_voucher": true},
	}
	report := admin.AppendReport(ds, ex)

	for _, want := range []string{
		"同行组（新增）: 1", "成员（新增）: 3", "登录名称（新增）: 4", "礼品类型（新增）: 1", "礼品实例（新增）: 2",
		"cake_card=1", "movie_voucher=1", "G101=1", "G002=1",
		"引用既有同行组: G001, G002",
		"引用既有成员: M006",
		"引用既有礼品类型: movie_voucher",
	} {
		if !strings.Contains(report, want) {
			t.Errorf("追加核对报告缺少 %q:\n%s", want, report)
		}
	}
	// 报告不得包含明文礼品凭证、姓名或昵称。
	for _, gi := range ds.GiftItems {
		if strings.Contains(report, gi.CodePlaintext) {
			t.Errorf("追加核对报告泄漏礼品凭证 %q", gi.CodePlaintext)
		}
	}
	for _, m := range ds.Members {
		if strings.Contains(report, m.DisplayName) {
			t.Errorf("追加核对报告泄漏成员姓名 %q", m.DisplayName)
		}
	}
	for _, ln := range ds.LoginNames {
		if strings.Contains(report, ln.Value) {
			t.Errorf("追加核对报告泄漏登录名称 %q", ln.Value)
		}
	}
}

// copyFixtures 把虚构数据包复制到临时目录，供破坏性用例修改。
func copyFixtures(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	entries, err := os.ReadDir(fixturesDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".csv") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(fixturesDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func writeCSV(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
