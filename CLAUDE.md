# Mnemosyne

Self-hosted IMAP email backup and search application.

## Quick Reference

```bash
nix develop                              # enter dev shell (Go, gopls, golangci-lint, pdftotext, etc.)
go test -race ./...                      # run all tests with race detector
golangci-lint run ./...                  # lint (must pass before commit)
go run ./cmd/mnemosyne serve             # start server (reads config or env)
go run ./cmd/mnemosyne adduser <email>   # create a user interactively
```

## Project Structure

```
cmd/mnemosyne/          CLI entrypoint (serve, adduser)
internal/
  accounts/             IMAP account + folder CRUD, AES-GCM password/token encryption (incl. optional SOCKS5 proxy creds)
  auth/                 bcrypt passwords, sessions, RequireAuth middleware
  backup/               orchestrator pipeline (with reconnect/retry), retention executor
    imap/               thin go-imap v2 client wrapper (LOGIN + OAUTHBEARER, TLS + SOCKS5 proxy support)
    policy/             retention policies (all, newest_n, younger_than)
  blobs/                content-addressed filesystem blob store (sha256)
  config/               YAML + env config with validation
  db/                   SQLite open/migrate with WAL+FK, embedded migrations
    migrations/         0001_init, 0002_imap, 0003_fts, 0004_jobs, 0005_job_progress, 0006_proxy, 0007_oauth
  export/               mbox, maildir (tar), IMAP upload writers + selection iterator
  extract/              text extraction: txt, html, pdf (pdftotext), docx (zip+xml)
  httpserver/           chi router, HTMX templates, handlers
  jobs/                 persistent job queue + worker pool with panic recovery
  messages/             messages, locations, attachments repos + FTS5 indexing
  oauth/                OAuth2 token manager (auth URL, code exchange, token refresh)
  scheduler/            cron-based backup scheduling (robfig/cron/v3)
  search/               Gmail-style query parser + FTS5/SQL executor
  testimap/             in-memory IMAP test server (wraps imapmemserver)
  users/                user CRUD
```

## Tech Stack

- **Go 1.26.1**, module `github.com/hjiang/mnemosyne`
- **SQLite** via `modernc.org/sqlite` (pure Go, no CGo) with WAL mode + foreign keys
- **go-imap v2** (`github.com/emersion/go-imap/v2`) for IMAP operations (LOGIN + OAUTHBEARER via go-sasl)
- **golang.org/x/oauth2** for OAuth2 token lifecycle (Google Workspace IMAP)
- **chi v5** for HTTP routing, HTMX + server-rendered templates for UI
- **FTS5** contentless virtual table for full-text search
- **Nix flake** for reproducible dev environment (`nix develop`)

## Conventions

### User Isolation

Every database query that touches user data **must** include a `user_id` filter. Methods enforcing this are tagged with the comment `// enforces user isolation`. When adding new repo methods, follow this pattern:

```go
// GetByID retrieves a widget scoped to the given user.
// enforces user isolation
func (r *Repo) GetByID(id, userID int64) (*Widget, error) {
    // ... WHERE id = ? AND user_id = ?
}
```

### Crash-Safe Write Ordering

The backup pipeline writes in this order: **blob -> message row -> location row**. This guarantees that if the process crashes between steps, the next run recovers safely via idempotent inserts (`ON CONFLICT DO NOTHING`). Never reorder these steps.

### Content-Addressed Dedup

Messages are keyed by `sha256(raw_body)`. The `messages.hash` column is the primary key. Two locations pointing to the same content share one message row and one blob on disk. Blob path: `<root>/<hash[0:2]>/<hash[2:4]>/<full_hash_hex>`.

### Bug Fixes

When fixing a bug, first write a failing test that reproduces the issue, then fix the code to make the test pass. This ensures the bug is well-understood and won't regress.

### Error Handling in Tests

- Use `t.Fatal` for setup failures, `t.Errorf` for assertion failures
- Use `t.TempDir()` for test directories (auto-cleaned)
- Use `t.Cleanup()` for deferred resource cleanup
- Never use `time.Sleep` in tests; inject a clock function instead

### Linting

The project uses golangci-lint v2 with: errcheck, govet, staticcheck, gosec, gocritic, revive, ineffassign, unused, misspell. All must pass before commit. Common patterns:

- `defer rows.Close() //nolint:errcheck` for SQL row closers
- `//nolint:gosec` for intentional test-only permissions or known-safe SQL construction
- The search executor builds SQL via `fmt.Sprintf` for the WHERE clause structure, but all values are parameterized (`?` placeholders)

