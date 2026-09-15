package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/weddinggift/wedding/internal/cryptox"
	"github.com/weddinggift/wedding/internal/domain"
)

// appendBaseline 是追加写入前从库内读取的现状基线：state 供
// domain.ValidateAppend 开放世界校验，groupID/memberID 把 external_ref
// 解析为内部 id。
type appendBaseline struct {
	state    *domain.ExistingState
	groupID  map[string]string
	memberID map[string]string
}

// queryer 抽象 *sql.DB 与 *sql.Tx 的查询能力，便于在事务内外复用基线读取。
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// LoadExistingState 读取追加校验所需的库内现状基线（data append --dry-run 用）。
// 既有礼品凭证在调用栈内解密为明文放入基线，不落库、不进日志。
func (d *DB) LoadExistingState(ctx context.Context, cx *cryptox.Service) (*domain.ExistingState, error) {
	b, err := d.loadAppendBaseline(ctx, d.db, cx)
	if err != nil {
		return nil, err
	}
	return b.state, nil
}

func (d *DB) loadAppendBaseline(ctx context.Context, q queryer, cx *cryptox.Service) (*appendBaseline, error) {
	b := &appendBaseline{
		state: &domain.ExistingState{
			GroupRefs:          make(map[string]string),
			MemberRefs:         make(map[string]bool),
			DisplayNameNorms:   make(map[string]string),
			LoginNameNorms:     make(map[string]string),
			TypeKeys:           make(map[string]bool),
			GiftItemRefs:       make(map[string]bool),
			OrdinalsByGroup:    make(map[string]map[int]string),
			GiftCodePlaintexts: make(map[string]string),
		},
		groupID:  make(map[string]string),
		memberID: make(map[string]string),
	}

	scan := func(label, query string, fill func(*sql.Rows) error) error {
		rows, err := q.QueryContext(ctx, query)
		if err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		defer rows.Close()
		for rows.Next() {
			if err := fill(rows); err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("%s: %w", label, err)
		}
		return nil
	}

	err := scan("读取既有同行组失败",
		"SELECT id, external_ref, status FROM recipient_group",
		func(rows *sql.Rows) error {
			var id, ref, status string
			if err := rows.Scan(&id, &ref, &status); err != nil {
				return err
			}
			b.groupID[ref] = id
			b.state.GroupRefs[ref] = status
			return nil
		})
	if err != nil {
		return nil, err
	}

	err = scan("读取既有成员失败",
		"SELECT id, external_ref, display_name_norm FROM member",
		func(rows *sql.Rows) error {
			var id, ref, norm string
			if err := rows.Scan(&id, &ref, &norm); err != nil {
				return err
			}
			b.memberID[ref] = id
			b.state.MemberRefs[ref] = true
			b.state.DisplayNameNorms[norm] = ref
			return nil
		})
	if err != nil {
		return nil, err
	}

	err = scan("读取既有登录名称失败",
		`SELECT ln.normalized_value, ln.kind, m.external_ref
		 FROM member_login_name ln JOIN member m ON m.id = ln.member_id`,
		func(rows *sql.Rows) error {
			var norm, kind, ref string
			if err := rows.Scan(&norm, &kind, &ref); err != nil {
				return err
			}
			b.state.LoginNameNorms[norm] = ref + "/" + kind
			return nil
		})
	if err != nil {
		return nil, err
	}

	err = scan("读取既有礼品类型失败",
		"SELECT type_key FROM gift_type",
		func(rows *sql.Rows) error {
			var key string
			if err := rows.Scan(&key); err != nil {
				return err
			}
			b.state.TypeKeys[key] = true
			return nil
		})
	if err != nil {
		return nil, err
	}

	err = scan("读取既有礼品实例失败",
		`SELECT gi.external_ref, g.external_ref, gi.ordinal, gi.code_ciphertext
		 FROM gift_item gi JOIN recipient_group g ON g.id = gi.group_id`,
		func(rows *sql.Rows) error {
			var ref, groupRef string
			var ordinal int
			var blob []byte
			if err := rows.Scan(&ref, &groupRef, &ordinal, &blob); err != nil {
				return err
			}
			code, err := cx.DecryptGiftCode(blob)
			if err != nil {
				return fmt.Errorf("解密既有礼品 %s 凭证失败: %w", ref, err)
			}
			b.state.GiftItemRefs[ref] = true
			if b.state.OrdinalsByGroup[groupRef] == nil {
				b.state.OrdinalsByGroup[groupRef] = make(map[int]string)
			}
			b.state.OrdinalsByGroup[groupRef][ordinal] = ref
			b.state.GiftCodePlaintexts[code] = ref
			return nil
		})
	if err != nil {
		return nil, err
	}

	return b, nil
}

