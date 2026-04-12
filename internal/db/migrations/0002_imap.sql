CREATE TABLE IF NOT EXISTS imap_accounts (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    label TEXT NOT NULL,
    host TEXT NOT NULL,
    port INTEGER NOT NULL,
    username TEXT NOT NULL,
    password_enc BLOB NOT NULL,
    use_tls INTEGER NOT NULL DEFAULT 1,
    last_sync_at INTEGER
);

CREATE INDEX IF NOT EXISTS idx_imap_accounts_user ON imap_accounts(user_id);

CREATE TABLE IF NOT EXISTS imap_folders (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id INTEGER NOT NULL REFERENCES imap_accounts(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 0,
    uid_validity INTEGER,
    last_seen_uid INTEGER NOT NULL DEFAULT 0,
    policy_json TEXT NOT NULL DEFAULT '{"leave_on_server":"all"}',
    UNIQUE(account_id, name)
);

CREATE TABLE IF NOT EXISTS messages (
    hash BLOB PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    message_id TEXT,
    from_addr TEXT,
    to_addrs TEXT,
    cc_addrs TEXT,
    subject TEXT,
    date INTEGER,
    size INTEGER NOT NULL,
    has_attachments INTEGER NOT NULL DEFAULT 0,
    body_text TEXT
);

CREATE INDEX IF NOT EXISTS idx_messages_user ON messages(user_id);
CREATE INDEX IF NOT EXISTS idx_messages_message_id ON messages(user_id, message_id);

CREATE TABLE IF NOT EXISTS message_locations (
    message_hash BLOB NOT NULL REFERENCES messages(hash) ON DELETE CASCADE,
    folder_id INTEGER NOT NULL REFERENCES imap_folders(id) ON DELETE CASCADE,
    uid INTEGER NOT NULL,
    internal_date INTEGER,
    flags TEXT,
    PRIMARY KEY(folder_id, uid)
);

CREATE INDEX IF NOT EXISTS idx_locations_by_hash ON message_locations(message_hash);

CREATE TABLE IF NOT EXISTS attachments (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    message_hash BLOB NOT NULL REFERENCES messages(hash) ON DELETE CASCADE,
    filename TEXT,
    mime_type TEXT,
    size INTEGER NOT NULL,
    blob_hash BLOB NOT NULL,
    text_extracted INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_attachments_message ON attachments(message_hash);
