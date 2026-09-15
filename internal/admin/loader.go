// Package admin 实现项目所有者的本地管理能力：数据文件装载、校验报告、
// 脱敏状态输出。导入、密钥停用与备份由 cmd 层编排 store/cryptox 完成。
package admin

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/weddinggift/wedding/internal/domain"
)

// utf8BOM 用于剥离 Excel 等工具导出的 UTF-8 BOM。
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// csvFileReader 按契约列读取单个 CSV；首次导入与追加导入的区别仅在文件
// 缺失与零数据行的处理（readCSV 严格 / readAppendCSV 宽松）。
type csvFileReader func(path string, columns []string) ([]csvRow, []error)

// LoadDataset 从 dir 读取六个契约 CSV 文件并解析为 domain.Dataset。
// 返回数据集与全部解析错误（文件缺失、表头不符、行解析失败）；
// 只要存在错误，调用方就必须中止后续校验与导入。
func LoadDataset(dir string) (*domain.Dataset, []error) {
	ds := &domain.Dataset{}
	errs := loadEntityFiles(ds, dir, readCSV)

	rows, ferrs := readCSV(filepath.Join(dir, domain.FileAccessKeys),
		[]string{"external_ref", "code_plaintext", "status", "bind_group_external_ref"})
	if len(ferrs) > 0 {
		errs = append(errs, ferrs...)
	} else {
		for _, r := range rows {
			ds.AccessKeys = append(ds.AccessKeys, domain.AccessKeyImport{
				ExternalRef:          r.fields["external_ref"],
				CodePlaintext:        r.fields["code_plaintext"],
				Status:               r.fields["status"],
				BindGroupExternalRef: r.fields["bind_group_external_ref"],
			})
		}
	}
	return ds, errs
}

// LoadAppendDataset 从 dir 读取追加数据包（五类实体的契约 CSV，数据契约 §2.8）。
// 与 LoadDataset 的差异：
//   - 五个实体文件均允许缺失（视为该实体零新增）或仅表头零数据行；
//   - access_keys.csv 不参与追加：缺失或仅表头均可，含数据行即报错，
//     防止「以为追加了密钥但没生效」。
//
// 表头契约与行解析规则与首次导入完全一致。
func LoadAppendDataset(dir string) (*domain.Dataset, []error) {
	ds := &domain.Dataset{}
	errs := loadEntityFiles(ds, dir, readAppendCSV)

	keyRows, ferrs := readAppendCSV(filepath.Join(dir, domain.FileAccessKeys),
		[]string{"external_ref", "code_plaintext", "status", "bind_group_external_ref"})
	if len(ferrs) > 0 {
		errs = append(errs, ferrs...)
	} else if len(keyRows) > 0 {
		errs = append(errs, fmt.Errorf("%s: 追加模式不支持新增密钥（含 %d 行数据；请移除该文件或仅保留表头）",
			domain.FileAccessKeys, len(keyRows)))
	}
	return ds, errs
}

// loadEntityFiles 按契约列装载五类共有实体（同行组/成员/登录名称/礼品类型/
// 礼品实例），首次导入与追加导入共用。返回全部解析错误；只要存在错误，
// 调用方就必须中止后续校验与导入。
func loadEntityFiles(ds *domain.Dataset, dir string, read csvFileReader) []error {
	var errs []error

	load := func(name string, columns []string, fill func(map[string]string, int)) {
		rows, ferrs := read(filepath.Join(dir, name), columns)
		if len(ferrs) > 0 {
			errs = append(errs, ferrs...)
			return
		}
		for _, r := range rows {
			fill(r.fields, r.line)
		}
	}

	load(domain.FileGroups,
		[]string{"external_ref", "salutation", "custom_message", "status"},
		func(f map[string]string, _ int) {
			ds.Groups = append(ds.Groups, domain.GroupImport{
				ExternalRef:   f["external_ref"],
				Salutation:    f["salutation"],
				CustomMessage: f["custom_message"],
				Status:        f["status"],
			})
		})

	load(domain.FileMembers,
		[]string{"external_ref", "group_external_ref", "display_name", "can_login", "sort_order"},
		func(f map[string]string, line int) {
			canLogin, err := parseBool(f["can_login"])
			if err != nil {
				errs = append(errs, fmt.Errorf("%s 第 %d 行: can_login %q 非法（仅支持 true/false/1/0）", domain.FileMembers, line, f["can_login"]))
			}
			sortOrder, err := parseInt(f["sort_order"])
			if err != nil {
				errs = append(errs, fmt.Errorf("%s 第 %d 行: sort_order %q 不是整数", domain.FileMembers, line, f["sort_order"]))
			}
			ds.Members = append(ds.Members, domain.MemberImport{
				ExternalRef:      f["external_ref"],
				GroupExternalRef: f["group_external_ref"],
				DisplayName:      f["display_name"],
				CanLogin:         canLogin,
				SortOrder:        sortOrder,
			})
		})

	load(domain.FileLoginNames,
		[]string{"member_external_ref", "value", "kind"},
		func(f map[string]string, _ int) {
			ds.LoginNames = append(ds.LoginNames, domain.LoginNameImport{
				MemberExternalRef: f["member_external_ref"],
				Value:             f["value"],
				Kind:              f["kind"],
			})
		})

	load(domain.FileGiftTypes,
		[]string{"type_key", "title", "subtitle", "default_instructions", "default_validity_text", "default_redemption_url", "theme_variant"},
		func(f map[string]string, _ int) {
			ds.GiftTypes = append(ds.GiftTypes, domain.GiftTypeImport{
				TypeKey:              f["type_key"],
				Title:                f["title"],
				Subtitle:             f["subtitle"],
				DefaultInstructions:  f["default_instructions"],
				DefaultValidityText:  f["default_validity_text"],
				DefaultRedemptionURL: f["default_redemption_url"],
				ThemeVariant:         f["theme_variant"],
			})
		})

	load(domain.FileGiftItems,
		[]string{"external_ref", "group_external_ref", "type_key", "credential_mode", "code_plaintext", "secret_plaintext", "instructions_override", "validity_text_override", "redemption_url_override", "ordinal", "status"},
		func(f map[string]string, line int) {
			ordinal, err := parseInt(f["ordinal"])
			if err != nil {
				errs = append(errs, fmt.Errorf("%s 第 %d 行: ordinal %q 不是整数", domain.FileGiftItems, line, f["ordinal"]))
			}
			ds.GiftItems = append(ds.GiftItems, domain.GiftItemImport{
				ExternalRef:           f["external_ref"],
				GroupExternalRef:      f["group_external_ref"],
				TypeKey:               f["type_key"],
				CredentialMode:        f["credential_mode"],
				CodePlaintext:         f["code_plaintext"],
				SecretPlaintext:       f["secret_plaintext"],
				InstructionsOverride:  f["instructions_override"],
				ValidityTextOverride:  f["validity_text_override"],
				RedemptionURLOverride: f["redemption_url_override"],
				Ordinal:               ordinal,
				Status:                f["status"],
			})
		})

	return errs
}

