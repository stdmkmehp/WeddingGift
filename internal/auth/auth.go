// Package auth 实现 M4 的鉴权与会话服务（WG-401～WG-404）：
// 登录名称解析、8 位密钥校验、一对一绑定事务编排、服务端会话的创建/解析/退出。
//
// 对外错误只有两类（产品架构 §7.1/§12）：
//   - ErrInvalidCredentials：名称/密钥/绑定状态任一环不通过的统一模糊错误；
//   - ErrInvalidInput：公开格式规则不满足（空名称、超长名称、密钥非 8 位数字），
//     格式规则本身是公开的，不构成枚举泄漏。
//
// 日志只允许出现内部编号，禁止姓名、昵称、明文密钥。
package auth

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/weddinggift/wedding/internal/cryptox"
	"github.com/weddinggift/wedding/internal/domain"
	"github.com/weddinggift/wedding/internal/store"
)

var (
	// ErrInvalidCredentials 是统一鉴权失败：调用方不得向外区分具体原因。
	ErrInvalidCredentials = errors.New("名字或卡片密钥不正确")
	// ErrInvalidInput 是输入格式不满足公开规则（前端应已就地拦截，此处为兜底）。
	ErrInvalidInput = errors.New("输入格式不正确")
	// ErrSessionInvalid 表示会话不存在、已过期、密钥被停用或组被停用。
	ErrSessionInvalid = errors.New("会话无效或已过期")
)

// SessionTTL 是会话固定有效期（技术架构 §9.4 允许把"7 天闲置 + 30 天绝对"
// 收敛为一个固定有效期；取 30 天覆盖完整婚礼窗口，无滑动续期）。
const SessionTTL = 30 * 24 * time.Hour

// Service 编排登录、绑定与会话。依赖 store（持久化）与 cryptox（摘要/令牌）。
type Service struct {
	db *store.DB
	cx *cryptox.Service
	// now 便于测试注入固定时间；生产为 time.Now。
	now func() time.Time
}

// New 构造鉴权服务。
func New(db *store.DB, cx *cryptox.Service) *Service {
	return &Service{db: db, cx: cx, now: time.Now}
}

// NewWithClock 是测试用构造：注入固定/可控时钟。
func NewWithClock(db *store.DB, cx *cryptox.Service, now func() time.Time) *Service {
	s := New(db, cx)
	s.now = now
	return s
}

// LoginResult 是一次成功登录的完整结果。
type LoginResult struct {
	// Token 是明文会话令牌（仅此一次交给调用方写 Cookie；服务端只存摘要）。
	Token     []byte
	ExpiresAt time.Time
	// FirstBind 表示本次是否完成了密钥与同行组的首次绑定（日志用）。
	FirstBind    bool
	AccessKeyRef string
	Group        GroupPayload
}

// GroupPayload 是登录/会话解析后向页面返回的同行组数据（不含礼品码）。
type GroupPayload struct {
	// GroupID 是内部 ID，仅供服务端做礼品归属校验（M5 reveal），不下发前端。
	GroupID       string
	Salutation    string
	CustomMessage string
	ExpiresAt     time.Time
	Gifts         []store.GiftSummary
}

// Login 执行完整登录流程：格式校验 → 摘要 → BEGIN IMMEDIATE 绑定事务 → 会话。
// keyCode 必须是原始 8 位数字字符串；本函数不修改其内容、不做任何容错裁剪。
func (s *Service) Login(ctx context.Context, loginName, keyCode string) (*LoginResult, error) {
	if !domain.IsValidAccessKeyCode(keyCode) {
		return nil, ErrInvalidInput
	}
	norm := domain.NormalizeLoginName(loginName)
	if norm == "" || utf8.RuneCountInString(norm) > domain.MaxNameLen {
		return nil, ErrInvalidInput
	}

	token, err := cryptox.NewSessionToken()
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	expiresAt := now.Add(SessionTTL)

	outcome, ok, err := s.db.LoginWithCredentials(ctx, norm,
		s.cx.AccessKeyDigest(keyCode), cryptox.SessionTokenDigest(token),
		expiresAt.Format(time.RFC3339), now.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrInvalidCredentials
	}

	group, err := s.groupPayload(ctx, outcome.GroupID, outcome.GroupSalutation, outcome.GroupCustomMessage, expiresAt)
	if err != nil {
		return nil, err
	}
	return &LoginResult{
		Token:        token,
		ExpiresAt:    expiresAt,
		FirstBind:    outcome.FirstBind,
		AccessKeyRef: outcome.AccessKeyRef,
		Group:        group,
	}, nil
}

// ResolveSession 按明文令牌解析有效会话（bootstrap 与受保护接口使用）。
// 任何一环失效都返回 ErrSessionInvalid；过期会话由 store 层顺手删除。
func (s *Service) ResolveSession(ctx context.Context, token []byte) (*GroupPayload, error) {
	if len(token) != 32 {
		return nil, ErrSessionInvalid
	}
	now := s.now().UTC()
	info, err := s.db.FindSessionByTokenDigest(ctx, cryptox.SessionTokenDigest(token), now.Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, ErrSessionInvalid
	}
	expiresAt, err := time.Parse(time.RFC3339, info.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("会话过期时间损坏: %w", err)
	}
	payload, err := s.groupPayload(ctx, info.GroupID, info.GroupSalutation, info.GroupCustomMessage, expiresAt)
	if err != nil {
		return nil, err
	}
	return &payload, nil
}

// Logout 删除令牌对应会话；令牌不存在时视为已退出，不报错。
func (s *Service) Logout(ctx context.Context, token []byte) error {
	return s.db.DeleteSession(ctx, cryptox.SessionTokenDigest(token))
}

// groupPayload 组装同行组对外数据：称呼、专属短句与按序礼品摘要。
// 组下无 active 礼品属于数据异常（契约要求每组至少一份），按失败处理，
// 不向外展示脏数据（产品架构 §12 "礼品配置缺失或无法读取"）。
func (s *Service) groupPayload(ctx context.Context, groupID, salutation, customMessage string, expiresAt time.Time) (GroupPayload, error) {
	gifts, err := s.db.GiftSummariesForGroup(ctx, groupID)
	if err != nil {
		return GroupPayload{}, err
	}
	if len(gifts) == 0 {
		return GroupPayload{}, errors.New("同行组无可用礼品数据")
	}
	return GroupPayload{
		GroupID:       groupID,
		Salutation:    salutation,
		CustomMessage: customMessage,
		ExpiresAt:     expiresAt,
		Gifts:         gifts,
	}, nil
}
