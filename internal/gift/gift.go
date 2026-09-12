// Package gift 实现 M5 的多礼品服务（WG-501）：按 giftId 校验归属并逐份解密礼品码。
//
// 礼品摘要列表随登录/bootstrap 下发（见 auth.GroupPayload）；明文激活码只通过
// 本包的 RevealCode 按需解密，且必须再次校验礼品属于当前会话同行组，
// 不能只信任前端持有的列表（技术架构 §9.1）。
package gift

import (
	"context"
	"errors"
	"fmt"

	"github.com/weddinggift/wedding/internal/cryptox"
	"github.com/weddinggift/wedding/internal/store"
)

// ErrUnavailable 是对外统一的礼品不可用错误：礼品不存在、不属于当前同行组
// 或已停用，对外不区分具体原因（避免借响应差异枚举内部 ID）。
var ErrUnavailable = errors.New("礼物信息暂时无法读取")

// Service 编排礼品码的归属校验与解密。
type Service struct {
	db *store.DB
	cx *cryptox.Service
}

// New 构造礼品服务。
func New(db *store.DB, cx *cryptox.Service) *Service {
	return &Service{db: db, cx: cx}
}

// RevealCode 校验 giftID 属于 groupID 后解密并返回明文激活码。
//
//   - 礼品不存在/跨组/已停用：ErrUnavailable；
//   - 密文损坏（数据异常）：返回内部错误，调用方记录日志并返回通用 500，
//     不向外展示脏数据（产品架构 §12 "礼品配置缺失或无法读取"）。
func (s *Service) RevealCode(ctx context.Context, groupID, giftID string) (string, error) {
	blob, ok, err := s.db.GiftCodeCiphertext(ctx, groupID, giftID)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", ErrUnavailable
	}
	code, err := s.cx.DecryptGiftCode(blob)
	if err != nil {
		return "", fmt.Errorf("礼品码解密失败（数据异常）: %w", err)
	}
	return code, nil
}
