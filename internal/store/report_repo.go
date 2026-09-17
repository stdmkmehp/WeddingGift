// 本文件实现日报（report daily / serve 进程内调度）所需的只读查询：
// 登录失败记录的富化注解（密钥与名字的归属识别），不改写任何业务数据。
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// LookupAccessKeyByDigest 返回密钥摘要对应密钥的 external_ref、其已绑定
// 同行组的编号与称呼（未绑定为空串）。不命中（含已停用密钥也命中——停用
// 不影响「这是哪张卡」的识别）时 ok=false。仅供登录失败记录与日报富化
// 注解使用，不参与鉴权决策。
func (d *DB) LookupAccessKeyByDigest(ctx context.Context, digest []byte) (ref, boundGroupRef, boundSalutation string, ok bool, err error) {
	err = d.db.QueryRowContext(ctx, `
		SELECT k.external_ref, COALESCE(g.external_ref, ''), COALESCE(g.salutation, '')
		FROM access_key k
		LEFT JOIN recipient_group g ON g.id = k.group_id
		WHERE k.key_digest = ?`,
		digest,
	).Scan(&ref, &boundGroupRef, &boundSalutation)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", false, nil
	}
	if err != nil {
		return "", "", "", false, fmt.Errorf("查询密钥归属失败: %w", err)
	}
	return ref, boundGroupRef, boundSalutation, true, nil
}

// GroupSalutationByLoginName 返回规范化登录名称对应的启用同行组编号与称呼
// （与登录事务同口径：成员可登录且组 active）；无匹配时 ok=false。
// 仅供登录失败记录与日报富化注解使用，不参与鉴权决策。
func (d *DB) GroupSalutationByLoginName(ctx context.Context, normalizedName string) (groupRef, salutation string, ok bool, err error) {
	err = d.db.QueryRowContext(ctx, `
		SELECT g.external_ref, g.salutation
		FROM member_login_name ln
		JOIN member m ON m.id = ln.member_id
		JOIN recipient_group g ON g.id = m.group_id
		WHERE ln.normalized_value = ? AND m.can_login = 1 AND g.status = 'active'`,
		normalizedName,
	).Scan(&groupRef, &salutation)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, fmt.Errorf("解析登录名称归属失败: %w", err)
	}
	return groupRef, salutation, true, nil
}
