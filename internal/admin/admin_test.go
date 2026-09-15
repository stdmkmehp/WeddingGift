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
