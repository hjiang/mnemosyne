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
  accounts/             IMAP account + folder CRUD, AES-GCM password encryption
  auth/                 bcrypt passwords, sessions, RequireAuth middleware
  backup/               orchestrator pipeline, retention executor
    imap/               thin go-imap v2 client wrapper
    policy/             retention policies (all, newest_n, younger_than)
  blobs/                content-addressed filesystem blob store (sha256)
  config/               YAML + env config with validation
  db/                   SQLite open/migrate with WAL+FK, embedded migrations
    migrations/         0001_init, 0002_imap, 0003_fts, 0004_jobs
  export/               mbox, maildir (tar), IMAP upload writers + selection iterator
  extract/              text extraction: txt, html, pdf (pdftotext), docx (zip+xml)
  httpserver/           chi router, HTMX templates, handlers
  jobs/                 persistent job queue + worker pool with panic recovery
  messages/             messages, locations, attachments repos + FTS5 indexing
  scheduler/            cron-based backup scheduling (robfig/cron/v3)
  search/               Gmail-style query parser + FTS5/SQL executor
  testimap/             in-memory IMAP test server (wraps imapmemserver)
  users/                user CRUD
```

## Tech Stack

- **Go 1.26.1**, module `github.com/hjiang/mnemosyne`
- **SQLite** via `modernc.org/sqlite` (pure Go, no CGo) with WAL mode + foreign keys
- **go-imap v2** (`github.com/emersion/go-imap/v2`) for IMAP operations
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

### Migrations

Add new migrations as `internal/db/migrations/NNNN_name.sql`. They are embedded via `embed.FS` and applied in order on startup. Migrations are forward-only (no down migrations).

## Architecture Decisions

- **Pure Go SQLite** (`modernc.org/sqlite`): no CGo dependency, simplifies cross-compilation and Nix builds
- **Content-addressed blobs on filesystem**: avoids bloating SQLite with large binary data; fsync + rename for atomic writes
- **FTS5 contentless table**: saves ~50% disk vs regular FTS5; we have the original text in the blob store
- **`BODY.PEEK[]`** for IMAP fetch: never sets `\Seen` flag on the upstream server
- **Retention policies as pure functions**: `(messages, now) -> UIDs to expunge`, fully testable without IMAP
- **backupOK guard**: retention never deletes upstream messages unless the backup confirms all blobs are durable
