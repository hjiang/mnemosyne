# Mnemosyne — Architecture

A self-hosted IMAP backup and search service. This document describes the
target architecture; it is the reference for the initial implementation and
should be updated as decisions change.

## 1. Goals and non-goals

**Goals** (from `SPEC.md`)

- Pull and store mail from one or more IMAP accounts per user.
- Deduplicate messages across runs and across folders.
- Back up attachments alongside messages.
- Serve Gmail-style structured search and full-text search over bodies and
  attachment text.
- Export selections as mbox, Maildir, or by uploading to an IMAP folder.
- Run as a single lightweight service on a Linux host.
- Strict multi-user data isolation.

**Non-goals (initial version)**

- Not a mail client: no compose, no reply, no read/unread sync back to IMAP.
- Not a distributed system: single-node, single-process.
- Not a mail server: we never accept SMTP or serve IMAP ourselves (export to
  IMAP uploads to an *external* server the user configures).

## 2. Technology choices

| Concern            | Choice                              | Why                                                                                 |
| ------------------ | ----------------------------------- | ----------------------------------------------------------------------------------- |
| Language / runtime | **Go**                              | Single static binary, strong stdlib for net/http and crypto, good IMAP libraries.   |
| HTTP               | `net/http` + `chi` router           | Minimal, idiomatic, no framework lock-in.                                           |
| Templating / UI    | `html/template` + HTMX              | Server-rendered, no SPA build step, minimal JS — matches "minimalist" UI goal.      |
| Metadata store     | **SQLite** (WAL mode)               | One file, zero ops, fits the "easy to deploy" constraint.                           |
| Full-text index    | **SQLite FTS5**                     | Co-located with metadata, no second daemon, supports prefix + phrase queries.       |
| Blob store         | Filesystem, content-addressed       | Large opaque objects belong on the FS; hash-naming makes dedup a structural invariant. |
| IMAP client        | `github.com/emersion/go-imap` (v2)  | Actively maintained, supports partial fetches and IDLE.                             |
| Mail parsing       | `github.com/emersion/go-message`    | Same author, handles MIME, encodings, headers.                                      |
| Attachment text    | `pdftotext` (poppler) + built-in extractors for docx/odt/html/txt | Shell out for PDFs; pure-Go for the rest. Extractors are pluggable. |
| Auth               | Session cookies, `bcrypt` passwords | Simple, battle-tested, no external IdP required.                                    |
| Scheduling         | In-process ticker + job queue table | No external cron; a daily tick enqueues jobs, workers drain the queue.              |
| Config             | YAML file + env var overrides       | Readable, easy to template from Ansible/NixOS.                                      |

Alternatives considered and rejected:

- **Postgres instead of SQLite** — more operational burden, no feature we need
  that FTS5 can't provide at this scale.
- **Bleve for full-text** — another index format to back up and reason about.
  Only worth it if FTS5 proves inadequate for Gmail-style search, in which
  case the search layer is isolated enough to swap.
- **SPA frontend (React/Svelte)** — build toolchain conflicts with "lightweight
  and easy to deploy". HTMX gives us dynamic updates without a bundler.

## 3. System layout

```
                 ┌────────────────────────────────────────────┐
                 │              mnemosyne (single binary)     │
                 │                                            │
  browser ─────▶ │  HTTP handlers ──▶ services ──▶ repo (SQL) │
                 │        │                │         │        │
                 │        │                ▼         ▼        │
                 │        │           job queue ◀── sqlite    │
                 │        │                │                  │
                 │        │                ▼                  │
                 │        │          worker pool              │
                 │        │           │      │                │
                 │        │           ▼      ▼                │
                 │        │       IMAP    extractor           │
                 │        │       client  (pdftotext, …)      │
                 │        ▼           │                       │
                 │     search         ▼                       │
                 │     (FTS5)    blob store (fs)              │
                 └────────────────────────────────────────────┘
```

All boxes live in one process. The only external dependencies at runtime are
the user's IMAP servers and any extractor binaries on `PATH`.

## 4. Package structure

