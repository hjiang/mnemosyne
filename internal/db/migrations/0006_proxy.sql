-- Add per-account SOCKS5 proxy settings.
-- Empty proxy_host means no proxy.
ALTER TABLE imap_accounts ADD COLUMN proxy_host TEXT NOT NULL DEFAULT '';
ALTER TABLE imap_accounts ADD COLUMN proxy_port INTEGER NOT NULL DEFAULT 0;
ALTER TABLE imap_accounts ADD COLUMN proxy_username TEXT NOT NULL DEFAULT '';
ALTER TABLE imap_accounts ADD COLUMN proxy_password_enc BLOB NOT NULL DEFAULT x'';
