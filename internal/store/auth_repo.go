// 本文件实现 M4 鉴权所需的 store 层能力（技术架构 §8 绑定算法、§6 会话）：
// BEGIN IMMEDIATE 绑定事务、会话读写与礼品摘要查询。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// immediateConn 在专用连接上手动执行 BEGIN IMMEDIATE / COMMIT / ROLLBACK。
// database/sql 的 BeginTx 对 SQLite 是 DEFERRED 事务；§8.2 要求绑定流程
// 使用 BEGIN IMMEDIATE 与条件更新共同保护「一个密钥最多绑定一个同行组」，
// 因此这里显式控制事务语句。所有语句都落在同一连接上，语义与 sql.Tx 等价。
type immediateConn struct {
	conn *sql.Conn
	done bool
}

// BeginImmediate 在专用连接上开启 IMMEDIATE 事务（拿到写锁后才开始读写）。
func (d *DB) BeginImmediate(ctx context.Context) (*immediateConn, error) {
	conn, err := d.db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("开启 IMMEDIATE 事务失败: %w", err)
	}
	return &immediateConn{conn: conn}, nil
}

// Commit 提交事务并释放连接。
func (t *immediateConn) Commit() error {
	if t.done {
		return errors.New("事务已结束")
	}
	t.done = true
	_, err := t.conn.ExecContext(context.Background(), "COMMIT")
	_ = t.conn.Close()
	return err
}

// Rollback 回滚事务并释放连接；Commit 后调用是安全的空操作。
func (t *immediateConn) Rollback() error {
	if t.done {
		return nil
	}
	t.done = true
	_, err := t.conn.ExecContext(context.Background(), "ROLLBACK")
	_ = t.conn.Close()
	return err
}

// LoginOutcome 是一次成功登录的数据侧结果：已建立的绑定与新建会话。
type LoginOutcome struct {
	GroupID            string
	GroupSalutation    string
	GroupCustomMessage string
	AccessKeyID        string
	AccessKeyRef       string
	// FirstBind 表示本次请求是否完成了密钥与同行组的首次绑定（日志用）。
	FirstBind bool
}

// LoginDenyReason 是业务层面鉴权失败的原因分类，仅当 LoginWithCredentials
// 返回的 *LoginOutcome 为 nil 时有意义。
type LoginDenyReason int

const (
	// LoginDenyNone 表示未拒绝（登录成功）。
	LoginDenyNone LoginDenyReason = iota
	// LoginDenyGeneric 是统一模糊失败：名称无效、密钥无效或已停用，
	// 调用方不得向外部区分具体原因。
	LoginDenyGeneric
	// LoginDenyKeyBoundElsewhere 表示名称与密钥各自有效，但密钥已绑定
	// 其他同行组（多为他人冒用名称提前绑定）。该状态对外明确可见，
	// 便于持卡本人联系新郎人工处理（2026-09-13 产品决策调整）。
	LoginDenyKeyBoundElsewhere
)