```
cmd/mnemosyne/           main — flags, config load, wire graph, HTTP listen
internal/
  config/                YAML + env parsing, validation
  auth/                  password hashing, session middleware, user context
  httpserver/            chi router, handlers, templates, static assets
    templates/           html/template files
    static/              htmx.min.js, pico.css or similar
  users/                 CRUD for users
  accounts/              IMAP account + folder + policy CRUD
  backup/                orchestrator: scheduling, job enqueue, IMAP sync
    imap/                thin wrapper over go-imap, retries, idle
    policy/              retention rules (leave-on-server, age, count)
  messages/              storage model: messages, folders, dedup index
  blobs/                 content-addressed filesystem store
  extract/               attachment text extraction (pdf, docx, html, txt)
  search/                query parser (Gmail syntax) + FTS5 execution
  export/                mbox / Maildir / IMAP upload writers
  jobs/                  generic worker pool + queue backed by SQLite
  db/                    sqlx-style helpers, migrations, sqlc if used
    migrations/          numbered .sql files
pkg/                     (empty by default — keep code internal)
```

The `internal/` boundary prevents other Go modules from importing us; the
project is an application, not a library.

## 5. Data model

SQLite, strict mode, foreign keys on. All user-owned tables carry `user_id`.

```
users(
  id INTEGER PK,
  email TEXT UNIQUE NOT NULL,
  password_hash TEXT NOT NULL,
  created_at INTEGER NOT NULL
)

imap_accounts(
  id INTEGER PK,
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  label TEXT NOT NULL,             -- user-visible name
  host TEXT NOT NULL,
  port INTEGER NOT NULL,
  username TEXT NOT NULL,
  password_enc BLOB NOT NULL,      -- encrypted with server key
  use_tls INTEGER NOT NULL,
  last_sync_at INTEGER
)

imap_folders(
  id INTEGER PK,
  account_id INTEGER NOT NULL REFERENCES imap_accounts(id) ON DELETE CASCADE,
  name TEXT NOT NULL,              -- IMAP folder name (UTF-7 decoded)
  uid_validity INTEGER,            -- IMAP UIDVALIDITY, invalidates cache on change
  last_seen_uid INTEGER,           -- highest UID we've processed
  policy_json TEXT NOT NULL,       -- retention policy, see §6
  UNIQUE(account_id, name)
)

-- One row per unique message, keyed by content hash.
-- Physical bytes live on disk at blobs/<hash[:2]>/<hash[2:4]>/<hash>.
messages(
  hash BLOB PRIMARY KEY,           -- sha256 of raw RFC822
  user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  message_id TEXT,                 -- RFC822 Message-ID header, nullable
  from_addr TEXT,
  to_addrs TEXT,                   -- comma-joined
  cc_addrs TEXT,
  subject TEXT,
  date INTEGER,                    -- epoch seconds
  size INTEGER NOT NULL,
  has_attachments INTEGER NOT NULL
)

-- Where a message lives. A message may appear in many folders across
-- accounts; the body is stored once.
message_locations(
  message_hash BLOB NOT NULL REFERENCES messages(hash) ON DELETE CASCADE,
  folder_id INTEGER NOT NULL REFERENCES imap_folders(id) ON DELETE CASCADE,
  uid INTEGER NOT NULL,            -- IMAP UID within that folder
  internal_date INTEGER,
  flags TEXT,                      -- IMAP flags snapshot
  PRIMARY KEY(folder_id, uid)
)
CREATE INDEX idx_locations_by_hash ON message_locations(message_hash);

attachments(
  id INTEGER PK,
  message_hash BLOB NOT NULL REFERENCES messages(hash) ON DELETE CASCADE,
  filename TEXT,
  mime_type TEXT,
  size INTEGER NOT NULL,
  blob_hash BLOB NOT NULL,         -- content hash; extracted body also CAS'd
  text_extracted INTEGER NOT NULL  -- 0 = pending, 1 = done, 2 = failed
)

-- FTS5 virtual table. Rows are keyed by messages.rowid via an
-- external-content configuration so we don't duplicate the payload.
CREATE VIRTUAL TABLE messages_fts USING fts5(
  subject, from_addr, to_addrs, body, attachment_text,
  content='', tokenize='porter unicode61'
);

jobs(
  id INTEGER PK,
  user_id INTEGER NOT NULL,
  kind TEXT NOT NULL,              -- 'backup', 'extract', 'export'
  payload_json TEXT NOT NULL,
  state TEXT NOT NULL,             -- 'pending','running','done','failed'
  attempts INTEGER NOT NULL DEFAULT 0,
  scheduled_for INTEGER NOT NULL,
  started_at INTEGER,
  finished_at INTEGER,
  error TEXT
)
CREATE INDEX idx_jobs_pending ON jobs(state, scheduled_for);

sessions(
  id BLOB PRIMARY KEY,             -- random 32 bytes
  user_id INTEGER NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
)
```