### Templates

Authenticated pages extend `layout.html` via `{{define "content"}}`. Login is self-contained (no layout). Layout is parsed alongside each page template via `ParseFS`. Nav active state is auto-injected by `render()` based on template name (`navActiveMap` in `server.go`). Pages needing extra scripts use `{{define "scripts"}}`. Stored in `internal/httpserver/templates/`. Registered in the `templates` map in `server.go:New()`.

### IMAP Testing

Use `internal/testimap` for tests that need an IMAP server. It wraps go-imap's `imapmemserver`:

```go
srv := testimap.New(t)                    // starts on random port, auto-stopped
srv.AddFolder(t, "INBOX", 1)             // create folder
srv.SeedMessages(t, "INBOX", 5)          // seed N test messages
srv.AppendMessage(t, "INBOX", rawBytes)  // seed specific message
// connect with: srv.Addr, srv.Username ("testuser"), srv.Password ("testpass")
```

### Config

Config file at `MNEMOSYNE_CONFIG` (default `/etc/mnemosyne/config.yaml`). Falls back to defaults with env overrides: `MNEMOSYNE_LISTEN`, `MNEMOSYNE_DATA_DIR`, `MNEMOSYNE_BASE_URL`.

Google OAuth is configured via `oauth.google.client_id` / `oauth.google.client_secret` in the YAML config, or env vars `MNEMOSYNE_OAUTH_GOOGLE_CLIENT_ID` / `MNEMOSYNE_OAUTH_GOOGLE_CLIENT_SECRET`. When configured, accounts can authenticate via OAUTHBEARER instead of plain passwords — useful for Google Workspace orgs that disable app passwords.

### Migrations

Add new migrations as `internal/db/migrations/NNNN_name.sql`. They are embedded via `embed.FS` and applied in order on startup. Migrations are forward-only (no down migrations).

### OAuth2 (Google Workspace)

Accounts use one of two auth types: `password` (plain IMAP LOGIN) or `oauth_google` (OAUTHBEARER SASL). The `auth_type` column in `imap_accounts` determines which path the orchestrator takes.

OAuth tokens (refresh + access) are encrypted at rest with the same AES-256-GCM key used for passwords. The `TokenManager` (`internal/oauth`) handles the authorization code flow (browser redirect) and background token refresh. The orchestrator calls `EnsureFreshToken` before each IMAP dial; if the access token is within 5 minutes of expiry, it refreshes automatically.

After exchanging the authorization code for tokens, the OAuth callback fetches the user's email from Google's userinfo endpoint (via `fetchGoogleEmail`) rather than decoding it from the JWT `id_token`. The endpoint also confirms `email_verified` is true.

OAuth state parameters are stored in-memory with a 10-minute expiry. This is appropriate for a single-instance self-hosted app.

## Architecture Decisions

- **Pure Go SQLite** (`modernc.org/sqlite`): no CGo dependency, simplifies cross-compilation and Nix builds
- **Content-addressed blobs on filesystem**: avoids bloating SQLite with large binary data; fsync + rename for atomic writes
- **FTS5 contentless table**: saves ~50% disk vs regular FTS5; we have the original text in the blob store
- **`BODY.PEEK[]`** for IMAP fetch: never sets `\Seen` flag on the upstream server
- **Retention policies as pure functions**: `(messages, now) -> UIDs to expunge`, fully testable without IMAP
- **backupOK guard**: retention never deletes upstream messages unless the backup confirms all blobs are durable
- **Retry while making progress**: the orchestrator reconnects and retries `syncFolder` on connection-level failures (signaled via the `connError` sentinel) as long as either `NewLocations` or `NewEnvelopes` advanced during the attempt. Stopping when no progress is made avoids tight loops on persistent server errors. Envelopes already fetched are carried across retries (the in/out `envelopes` slice on `syncFolder`) so we don't refetch them.
- **SOCKS5 per account**: each `imap_accounts` row carries optional `proxy_host`, `proxy_port`, `proxy_username`, and `proxy_password_enc` columns (migration `0006_proxy`). Empty `proxy_host` means direct connection. The proxy password is encrypted with the same `KeyManager` used for the IMAP password.
- **OAUTHBEARER over XOAUTH2**: OAUTHBEARER (RFC 7628) is the modern standard; go-sasl provides it out of the box. Gmail supports both.