// LoginWithCredentials 执行 §8.1 的原子绑定/登录流程。
//
// 入参为规范化登录名称、明文密钥的 HMAC 摘要、新会话令牌摘要与会话过期时间；
// 明文密钥从不进入本函数。返回 (outcome, LoginDenyNone, nil) 表示成功并 COMMIT；
// 返回 (nil, reason, nil) 表示业务层面的鉴权失败，reason 区分统一模糊失败与
// 「密钥已绑定其他同行组」；返回 error 表示数据库级故障。
//
// 绑定关系（2026-09-13 起）：一个密钥最多绑定一个同行组；一个同行组可绑定
// 多个密钥，同组使用任一已绑定密钥登录看到的礼品完全相同。
func (d *DB) LoginWithCredentials(ctx context.Context, normalizedName string, keyDigest, sessionDigest []byte, sessionExpiresAt, now string) (*LoginOutcome, LoginDenyReason, error) {
	tx, err := d.BeginImmediate(ctx)
	if err != nil {
		return nil, LoginDenyNone, err
	}
	defer func() { _ = tx.Rollback() }()

	// 1. 登录名称 → 唯一成员与同行组；要求成员可登录且组处于启用状态。
	var memberGroupID, salutation, customMessage string
	err = tx.conn.QueryRowContext(ctx, `
		SELECT g.id, g.salutation, g.custom_message
		FROM member_login_name ln
		JOIN member m ON m.id = ln.member_id
		JOIN recipient_group g ON g.id = m.group_id
		WHERE ln.normalized_value = ? AND m.can_login = 1 AND g.status = 'active'`,
		normalizedName,
	).Scan(&memberGroupID, &salutation, &customMessage)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, LoginDenyGeneric, nil
	}
	if err != nil {
		return nil, LoginDenyNone, fmt.Errorf("解析登录名称失败: %w", err)
	}

	// 2. 密钥摘要 → active 密钥记录。
	var keyID, keyRef string
	var keyGroupID sql.NullString
	err = tx.conn.QueryRowContext(ctx, `
		SELECT id, external_ref, group_id FROM access_key
		WHERE key_digest = ? AND status = 'active'`,
		keyDigest,
	).Scan(&keyID, &keyRef, &keyGroupID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, LoginDenyGeneric, nil
	}
	if err != nil {
		return nil, LoginDenyNone, fmt.Errorf("查询密钥失败: %w", err)
	}

	firstBind := false
	switch {
	case !keyGroupID.Valid:
		// 3a. 密钥未绑定：用条件更新完成绑定（一个同行组允许绑定多个密钥）；
		// BEGIN IMMEDIATE 已序列化并发首绑，WHERE group_id IS NULL 保证
		// 一个密钥最多绑定一个同行组。
		res, err := tx.conn.ExecContext(ctx,
			"UPDATE access_key SET group_id = ?, bound_at = ? WHERE id = ? AND group_id IS NULL",
			memberGroupID, now, keyID,
		)
		if err != nil {
			return nil, LoginDenyNone, fmt.Errorf("绑定密钥失败: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return nil, LoginDenyNone, fmt.Errorf("绑定密钥失败: 影响行数异常: %v", err)
		}
		firstBind = true
	case keyGroupID.String == memberGroupID:
		// 3b. 密钥已绑定本组：同组再次登录，仅创建会话。
	default:
		// 3c. 密钥已绑定其他同行组：对外明确提示密钥已被绑定。
		return nil, LoginDenyKeyBoundElsewhere, nil
	}

	// 4. 创建会话。
	if _, err := tx.conn.ExecContext(ctx, `
		INSERT INTO session (token_digest, group_id, access_key_id, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		sessionDigest, memberGroupID, keyID, sessionExpiresAt, now,
	); err != nil {
		return nil, LoginDenyNone, fmt.Errorf("创建会话失败: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, LoginDenyNone, fmt.Errorf("提交登录事务失败: %w", err)
	}
	return &LoginOutcome{
		GroupID:            memberGroupID,
		GroupSalutation:    salutation,
		GroupCustomMessage: customMessage,
		AccessKeyID:        keyID,
		AccessKeyRef:       keyRef,
		FirstBind:          firstBind,
	}, LoginDenyNone, nil
}

// SessionInfo 是一条有效会话的解析结果。
type SessionInfo struct {
	GroupID            string
	GroupSalutation    string
	GroupCustomMessage string
	AccessKeyID        string
	ExpiresAt          string
}

// FindSessionByTokenDigest 按会话令牌摘要解析有效会话：
// 会话未过期、关联密钥仍为 active、同行组仍为 active 三者同时满足才有效。
// 无效（不存在/过期/密钥停用/组停用）返回 (nil, nil)；过期会话顺手删除。
func (d *DB) FindSessionByTokenDigest(ctx context.Context, tokenDigest []byte, now string) (*SessionInfo, error) {
	var info SessionInfo
	err := d.db.QueryRowContext(ctx, `
		SELECT s.group_id, g.salutation, g.custom_message, s.access_key_id, s.expires_at
		FROM session s
		JOIN access_key k ON k.id = s.access_key_id
		JOIN recipient_group g ON g.id = s.group_id
		WHERE s.token_digest = ? AND k.status = 'active' AND g.status = 'active'`,
		tokenDigest,
	).Scan(&info.GroupID, &info.GroupSalutation, &info.GroupCustomMessage, &info.AccessKeyID, &info.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询会话失败: %w", err)
	}
	// RFC3339 UTC 文本可直接按字典序比较。
	if info.ExpiresAt <= now {
		_, _ = d.db.ExecContext(ctx, "DELETE FROM session WHERE token_digest = ?", tokenDigest)
		return nil, nil
	}
	return &info, nil
}

// DeleteSession 删除指定摘要的会话（退出登录）；不存在时不报错。
func (d *DB) DeleteSession(ctx context.Context, tokenDigest []byte) error {
	if _, err := d.db.ExecContext(ctx, "DELETE FROM session WHERE token_digest = ?", tokenDigest); err != nil {
		return fmt.Errorf("删除会话失败: %w", err)
	}
	return nil
}

// DeleteExpiredSessions 清理全部已过期会话（启动与登录时顺手调用，保持表有界）。
func (d *DB) DeleteExpiredSessions(ctx context.Context, now string) (int64, error) {
	res, err := d.db.ExecContext(ctx, "DELETE FROM session WHERE expires_at <= ?", now)
	if err != nil {
		return 0, fmt.Errorf("清理过期会话失败: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	return n, nil
}

// GiftSummary 是一份礼品的对外展示摘要（不含凭证明文；明文只走 M5 的 reveal 通道）。
type GiftSummary struct {
	ID             string // 内部 ID，作为 reveal 接口的 giftId（不透明标识）
	TypeKey        string
	CredentialMode string // code=单一激活码；card_secret=卡号+密码（卡号走 code 字段）
	Title          string
	Subtitle       string
	ThemeVariant   string
	Instructions   string
	ValidityText   string
	RedemptionURL  string
	Ordinal        int
}

// GiftCredentialCiphertexts 返回指定礼品在当前同行组下的凭证密文（WG-501 reveal 通道）。
// code 模式的 secret 为空 BLOB。礼品不存在、不属于该组或已停用都返回 ok=false——
// 调用方不得向外区分原因。
func (d *DB) GiftCredentialCiphertexts(ctx context.Context, groupID, giftID string) (code, secret []byte, ok bool, err error) {
	err = d.db.QueryRowContext(ctx, `
		SELECT code_ciphertext, secret_ciphertext FROM gift_item
		WHERE id = ? AND group_id = ? AND status = 'active'`,
		giftID, groupID,
	).Scan(&code, &secret)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, fmt.Errorf("查询礼品凭证密文失败: %w", err)
	}
	return code, secret, true, nil
}

// GiftSummariesForGroup 返回同行组全部 active 礼品的展示摘要，按 ordinal 升序。
// 实例级覆盖字段优先于类型默认值（空串表示未覆盖，见数据契约 §2.6）。
func (d *DB) GiftSummariesForGroup(ctx context.Context, groupID string) ([]GiftSummary, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT gi.id, gi.type_key, gi.credential_mode, gt.title, gt.subtitle, gt.theme_variant,
		       CASE WHEN gi.instructions_override <> '' THEN gi.instructions_override ELSE gt.default_instructions END,
		       CASE WHEN gi.validity_text_override <> '' THEN gi.validity_text_override ELSE gt.default_validity_text END,
		       CASE WHEN gi.redemption_url_override <> '' THEN gi.redemption_url_override ELSE gt.default_redemption_url END,
		       gi.ordinal
		FROM gift_item gi
		JOIN gift_type gt ON gt.type_key = gi.type_key
		WHERE gi.group_id = ? AND gi.status = 'active'
		ORDER BY gi.ordinal`,
		groupID,
	)
	if err != nil {
		return nil, fmt.Errorf("查询礼品摘要失败: %w", err)
	}
	defer rows.Close()

	var out []GiftSummary
	for rows.Next() {
		var g GiftSummary
		if err := rows.Scan(&g.ID, &g.TypeKey, &g.CredentialMode, &g.Title, &g.Subtitle, &g.ThemeVariant,
			&g.Instructions, &g.ValidityText, &g.RedemptionURL, &g.Ordinal); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