Why `hash` as the `messages` primary key: it makes the dedup invariant
unforgeable. There is no code path that can produce two rows for the same
bytes. `message_locations` is the many-to-one that captures "this message also
appears in INBOX of account 2".

Why `user_id` is on `messages` but dedup is still global-per-user: dedup
spans folders and accounts *within* a user, but never across users — two users
who happen to receive the same mail get two independent rows, because their
data must be isolatable (e.g. for account deletion).

## 6. Retention policies

Stored as JSON on each folder. The policy controls what happens on the
*server* after a successful local backup; the local copy is authoritative
forever.

```json
{
  "leave_on_server": "all"          // keep everything on the server
}
{
  "leave_on_server": "newest_n",
  "n": 1000
}
{
  "leave_on_server": "younger_than",
  "days": 90
}
```

The policy engine produces a set of IMAP UIDs to expunge after each run. This
is the only destructive operation we perform on upstream IMAP, and it runs
only after the blob is durably written and fsync'd.

## 7. Backup pipeline

One pass over one folder:

1. **Connect** to the account, `SELECT` the folder.
2. **UIDVALIDITY check**: if it changed vs. stored value, the folder's UID
   space was reset — drop all `message_locations` for this folder and
   re-scan from UID 1. Blobs are untouched because they're hash-keyed.
3. **List new UIDs**: `UID SEARCH UID <last_seen_uid+1>:*`.
4. For each new UID, in batches:
   a. `FETCH UID ENVELOPE RFC822.SIZE INTERNALDATE BODYSTRUCTURE` (cheap).
   b. Look up by `Message-ID` header in `messages`. If a candidate exists and
      sizes match, we *probably* have it — fetch just enough to confirm the
      hash. Otherwise, fetch `BODY[]`.
   c. Hash the full body. If the hash is already in `messages`, insert only
      `message_locations`. Otherwise write the blob, insert `messages`,
      insert `message_locations`, enqueue an `extract` job per attachment.
   d. Index into FTS5 (subject/from/to/body) immediately; attachment text is
      indexed when the extract job finishes.
5. **Apply retention policy**: compute UIDs to expunge, `UID STORE +FLAGS
   \Deleted`, `EXPUNGE`.
6. Update `last_seen_uid` and `last_sync_at` in a single transaction.

Crash safety: every state transition (blob written → row inserted →
location inserted) happens in that order. If we crash after writing a blob
but before inserting the row, the next run re-downloads and overwrites the
same file (same hash, same bytes). No orphan rows are ever created because
rows are only inserted once the blob is durable.

## 8. Search

Two-layer design:

1. **Query parser** (`internal/search/parser.go`) tokenizes Gmail-style
   input:
   - `from:alice@example.com subject:"quarterly report" budget` →
     `{from: "alice@example.com", subject: "quarterly report", text: "budget"}`
   - Supported operators initially: `from:`, `to:`, `cc:`, `subject:`,
     `has:attachment`, `filename:`, `before:2026-01-01`, `after:`.
2. **Execution** builds a SQL query that combines:
   - Column predicates on `messages` (indexed WHERE clauses).
   - An FTS5 `MATCH` expression for the free text against `messages_fts`.
   - A `user_id` filter — always, injected by the repo layer, never
     optional.

