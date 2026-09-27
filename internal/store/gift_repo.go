package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/weddinggift/wedding/internal/cryptox"
	"github.com/weddinggift/wedding/internal/domain"
)

// FixGiftCodeResult 描述 gift fix-code 的结果。
type FixGiftCodeResult string

const (
	// FixGiftCodeDone 表示激活码已更新并通过写后读回校验。
	FixGiftCodeDone FixGiftCodeResult = "fixed"
	// FixGiftCodeNotFound 表示不存在该 external_ref 的礼品。
	FixGiftCodeNotFound FixGiftCodeResult = "not_found"
	// FixGiftCodeNotCodeMode 表示礼品存在但不是 code 模式（card_secret 拒绝走本通道）。
	FixGiftCodeNotCodeMode FixGiftCodeResult = "not_code_mode"
)

// FixGiftCode 修正指定 external_ref 的 code 模式礼品激活码（上线后纠错通道，
// 是「只增不改不删」（D-21）的唯一例外：仅当既有激活码明文填错时使用，
// 仅重写 code_ciphertext，不触碰其他任何字段）。
//
// 语义约束：
//   - 新明文不能为空、不能含换行（stdin 读入时的粘贴事故防护）；
//   - 礼品必须存在且 credential_mode 为 code；card_secret 明确拒绝，
//     防止把卡号字段当激活码误改；
//   - 单事务内「加密 → UPDATE → 读回解密比对」后提交，任一环节失败整体回滚；
//   - 明文只存在于调用栈内，不落库、不进日志；
//   - 服务在线时可安全执行（单行 UPDATE 短事务；WAL + busy_timeout），
//     读路径 RevealCode 无缓存直读库，提交后下一请求即生效，无需停服。
func (d *DB) FixGiftCode(ctx context.Context, cx *cryptox.Service, externalRef, newCodePlaintext string) (FixGiftCodeResult, error) {
	if newCodePlaintext == "" {
		return "", errors.New("新激活码不能为空")
	}
	if strings.ContainsAny(newCodePlaintext, "\r\n") {
		return "", errors.New("新激活码不能包含换行")
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	var id, mode string
	err = tx.QueryRowContext(ctx,
		"SELECT id, credential_mode FROM gift_item WHERE external_ref = ?", externalRef,
	).Scan(&id, &mode)
	if errors.Is(err, sql.ErrNoRows) {
		return FixGiftCodeNotFound, nil
	}
	if err != nil {
		return "", fmt.Errorf("查询礼品失败: %w", err)
	}
	if mode != domain.CredentialModeCode {
		return FixGiftCodeNotCodeMode, nil
	}

	ciphertext, err := cx.EncryptGiftCode(newCodePlaintext)
	if err != nil {
		return "", fmt.Errorf("加密礼品 %s 新激活码失败: %w", externalRef, err)
	}
	if _, err := tx.ExecContext(ctx,
		"UPDATE gift_item SET code_ciphertext = ? WHERE id = ?", ciphertext, id,
	); err != nil {
		return "", fmt.Errorf("更新礼品 %s 激活码失败: %w", externalRef, err)
	}

	// 写后读回校验：同一事务内读回密文解密，与目标明文比对，不一致则回滚。
	var blob []byte
	if err := tx.QueryRowContext(ctx,
		"SELECT code_ciphertext FROM gift_item WHERE id = ?", id,
	).Scan(&blob); err != nil {
		return "", fmt.Errorf("读回礼品 %s 密文失败: %w", externalRef, err)
	}
	plain, err := cx.DecryptGiftCode(blob)
	if err != nil {
		return "", fmt.Errorf("读回礼品 %s 解密失败: %w", externalRef, err)
	}
	if plain != newCodePlaintext {
		return "", errors.New("写后读回校验失败：解密结果与目标明文不一致")
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("提交修正事务失败: %w", err)
	}
	return FixGiftCodeDone, nil
}
