ALTER TABLE imap_accounts ADD COLUMN auth_type TEXT NOT NULL DEFAULT 'password';
ALTER TABLE imap_accounts ADD COLUMN refresh_token_enc BLOB;
ALTER TABLE imap_accounts ADD COLUMN access_token_enc BLOB;
ALTER TABLE imap_accounts ADD COLUMN token_expiry INTEGER;