The parser is deliberately separate from execution so we can add operators
without touching SQL, and swap FTS5 for something else later without
rewriting the parser.

## 9. Export

A job kind. Given a search query or an explicit set of message hashes:

- **mbox**: concatenate `From ` separator + raw RFC822 bytes, stream to a
  download handler.
- **Maildir**: write `cur/<timestamp>.<hash>:2,` files into a tar stream.
- **IMAP**: `APPEND` each raw message to a user-supplied folder on a
  user-supplied server.

Exports never materialize to disk server-side — they stream, so large
exports don't bloat the host.

## 10. Multi-user isolation

Enforced in layers, so a bug in one layer doesn't breach:

1. **Session middleware** resolves a session cookie to a `user_id` and
   puts it in `context.Context`. Handlers read from context only.
2. **Repository layer** takes `user_id` as an explicit parameter on every
   method and injects it into every `WHERE` clause. There is no "get by
   id" that doesn't also check `user_id`.
3. **Blob paths** are content-addressed and not user-namespaced, but access
   to them only happens via the repo layer which has already checked
   ownership. Direct filesystem access is not exposed over HTTP.
4. **SQLite foreign key cascades** on `users.id` delete everything a user
   owns — messages, locations, jobs, sessions — when their account is
   removed. Blobs that become unreferenced are garbage-collected by a
   periodic job.

## 11. Secrets

IMAP passwords must be encrypted at rest. On first run, the server generates
a 32-byte key and writes it to `$DATA_DIR/secret.key` (mode 0600). Account
passwords are AES-GCM encrypted with that key before insert. Losing the key
means losing all stored IMAP credentials — this is documented in the deploy
notes, and the key file is what backups must include.

## 12. Configuration

`/etc/mnemosyne/config.yaml` (or `$MNEMOSYNE_CONFIG`):

```yaml
listen: ":8080"
data_dir: "/var/lib/mnemosyne"
base_url: "https://mail.example.com"
session_ttl: "720h"
backup:
  default_schedule: "0 3 * * *"   # 03:00 daily
  max_concurrent: 2
extract:
  pdftotext_path: "/usr/bin/pdftotext"
```

Every field has a sensible default; the minimum config is an empty file.

## 13. Deployment

- Build: `go build -o mnemosyne ./cmd/mnemosyne` → one static binary.
- Layout on host:
  ```
  /usr/local/bin/mnemosyne
  /etc/mnemosyne/config.yaml
  /var/lib/mnemosyne/
    mnemosyne.db          # SQLite
    secret.key            # AES key
    blobs/                # content-addressed store
  ```
- `systemd` unit runs the binary as a dedicated `mnemosyne` user; reverse
  proxy (Caddy/nginx) terminates TLS.
- Backup = stop service, `cp -a /var/lib/mnemosyne`, start service. Or
  `sqlite3 .backup` + rsync the blob dir while running.

## 14. Implementation stages

Tracked in `IMPLEMENTATION_PLAN.md` when work begins. High level:

1. **Skeleton**: config, SQLite + migrations, users + auth, empty web UI.
2. **Accounts + manual fetch**: add IMAP account, pick folders, run a
   one-shot backup with dedup and blob store. No search yet.
3. **Search**: query parser, FTS5 indexing, results UI.
4. **Scheduler + policies**: job queue, daily trigger, retention policy
   execution.
5. **Attachments + extraction**: extractor workers, attachment search.
6. **Export**: mbox, Maildir, IMAP-upload.

Each stage is independently shippable and testable.

## 15. Open questions

- **Encryption of blobs at rest**: the current design encrypts credentials
  but not message bodies. Most self-hosters rely on full-disk encryption;
  adding per-blob encryption adds complexity to dedup (same plaintext must
  encrypt to the same ciphertext, i.e. deterministic AES). Defer until
  requested.
- **OAuth2 IMAP (Gmail XOAUTH2)**: plain IMAP auth only in v1. Gmail's
  app-password path works; full OAuth is a follow-up.
- **IMAP IDLE for near-real-time backup**: the spec says "once a day by
  default", so IDLE is out of scope for v1 but the IMAP wrapper should
  leave room for it.
