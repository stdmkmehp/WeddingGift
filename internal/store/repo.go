package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/weddinggift/wedding/internal/cryptox"
	"github.com/weddinggift/wedding/internal/domain"
)

// newID 生成 128 bit 随机内部 ID（hex）。内部 ID 不出现在日志与对外响应之外的管理输出中。
func newID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成内部 ID 失败: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// utcNow 返回当前 UTC 时间（RFC3339），所有持久化时间统一走这里。
func utcNow() string {
	return time.Now().UTC().Format(time.RFC3339)
}

// IsEmpty 报告业务六表是否全部为空（导入前置条件）。
func (d *DB) IsEmpty(ctx context.Context) (bool, error) {
	tables := []string{
		"recipient_group", "member", "member_login_name",
		"access_key", "gift_type", "gift_item",
	}
	for _, t := range tables {
		var n int
		// 表名来自上面的常量列表，非外部输入。
		if err := d.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+t).Scan(&n); err != nil {
			return false, fmt.Errorf("检查表 %s 失败: %w", t, err)
		}
		if n > 0 {
			return false, nil
		}
	}
	return true, nil
}

// ImportDataset 把校验通过的数据包在单个事务内写入数据库（WG-303）。
//
// 语义约束：
//   - 数据库必须为空库（六张业务表均无数据），否则拒绝导入——重复运行的
//     结果必须可预测，不静默重复（§11.2）；需要重建时先备份、再换新库。
//   - 密钥只写 HMAC 摘要、礼品码只写 AEAD 密文；明文仅存在于本函数调用栈内。
//   - 任一错误全部回滚，数据库零写入。
func (d *DB) ImportDataset(ctx context.Context, ds *domain.Dataset, cx *cryptox.Service) error {
	empty, err := d.IsEmpty(ctx)
	if err != nil {
		return err
	}
	if !empty {
		return errors.New("数据库非空，拒绝导入（避免静默重复）；如需重建请先备份并新建空库后重试")
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := utcNow()

	// 礼品类型（被 gift_item 引用，最先写入）。
	for _, gt := range ds.GiftTypes {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gift_type
			    (type_key, title, subtitle, default_instructions, default_validity_text, default_redemption_url, theme_variant)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			gt.TypeKey, gt.Title, gt.Subtitle, gt.DefaultInstructions,
			gt.DefaultValidityText, gt.DefaultRedemptionURL, gt.ThemeVariant,
		); err != nil {
			return fmt.Errorf("写入礼品类型 %s 失败: %w", gt.TypeKey, err)
		}
	}

	// 同行组；记录 external_ref -> 内部 id 供成员/礼品/密钥引用。
	groupID := make(map[string]string, len(ds.Groups))
	for _, g := range ds.Groups {
		id, err := newID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO recipient_group (id, external_ref, salutation, custom_message, status, created_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			id, g.ExternalRef, g.Salutation, g.CustomMessage, g.Status, now,
		); err != nil {
			return fmt.Errorf("写入同行组 %s 失败: %w", g.ExternalRef, err)
		}
		groupID[g.ExternalRef] = id
	}

	// 成员；记录 external_ref -> 内部 id 供登录名称引用。
	memberID := make(map[string]string, len(ds.Members))
	for _, m := range ds.Members {
		id, err := newID()
		if err != nil {
			return err
		}
		canLogin := 0
		if m.CanLogin {
			canLogin = 1
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO member (id, group_id, external_ref, display_name, display_name_norm, can_login, sort_order)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			id, groupID[m.GroupExternalRef], m.ExternalRef, m.DisplayName,
			domain.NormalizeLoginName(m.DisplayName), canLogin, m.SortOrder,
		); err != nil {
			return fmt.Errorf("写入成员 %s 失败: %w", m.ExternalRef, err)
		}
		memberID[m.ExternalRef] = id
	}

	// 登录名称（本名与昵称）。
	for _, ln := range ds.LoginNames {
		id, err := newID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO member_login_name (id, member_id, normalized_value, kind)
			VALUES (?, ?, ?, ?)`,
			id, memberID[ln.MemberExternalRef], domain.NormalizeLoginName(ln.Value), ln.Kind,
		); err != nil {
			return fmt.Errorf("写入成员 %s 的登录名称失败: %w", ln.MemberExternalRef, err)
		}
	}

	// 卡片密钥：只写 HMAC 摘要，group_id 为空（导入不预绑定）。
	for _, k := range ds.AccessKeys {
		id, err := newID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO access_key (id, external_ref, key_digest, group_id, status, bound_at, created_at)
			VALUES (?, ?, ?, NULL, ?, NULL, ?)`,
			id, k.ExternalRef, cx.AccessKeyDigest(k.CodePlaintext), k.Status, now,
		); err != nil {
			return fmt.Errorf("写入密钥 %s 失败: %w", k.ExternalRef, err)
		}
	}

	// 礼品实例：只写 AEAD 密文。
	for _, gi := range ds.GiftItems {
		id, err := newID()
		if err != nil {
			return err
		}
		ciphertext, err := cx.EncryptGiftCode(gi.CodePlaintext)
		if err != nil {
			return fmt.Errorf("加密礼品 %s 激活码失败: %w", gi.ExternalRef, err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gift_item
			    (id, external_ref, group_id, type_key, code_ciphertext,
			     instructions_override, validity_text_override, redemption_url_override, ordinal, status)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, gi.ExternalRef, groupID[gi.GroupExternalRef], gi.TypeKey, ciphertext,
			gi.InstructionsOverride, gi.ValidityTextOverride, gi.RedemptionURLOverride,
			gi.Ordinal, gi.Status,
		); err != nil {
			return fmt.Errorf("写入礼品 %s 失败: %w", gi.ExternalRef, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交导入事务失败: %w", err)
	}
	return nil
}

// GroupStat 是单个同行组的脱敏状态（status 命令输出用）。
// 严格遵循「只显示内部编号和数量」，不含称呼等成员信息。
type GroupStat struct {
	ExternalRef string
	Status      string
	MemberCount int
	GiftCount   int
	KeyRef      string // 已绑定密钥的 external_ref；未绑定为空
	KeyStatus   string
}

// Stats 是 status 命令的脱敏统计结果：只含内部编号与数量，不含明文密钥/礼品码。
type Stats struct {
	SchemaVersion   int
	Groups          int
	GroupsActive    int
	Members         int
	MembersCanLogin int
	LoginNames      int
	GiftTypes       int
	GiftItems       int
	GiftItemsActive int
	AccessKeys      int
	KeysActive      int
	KeysDisabled    int
	KeysBound       int
	Sessions        int
	GroupStats      []GroupStat
}

// Stats 汇总当前数据库的脱敏统计。
func (d *DB) Stats(ctx context.Context) (*Stats, error) {
	s := &Stats{}
	var err error
	if s.SchemaVersion, err = d.SchemaVersion(ctx); err != nil {
		return nil, err
	}

	count := func(dst *int, query string, args ...any) error {
		return d.db.QueryRowContext(ctx, query, args...).Scan(dst)
	}
	queries := []struct {
		dst   *int
		query string
	}{
		{&s.Groups, "SELECT COUNT(*) FROM recipient_group"},
		{&s.GroupsActive, "SELECT COUNT(*) FROM recipient_group WHERE status = 'active'"},
		{&s.Members, "SELECT COUNT(*) FROM member"},
		{&s.MembersCanLogin, "SELECT COUNT(*) FROM member WHERE can_login = 1"},
		{&s.LoginNames, "SELECT COUNT(*) FROM member_login_name"},
		{&s.GiftTypes, "SELECT COUNT(*) FROM gift_type"},
		{&s.GiftItems, "SELECT COUNT(*) FROM gift_item"},
		{&s.GiftItemsActive, "SELECT COUNT(*) FROM gift_item WHERE status = 'active'"},
		{&s.AccessKeys, "SELECT COUNT(*) FROM access_key"},
		{&s.KeysActive, "SELECT COUNT(*) FROM access_key WHERE status = 'active'"},
		{&s.KeysDisabled, "SELECT COUNT(*) FROM access_key WHERE status = 'disabled'"},
		{&s.KeysBound, "SELECT COUNT(*) FROM access_key WHERE group_id IS NOT NULL"},
		{&s.Sessions, "SELECT COUNT(*) FROM session"},
	}
	for _, q := range queries {
		if err := count(q.dst, q.query); err != nil {
			return nil, fmt.Errorf("统计查询失败: %w", err)
		}
	}

	rows, err := d.db.QueryContext(ctx, `
		SELECT g.external_ref, g.status,
		       (SELECT COUNT(*) FROM member m WHERE m.group_id = g.id),
		       (SELECT COUNT(*) FROM gift_item gi WHERE gi.group_id = g.id AND gi.status = 'active'),
		       IFNULL((SELECT k.external_ref FROM access_key k WHERE k.group_id = g.id), ''),
		       IFNULL((SELECT k.status FROM access_key k WHERE k.group_id = g.id), '')
		FROM recipient_group g
		ORDER BY g.external_ref`)
	if err != nil {
		return nil, fmt.Errorf("查询同行组状态失败: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var gs GroupStat
		if err := rows.Scan(&gs.ExternalRef, &gs.Status, &gs.MemberCount, &gs.GiftCount, &gs.KeyRef, &gs.KeyStatus); err != nil {
			return nil, err
		}
		s.GroupStats = append(s.GroupStats, gs)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return s, nil
}

// RevokeResult 描述 key revoke 的结果。
type RevokeResult string

const (
	// RevokeDone 表示密钥已从 active 停用，相关会话已删除。
	RevokeDone RevokeResult = "revoked"
	// RevokeAlreadyDisabled 表示密钥此前已处于停用状态（会话仍会被清理一遍）。
	RevokeAlreadyDisabled RevokeResult = "already_disabled"
	// RevokeNotFound 表示不存在该 external_ref 的密钥。
	RevokeNotFound RevokeResult = "not_found"
)

// RevokeAccessKey 停用指定 external_ref 的密钥并删除其全部会话（WG-304）。
// 停用即终态：不自动生成替代密钥，也不提供改绑。
func (d *DB) RevokeAccessKey(ctx context.Context, externalRef string) (RevokeResult, error) {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback() }()

	var id, status string
	err = tx.QueryRowContext(ctx,
		"SELECT id, status FROM access_key WHERE external_ref = ?", externalRef,
	).Scan(&id, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return RevokeNotFound, nil
	}
	if err != nil {
		return "", fmt.Errorf("查询密钥失败: %w", err)
	}

	result := RevokeAlreadyDisabled
	if status == domain.StatusActive {
		if _, err := tx.ExecContext(ctx,
			"UPDATE access_key SET status = ? WHERE id = ?", domain.StatusDisabled, id,
		); err != nil {
			return "", fmt.Errorf("停用密钥失败: %w", err)
		}
		result = RevokeDone
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM session WHERE access_key_id = ?", id); err != nil {
		return "", fmt.Errorf("删除密钥会话失败: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return "", fmt.Errorf("提交停用事务失败: %w", err)
	}
	return result, nil
}
