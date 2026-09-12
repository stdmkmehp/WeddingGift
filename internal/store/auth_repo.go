// 本文件实现 M4 鉴权所需的 store 层能力（技术架构 §8 一对一绑定算法、§6 会话）：
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
// 使用 BEGIN IMMEDIATE 与 UNIQUE(access_keys.group_id) 共同保护一对一关系，
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

// LoginWithCredentials 执行 §8.1 的原子绑定/登录流程。
//
// 入参为规范化登录名称、明文密钥的 HMAC 摘要、新会话令牌摘要与会话过期时间；
// 明文密钥从不进入本函数。返回 (outcome, true) 表示成功并 COMMIT；
// 返回 (nil, false) 表示业务层面的鉴权失败（名称无效、密钥无效/停用、
// 跨组、组已有其他密钥等），调用方不得向外部区分具体原因；
// 返回 error 表示数据库级故障。
func (d *DB) LoginWithCredentials(ctx context.Context, normalizedName string, keyDigest, sessionDigest []byte, sessionExpiresAt, now string) (*LoginOutcome, bool, error) {
	tx, err := d.BeginImmediate(ctx)
	if err != nil {
		return nil, false, err
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
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("解析登录名称失败: %w", err)
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
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("查询密钥失败: %w", err)
	}

	firstBind := false
	switch {
	case !keyGroupID.Valid:
		// 3a. 密钥未绑定：先确认目标组尚无密钥（IMMEDIATE 写锁下读是新的），
		// 再用条件更新完成绑定；UNIQUE(group_id) 是最终防线。
		var existing int
		if err := tx.conn.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM access_key WHERE group_id = ?", memberGroupID,
		).Scan(&existing); err != nil {
			return nil, false, fmt.Errorf("检查组绑定状态失败: %w", err)
		}
		if existing > 0 {
			return nil, false, nil
		}
		res, err := tx.conn.ExecContext(ctx,
			"UPDATE access_key SET group_id = ?, bound_at = ? WHERE id = ? AND group_id IS NULL",
			memberGroupID, now, keyID,
		)
		if err != nil {
			return nil, false, fmt.Errorf("绑定密钥失败: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil || n != 1 {
			return nil, false, fmt.Errorf("绑定密钥失败: 影响行数异常: %v", err)
		}
		firstBind = true
	case keyGroupID.String == memberGroupID:
		// 3b. 密钥已绑定本组：同组再次登录，仅创建会话。
	default:
		// 3c. 密钥已绑定其他组：通用失败。
		return nil, false, nil
	}

	// 4. 创建会话。
	if _, err := tx.conn.ExecContext(ctx, `
		INSERT INTO session (token_digest, group_id, access_key_id, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		sessionDigest, memberGroupID, keyID, sessionExpiresAt, now,
	); err != nil {
		return nil, false, fmt.Errorf("创建会话失败: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("提交登录事务失败: %w", err)
	}
	return &LoginOutcome{
		GroupID:            memberGroupID,
		GroupSalutation:    salutation,
		GroupCustomMessage: customMessage,
		AccessKeyID:        keyID,
		AccessKeyRef:       keyRef,
		FirstBind:          firstBind,
	}, true, nil
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

// GiftSummary 是一份礼品的对外展示摘要（不含激活码；明文码只走 M5 的 reveal 通道）。
type GiftSummary struct {
	ID            string // 内部 ID，作为 reveal 接口的 giftId（不透明标识）
	TypeKey       string
	Title         string
	Subtitle      string
	ThemeVariant  string
	Instructions  string
	ValidityText  string
	RedemptionURL string
	Ordinal       int
}

// GiftSummariesForGroup 返回同行组全部 active 礼品的展示摘要，按 ordinal 升序。
// 实例级覆盖字段优先于类型默认值（空串表示未覆盖，见数据契约 §2.6）。
func (d *DB) GiftSummariesForGroup(ctx context.Context, groupID string) ([]GiftSummary, error) {
	rows, err := d.db.QueryContext(ctx, `
		SELECT gi.id, gi.type_key, gt.title, gt.subtitle, gt.theme_variant,
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
		if err := rows.Scan(&g.ID, &g.TypeKey, &g.Title, &g.Subtitle, &g.ThemeVariant,
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
