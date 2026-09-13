-- WeddingGift 初始 schema（WG-301）。
-- 对齐 docs/02-technical-architecture.md §6 ER 模型与关键约束：
--   - member.display_name_norm 全局唯一（本名唯一，不设计同名处理）；
--   - member_login_name.normalized_value 全局唯一（本名与所有昵称指向唯一成员）；
--   - access_key.group_id 可空、不设唯一约束（一个密钥最多绑定一个同行组，
--     由绑定事务的条件更新保证；一个同行组可绑定多个密钥）；
--   - 明文密钥/礼品码不入库（key_digest 为 HMAC 摘要，code_ciphertext 为 AEAD 密文）；
--   - 时间统一以 UTC RFC3339 文本存储。

CREATE TABLE recipient_group (
    id             TEXT PRIMARY KEY,
    external_ref   TEXT NOT NULL UNIQUE,
    salutation     TEXT NOT NULL,
    custom_message TEXT NOT NULL DEFAULT '',
    status         TEXT NOT NULL CHECK (status IN ('active', 'disabled')),
    created_at     TEXT NOT NULL
);

CREATE TABLE member (
    id                TEXT PRIMARY KEY,
    group_id          TEXT NOT NULL REFERENCES recipient_group (id),
    external_ref      TEXT NOT NULL UNIQUE,
    display_name      TEXT NOT NULL,
    display_name_norm TEXT NOT NULL UNIQUE,
    can_login         INTEGER NOT NULL CHECK (can_login IN (0, 1)),
    sort_order        INTEGER NOT NULL
);

CREATE TABLE member_login_name (
    id               TEXT PRIMARY KEY,
    member_id        TEXT NOT NULL REFERENCES member (id),
    normalized_value TEXT NOT NULL UNIQUE,
    kind             TEXT NOT NULL CHECK (kind IN ('real_name', 'nickname'))
);

CREATE TABLE access_key (
    id           TEXT PRIMARY KEY,
    external_ref TEXT NOT NULL UNIQUE,
    key_digest   BLOB NOT NULL UNIQUE,
    group_id     TEXT REFERENCES recipient_group (id),
    status       TEXT NOT NULL CHECK (status IN ('active', 'disabled')),
    bound_at     TEXT,
    created_at   TEXT NOT NULL
);

CREATE TABLE gift_type (
    type_key               TEXT PRIMARY KEY,
    title                  TEXT NOT NULL,
    subtitle               TEXT NOT NULL DEFAULT '',
    default_instructions   TEXT NOT NULL,
    default_validity_text  TEXT NOT NULL DEFAULT '',
    default_redemption_url TEXT NOT NULL DEFAULT '',
    theme_variant          TEXT NOT NULL DEFAULT ''
);

CREATE TABLE gift_item (
    id                      TEXT PRIMARY KEY,
    external_ref            TEXT NOT NULL UNIQUE,
    group_id                TEXT NOT NULL REFERENCES recipient_group (id),
    type_key                TEXT NOT NULL REFERENCES gift_type (type_key),
    code_ciphertext         BLOB NOT NULL,
    instructions_override   TEXT NOT NULL DEFAULT '',
    validity_text_override  TEXT NOT NULL DEFAULT '',
    redemption_url_override TEXT NOT NULL DEFAULT '',
    ordinal                 INTEGER NOT NULL,
    status                  TEXT NOT NULL CHECK (status IN ('active', 'disabled')),
    UNIQUE (group_id, ordinal)
);

CREATE TABLE session (
    token_digest  BLOB PRIMARY KEY,
    group_id      TEXT NOT NULL REFERENCES recipient_group (id),
    access_key_id TEXT NOT NULL REFERENCES access_key (id),
    expires_at    TEXT NOT NULL,
    created_at    TEXT NOT NULL
);

CREATE INDEX idx_member_group ON member (group_id);
CREATE INDEX idx_member_login_name_member ON member_login_name (member_id);
CREATE INDEX idx_gift_item_group ON gift_item (group_id);
CREATE INDEX idx_session_access_key ON session (access_key_id);