// csvRow 是一行已按表头映射的 CSV 记录；line 是源文件行号（含表头行，从 1 起）。
type csvRow struct {
	fields map[string]string
	line   int
}

// readCSV 读取一个契约 CSV（首次导入语义）：文件必须存在且至少一行数据。
func readCSV(path string, required []string) ([]csvRow, []error) {
	name := filepath.Base(path)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, []error{fmt.Errorf("读取 %s 失败: %w", name, err)}
	}
	rows, errs := parseCSVRows(name, raw, required)
	if len(errs) > 0 {
		return nil, errs
	}
	if len(rows) == 0 {
		return nil, []error{fmt.Errorf("%s: 没有数据行", name)}
	}
	return rows, nil
}

// readAppendCSV 读取一个契约 CSV（追加语义）：文件缺失视为零数据行；
// 在场文件允许仅表头零数据行。
func readAppendCSV(path string, required []string) ([]csvRow, []error) {
	name := filepath.Base(path)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, []error{fmt.Errorf("读取 %s 失败: %w", name, err)}
	}
	return parseCSVRows(name, raw, required)
}

// parseCSVRows 校验表头与 required 完全一致（不多不少、顺序无关），
// 然后逐行映射为 csvRow。密钥与激活码等字段一律按原文本读取，不做任何数值化。
func parseCSVRows(name string, raw []byte, required []string) ([]csvRow, []error) {
	raw = bytes.TrimPrefix(raw, utf8BOM)

	r := csv.NewReader(bytes.NewReader(raw))
	r.FieldsPerRecord = -1
	r.TrimLeadingSpace = false

	header, err := r.Read()
	if err != nil {
		return nil, []error{fmt.Errorf("%s: 读取表头失败: %w", name, err)}
	}
	if err := checkHeader(name, header, required); err != nil {
		return nil, []error{err}
	}
	index := make(map[string]int, len(header))
	for i, col := range header {
		index[strings.TrimSpace(col)] = i
	}

	var rows []csvRow
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		line, _ := r.FieldPos(0)
		if err != nil {
			return nil, []error{fmt.Errorf("%s 第 %d 行: 解析失败: %w", name, line, err)}
		}
		if len(rec) != len(header) {
			return nil, []error{fmt.Errorf("%s 第 %d 行: 字段数 %d 与表头 %d 不一致", name, line, len(rec), len(header))}
		}
		fields := make(map[string]string, len(header))
		for col, i := range index {
			fields[col] = rec[i]
		}
		rows = append(rows, csvRow{fields: fields, line: line})
	}
	return rows, nil
}

// checkHeader 校验实际表头与契约列集合完全一致。
func checkHeader(name string, header, required []string) error {
	actual := make(map[string]bool, len(header))
	for _, col := range header {
		c := strings.TrimSpace(col)
		if actual[c] {
			return fmt.Errorf("%s: 表头列 %q 重复", name, c)
		}
		actual[c] = true
	}
	want := make(map[string]bool, len(required))
	for _, c := range required {
		want[c] = true
		if !actual[c] {
			return fmt.Errorf("%s: 缺少必需列 %q", name, c)
		}
	}
	for c := range actual {
		if !want[c] {
			return fmt.Errorf("%s: 存在契约外列 %q", name, c)
		}
	}
	return nil
}

// parseBool 解析契约布尔字段（true/false/1/0）。
func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "1":
		return true, nil
	case "false", "0":
		return false, nil
	}
	return false, fmt.Errorf("非法布尔值 %q", s)
}

// parseInt 解析整数字段（按文本处理，拒绝空白与非数字内容）。
func parseInt(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}