// AppendDataset 把校验通过的追加数据包在单个事务内纯 INSERT 写入（数据契约 §2.8）。
//
// 语义约束：
//   - 数据库必须已完成首次导入；空库请使用 data import；
//   - 仅新增五类实体，不修改、不删除任何既有行：事务内重新读取现状基线并
//     复跑 domain.ValidateAppend，任一违规整体回滚、数据库零写入；
//   - 礼品凭证只写 AEAD 密文，明文仅存在于本函数调用栈内；
//   - 数据库 UNIQUE 约束（external_ref / type_key / 名称规范化值 / 组内 ordinal）
//     为最终兜底。服务在线时亦可安全执行（WAL + busy_timeout，写事务极短）。
func (d *DB) AppendDataset(ctx context.Context, ds *domain.Dataset, cx *cryptox.Service) error {
	empty, err := d.IsEmpty(ctx)
	if err != nil {
		return err
	}
	if empty {
		return errors.New("数据库为空：追加要求已完成首次导入，请先使用 data import")
	}

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	baseline, err := d.loadAppendBaseline(ctx, tx, cx)
	if err != nil {
		return err
	}
	if verrs := domain.ValidateAppend(ds, baseline.state); len(verrs) > 0 {
		return fmt.Errorf("追加前复核未通过（%d 处问题，数据库未写入），首处: %s", len(verrs), verrs[0].Error())
	}

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

	// 同行组；新组内部 id 并入 baseline.groupID 供成员/礼品引用。
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
		baseline.groupID[g.ExternalRef] = id
	}

	// 成员；新成员内部 id 并入 baseline.memberID 供登录名称引用。
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
			id, baseline.groupID[m.GroupExternalRef], m.ExternalRef, m.DisplayName,
			domain.NormalizeLoginName(m.DisplayName), canLogin, m.SortOrder,
		); err != nil {
			return fmt.Errorf("写入成员 %s 失败: %w", m.ExternalRef, err)
		}
		baseline.memberID[m.ExternalRef] = id
	}

	// 登录名称（本名与昵称；既有成员仅允许 nickname，校验已保证）。
	for _, ln := range ds.LoginNames {
		id, err := newID()
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO member_login_name (id, member_id, normalized_value, kind)
			VALUES (?, ?, ?, ?)`,
			id, baseline.memberID[ln.MemberExternalRef], domain.NormalizeLoginName(ln.Value), ln.Kind,
		); err != nil {
			return fmt.Errorf("写入成员 %s 的登录名称失败: %w", ln.MemberExternalRef, err)
		}
	}

	// 礼品实例：凭证只写 AEAD 密文。card_secret 模式的密码加密入
	// secret_ciphertext；code 模式写入空 BLOB（列 NOT NULL，模式决定语义）。
	for _, gi := range ds.GiftItems {
		id, err := newID()
		if err != nil {
			return err
		}
		ciphertext, err := cx.EncryptGiftCode(gi.CodePlaintext)
		if err != nil {
			return fmt.Errorf("加密礼品 %s 凭证失败: %w", gi.ExternalRef, err)
		}
		secretCiphertext := []byte{}
		if gi.CredentialMode == domain.CredentialModeCardSecret {
			secretCiphertext, err = cx.EncryptGiftCode(gi.SecretPlaintext)
			if err != nil {
				return fmt.Errorf("加密礼品 %s 密码失败: %w", gi.ExternalRef, err)
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gift_item
			    (id, external_ref, group_id, type_key, credential_mode, code_ciphertext, secret_ciphertext,
			     instructions_override, validity_text_override, redemption_url_override, ordinal, status)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, gi.ExternalRef, baseline.groupID[gi.GroupExternalRef], gi.TypeKey, gi.CredentialMode, ciphertext, secretCiphertext,
			gi.InstructionsOverride, gi.ValidityTextOverride, gi.RedemptionURLOverride,
			gi.Ordinal, gi.Status,
		); err != nil {
			return fmt.Errorf("写入礼品 %s 失败: %w", gi.ExternalRef, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交追加事务失败: %w", err)
	}
	return nil
}
