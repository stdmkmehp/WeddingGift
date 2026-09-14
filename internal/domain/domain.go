// Package domain 定义 WeddingGift 的领域实体、不变量与登录名称规范化。
//
// 本包不依赖 HTTP 或 SQLite（技术架构 §5 目录规则）。字段与业务规则以
// docs/04-m0-decisions.md 冻结的数据契约为事实源。
package domain

import (
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

// 实体状态枚举（数据契约 §2 各表 status 字段）。
const (
	StatusActive   = "active"
	StatusDisabled = "disabled"
)

// IsValidStatus 报告状态值是否合法。
func IsValidStatus(s string) bool {
	return s == StatusActive || s == StatusDisabled
}

// 登录名称类型（member_login_name.kind）。
const (
	LoginNameRealName = "real_name"
	LoginNameNickname = "nickname"
)

// IsValidLoginNameKind 报告登录名称类型是否合法。
func IsValidLoginNameKind(s string) bool {
	return s == LoginNameRealName || s == LoginNameNickname
}

// MiniProgramLinkPrefix 是微信小程序口令兑换入口的前缀（D-13 修订）：
// 形如「#小程序://中影电影通/XMjgAr06fK3fSzj」，由微信"复制链接"产生，
// 不是可点击 URL，网页端只能复制后引导用户粘贴到微信聊天打开。
const MiniProgramLinkPrefix = "#小程序://"

// accessKeyPattern 是卡片密钥的严格格式：8 位 ASCII 数字（D-05）。
// 不忽略空格、连字符、字母或全角数字。
var accessKeyPattern = regexp.MustCompile(`^[0-9]{8}$`)

// IsValidAccessKeyCode 报告密钥原文是否严格匹配 ^[0-9]{8}$。
func IsValidAccessKeyCode(raw string) bool {
	return accessKeyPattern.MatchString(raw)
}

// giftTypeKeyPattern 约束礼品类型编号形态，如 travel_csj_first。
var giftTypeKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// IsValidGiftTypeKey 报告礼品类型编号是否合法。
func IsValidGiftTypeKey(s string) bool {
	return giftTypeKeyPattern.MatchString(s)
}

// NormalizeLoginName 是导入与登录共用的登录名称规范化函数（契约 §2.1）：
// 去除首尾空白后做 Unicode NFC；不删除姓名内部字符，不做拼音、繁简、
// 错别字或近似匹配。函数行为冻结，任何变更都会同时影响导入与登录两侧。
func NormalizeLoginName(s string) string {
	return norm.NFC.String(strings.TrimSpace(s))
}

// 字段长度上限（按 rune 计）。取值宽松，仅用于拒绝明显异常的数据文件，
// 不构成业务规则；收紧这些值属于数据契约变更。
const (
	MaxExternalRefLen   = 64
	MaxNameLen          = 64
	MaxSalutationLen    = 128
	MaxCustomMessageLen = 512
	MaxTitleLen         = 64
	MaxSubtitleLen      = 128
	MaxInstructionsLen  = 1024
	MaxValidityTextLen  = 128
	MaxURLLen           = 512
	MaxGiftCodeLen      = 128
	MaxThemeVariantLen  = 32
)

// 以下结构体对应导入文件中的六类实体（契约 §2.2～§2.7）。
// 导入文件承载明文与业务外部编号；落库时密钥转 HMAC 摘要、礼品码转 AEAD 密文。

// GroupImport 是一行同行组导入记录。
type GroupImport struct {
	ExternalRef   string
	Salutation    string
	CustomMessage string
	Status        string
}

// MemberImport 是一行成员导入记录。
type MemberImport struct {
	ExternalRef      string
	GroupExternalRef string
	DisplayName      string
	CanLogin         bool
	SortOrder        int
}

// LoginNameImport 是一行登录名称导入记录（本名或昵称）。
type LoginNameImport struct {
	MemberExternalRef string
	Value             string
	Kind              string
}

// GiftTypeImport 是一行礼品类型导入记录。
type GiftTypeImport struct {
	TypeKey              string
	Title                string
	Subtitle             string
	DefaultInstructions  string
	DefaultValidityText  string
	DefaultRedemptionURL string
	ThemeVariant         string
}

// GiftItemImport 是一行礼品实例导入记录；CodePlaintext 只在导入流程中存在。
type GiftItemImport struct {
	ExternalRef           string
	GroupExternalRef      string
	TypeKey               string
	CodePlaintext         string
	InstructionsOverride  string
	ValidityTextOverride  string
	RedemptionURLOverride string
	Ordinal               int
	Status                string
}

// AccessKeyImport 是一行卡片密钥导入记录；CodePlaintext 只在导入流程中存在。
type AccessKeyImport struct {
	ExternalRef          string
	CodePlaintext        string
	Status               string
	BindGroupExternalRef string // 契约预留字段，导入时必须为空（不预绑定）
}

// Dataset 是一份完整数据包，包含六类实体的全部导入记录。
type Dataset struct {
	Groups     []GroupImport
	Members    []MemberImport
	LoginNames []LoginNameImport
	GiftTypes  []GiftTypeImport
	GiftItems  []GiftItemImport
	AccessKeys []AccessKeyImport
}
