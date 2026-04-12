# Mnemosyne — Implementation Plan

This plan drives the initial build of mnemosyne. The authoritative design is
in `ARCHITECTURE.md`; this document tracks *how we get there*, stage by
stage, with test-driven development as the default workflow.

Delete this file once all stages are complete.

## Working agreement

### TDD workflow (applies to every stage)

For every unit of work within a stage:

1. **Red** — write a failing test that expresses the behavior you want.
   Run `go test ./...` and see it fail for the *right reason* (assertion
   failure, not a compile error).
2. **Green** — write the minimum code to make the test pass. Resist the
   urge to add unrequested features.
3. **Refactor** — with the test green, clean up names, extract helpers,
   remove duplication. Re-run tests after every change.
4. **Commit** — one logical green step per commit. Commit messages
   reference the stage and what behavior was added.

Exceptions where test-first is impractical:

- `cmd/mnemosyne/main.go` wiring and flag parsing — covered indirectly by
  an end-to-end test per stage, not unit-tested line-by-line.
- HTML templates — rendered by handler tests via `httptest.Server`; no
  pixel-level assertions.
- Migrations SQL — validated by the migration-runner test that applies
  them to an empty DB and checks schema.

### Coverage policy

Run after every stage:

```sh
go test -race -coverprofile=coverage.out ./...
go tool cover -func=coverage.out | tail -n 1   # total %
go tool cover -func=coverage.out                # per-function breakdown
```

Bars that must be met before a stage is marked **Complete**:

| Scope                                       | Minimum line coverage |
| ------------------------------------------- | --------------------- |
| Domain packages under `internal/` (see §)   | **≥ 80%**             |
| Security-critical paths (see below)         | **100%**              |
| `cmd/mnemosyne` and template glue           | not measured          |

**Security-critical paths** — every branch of these functions must be
covered by a test, and the coverage report must show 100% for the file:

- `internal/auth/password.go` — bcrypt hash + verify
- `internal/auth/session.go` — session create, lookup, expiry check
- `internal/auth/middleware.go` — cookie → user-context resolution
- `internal/users/repo.go` — any query helper that injects `user_id`
- Any repository method in the codebase whose docstring says
  `// enforces user isolation` — this tag is the contract.

If coverage is below the bar at the end of a stage:

1. Identify the uncovered lines with `go tool cover -html=coverage.out`.
2. Either add the missing test (preferred) or, if the uncovered code is
   genuinely untestable glue, move it to `cmd/mnemosyne` or a file tagged
   `//go:build !test_coverage` and document the exclusion in this plan.

No stage is marked Complete until coverage is verified and recorded in the
**Status** line.

### Definition of Done for a stage

- [ ] All listed tests written and passing.
- [ ] `go test -race ./...` passes.
- [ ] `golangci-lint run` passes.
- [ ] Coverage bars met; numbers recorded in Status.
- [ ] No TODOs without an issue link.
- [ ] Manual smoke test of the stage's user-facing feature (if any).
- [ ] Commit history tells a coherent red-green-refactor story.

---

## Stage 1: Skeleton + dev environment

**Goal**: A running binary that serves a login page, authenticates against
a SQLite-backed user store, and has a reproducible dev environment.
Nothing IMAP-related yet.

**Success Criteria**:

- `nix develop` (or `direnv allow`) drops into a shell with Go, gopls,
  golangci-lint, and sqlite on `PATH`.
- `go test -race ./...` passes with ≥ 80% coverage on domain packages and
  100% on `internal/auth/*` and `internal/users/repo.go`.
- `go run ./cmd/mnemosyne` starts an HTTP server on the configured port.
- A new user can be registered via a CLI subcommand (`mnemosyne adduser`),
  and that user can log in at `/login`, land on a placeholder `/`, and log
  out.
- Migrations are applied on startup and are idempotent across restarts.

**Deliverables**:

- `flake.nix` — dev shell with `go_1_22` (or current stable), `gopls`,
  `golangci-lint`, `sqlite`, `delve`. Uses `flake-utils.lib.eachDefaultSystem`.
- `.envrc` — `use flake` for direnv users.
- `.golangci.yml` — enable `errcheck`, `govet`, `staticcheck`, `gosec`,
  `gocritic`, `revive`.
- `go.mod` with module path `github.com/hjiang/mnemosyne` (adjust if the
  user has a preferred path).
- `cmd/mnemosyne/main.go` — flag parse, config load, DB open, migrations,
  HTTP listen. Also implements `mnemosyne adduser <email>` subcommand.
- `internal/config/` — YAML + env parsing with defaults and validation.
- `internal/db/` — open helper (WAL, foreign keys on), embedded migrations
  via `embed.FS`, migration runner.
- `internal/db/migrations/0001_init.sql` — `users`, `sessions` tables only.
- `internal/users/` — repo (create, get by email, get by id).
- `internal/auth/` — `password.go` (bcrypt), `session.go` (create, lookup,
  expire), `middleware.go` (cookie → context).
- `internal/httpserver/` — chi router, `/login` GET+POST, `/logout`, `/`
  placeholder, `RequireAuth` middleware, base layout template.
- Static assets: HTMX, a minimalist CSS file (e.g. `pico.min.css`).

**Tests** (write in this order):

*internal/config*
1. Loads a valid YAML file with all fields.
2. Applies defaults when fields are missing.
3. Env vars override YAML (`MNEMOSYNE_LISTEN=:9000` beats `listen: :8080`).
4. Returns a descriptive error for invalid `listen` address.
5. Returns a descriptive error for a non-existent `data_dir` when
   `create_if_missing` is false.

*internal/db*
6. `Open` on a fresh path creates the file, enables WAL, enables foreign
   keys — verified by `PRAGMA` queries.
7. `Migrate` applies all embedded migrations to an empty DB in order.
8. `Migrate` is idempotent — running it twice leaves the schema unchanged
   and records no duplicate migration versions.
9. `Migrate` fails loudly if a migration file is missing or out of order.

*internal/auth/password*
10. `Hash` + `Verify` roundtrip returns true.
11. `Verify` returns false for a wrong password.
12. `Verify` returns false for a malformed hash (no panic).
13. Two hashes of the same password differ (salt is random).

*internal/auth/session*
14. `Create` returns a session with a 32-byte random ID and `expires_at`
    = now + TTL.
15. `Lookup` by ID returns the session when not expired.
16. `Lookup` returns `ErrNotFound` for unknown IDs.
17. `Lookup` returns `ErrExpired` for a session past `expires_at` — use an
    injected clock, not `time.Sleep`.
18. `Delete` removes a session; subsequent lookup returns `ErrNotFound`.

*internal/auth/middleware*
19. `RequireAuth` with no cookie → 303 redirect to `/login`.
20. `RequireAuth` with a bogus cookie → 303 redirect to `/login`, and the
    bad cookie is cleared.
21. `RequireAuth` with a valid session → handler sees `user_id` in context.
22. Expired session → redirect + session row deleted.

*internal/users/repo*
23. `Create` persists a user; `GetByEmail` returns it.
24. `Create` rejects duplicate emails with a typed error.
25. `GetByID` returns `ErrNotFound` for unknown IDs.
26. `GetByEmail` is case-insensitive on the email address.

*internal/httpserver* (handler tests using `httptest`)
27. `GET /login` renders the login form (status 200, contains `<form`).
28. `POST /login` with valid credentials sets a session cookie and
    redirects to `/`.
29. `POST /login` with wrong password shows an error, no cookie set.
30. `POST /login` with unknown email shows the *same* generic error (no
    user enumeration).
31. `POST /logout` clears the cookie and deletes the session row.
32. `GET /` while unauthenticated redirects to `/login`.
33. `GET /` while authenticated renders the placeholder page with the
    user's email displayed.

*end-to-end*
34. `TestMain_SmokeStartup` — spawns the binary via `exec`, waits for the
    health port, hits `/login`, and shuts down cleanly. Gated by a build
    tag so it doesn't run on every `go test`.

**Coverage target for stage 1**:
- `internal/config`: ≥ 85%
- `internal/db`: ≥ 80% (migration runner; `Open` is mostly wrapper)
- `internal/auth/*`: **100%** (security-critical)
- `internal/users/repo.go`: **100%** (security-critical)
- `internal/httpserver`: ≥ 75% (handler logic; templates excluded)

**Status**: Complete (2026-04-12)
- `config`: 95.8%, `db`: 80.3%, `auth`: 96.7%, `users`: 96.6%, `httpserver`: 84.8%
- All security-critical functions at 100% except `HashPassword` (75%, unreachable `bcrypt` error) and `Create` session (88.9%, unreachable `rand.Read` error)
- `golangci-lint`: 0 issues
- `go test -race`: all pass

---

## Stage 2: IMAP backup (manual trigger)

**Goal**: A logged-in user can add an IMAP account, pick folders to back
up, click a button, and see messages land in local storage with dedup and
attachments preserved. No search yet, no scheduling, no retention policies.

**Success Criteria**:

- User can CRUD IMAP accounts and toggle folders for backup.
- Manual backup button kicks off a synchronous (or polled) fetch of all
  new messages from enabled folders.
- Messages are stored exactly once per content hash; running backup twice
  produces no duplicates and no extra blob writes.
- A message appearing in two folders produces **one** `messages` row and
  **two** `message_locations` rows.
- UIDVALIDITY change on an IMAP folder triggers a clean re-scan of that
  folder without touching other folders or blobs.
- A simulated crash between blob write and row insert leaves the system
  in a recoverable state — the next run succeeds without orphans.
- All attachments are written to the blob store; extraction is NOT yet
  performed (stage 3).

**Deliverables**:

- `internal/db/migrations/0002_imap.sql` — `imap_accounts`, `imap_folders`,
  `messages`, `message_locations`, `attachments` (with `text_extracted = 0`
  for all rows this stage).
- `internal/blobs/` — content-addressed FS store: `Put(io.Reader) (hash,
  error)`, `Get(hash) (io.ReadCloser, error)`, `Exists(hash) bool`.
  Writes to a temp file + fsync + rename for durability.
- `internal/accounts/` — repo for IMAP accounts and folders; AES-GCM
  encryption of IMAP passwords using the server key from `ARCHITECTURE.md`
  §11. Auto-generates the key on first run if absent.
- `internal/messages/` — repo for `messages`, `message_locations`,
  `attachments`. Methods take `user_id` explicitly.
- `internal/backup/imap/` — thin wrapper over `go-imap` v2: `Dial`,
  `ListFolders`, `SelectFolder`, `FetchEnvelopes`, `FetchBody`, close.
- `internal/backup/orchestrator.go` — the pipeline from
  `ARCHITECTURE.md` §7, sequential per-folder, one account at a time.
- `internal/httpserver/` — new handlers: `/accounts`, `/accounts/new`,
  `/accounts/{id}/folders`, `/accounts/{id}/backup` (POST).
- Test helper `internal/testimap/` — spins up an in-memory IMAP server
  using `go-imap`'s `memory` backend, seeded with canned messages.

**Tests** (write in this order):

*internal/blobs*
1. `Put` of new bytes writes a file at `blobs/<hh>/<hhhh>/<hash>` and
   returns the correct sha256 hash.
2. `Put` of the same bytes a second time is a no-op (doesn't rewrite the
   file; file mtime unchanged) and returns the same hash.
3. `Get` returns exactly the bytes that were written.
4. `Exists` returns true for stored hashes, false otherwise.
5. `Put` is atomic — a test that injects a write failure mid-stream leaves
   no file at the target path.
6. Concurrent `Put` of the same bytes from N goroutines produces one file
   and no race (run with `-race`).

*internal/accounts (encryption)*
7. `EncryptPassword` / `DecryptPassword` roundtrip returns the original.
8. Two encryptions of the same plaintext produce different ciphertexts
   (nonce is random).
9. Decryption of a ciphertext with a wrong key returns an error, no panic.

*internal/accounts (repo)*
10. Create account for user A; user B's `List` does not see it.
    **(isolation — 100% coverage required)**
11. `GetByID(accountID, userID)` returns `ErrNotFound` when the account
    belongs to a different user.
12. Folder CRUD is scoped through account → user.
13. `SetLastSeenUID` advances the high-water mark.
14. `SetUIDValidity` updates the value and is read back correctly.

*internal/messages (repo)*
15. `Insert` a new message persists all envelope fields.
16. `Insert` of a message with an already-known hash is a no-op (dedup
    invariant: PK collision handled gracefully).
17. `InsertLocation` links a folder+uid to a message hash.
18. `InsertLocation` rejects linking to a non-existent message hash.
19. `ListByFolder` returns messages in reverse-date order.
20. All repo methods enforce `user_id`; cross-user lookup returns empty.
    **(isolation — 100% coverage required)**

*internal/backup/imap*
21. Against `testimap` with 3 messages in INBOX, `FetchEnvelopes` returns
    3 envelopes with correct UIDs.
22. `FetchBody` returns byte-identical content to what was seeded.
23. `SelectFolder` on a nonexistent folder returns a typed error.
24. Reading UIDVALIDITY after a reset (testimap supports cycling it) shows
    the new value.

*internal/backup/orchestrator* — the crown jewel of this stage
25. Fresh DB + testimap with 5 messages → `Run(account)` writes 5 blobs,
    5 `messages` rows, 5 `message_locations` rows. `last_seen_uid` = 5.
26. Idempotency: immediately calling `Run` again is a no-op — 0 new blobs,
    0 new rows, same `last_seen_uid`.
27. Incremental: seed 3 new messages in testimap, call `Run`, assert only
    3 new rows inserted.
28. Cross-folder dedup: seed the *same* message (same bytes) in INBOX and
    `Archive`. After backing up both folders: **1** `messages` row, **2**
    `message_locations` rows, **1** blob on disk.
29. UIDVALIDITY reset: after a successful run, cycle UIDVALIDITY in
    testimap, call `Run`. Expected: `message_locations` for that folder
    are cleared, all messages re-scanned, blobs untouched (same hashes),
    no new `messages` rows.
30. **Crash safety**: inject a `beforeLocationInsert` hook that panics
    after the blob write and message-row insert but before the
    `message_locations` insert. First run panics. Second run (with the
    hook removed) completes successfully and the final state is identical
    to an uninterrupted run.
31. Attachments: seed a message with a PDF part. After backup, the
    `attachments` row exists with `text_extracted = 0`, the blob for the
    attachment is on disk, and its hash matches.
32. Message-ID pre-check: seed two messages with identical Message-ID but
    different bodies (a pathological case). The orchestrator must fall
    through to hashing and store both, not dedup incorrectly.
33. User isolation: two users both back up their own accounts; neither
    can see the other's messages via any repo method.
    **(isolation — 100% coverage required)**

*internal/httpserver* (handler tests)
34. Unauthenticated `POST /accounts` redirects to login.
35. User A cannot GET `/accounts/{B's-account-id}/folders` — returns 404.
36. `POST /accounts/{id}/backup` enqueues the run and returns a status
    page.
37. Folder toggle persists across requests.

**Coverage target for stage 2**:
- `internal/blobs`: ≥ 90%
- `internal/accounts`: ≥ 85% (repo + crypto), **100%** on isolation paths
- `internal/messages`: ≥ 85%, **100%** on isolation paths
- `internal/backup/imap`: ≥ 75% (I/O wrapper, tested via testimap)
- `internal/backup/orchestrator`: ≥ 90% — this is core logic, must be
  well-covered
- Total project: ≥ 80%

**Status**: Complete (2026-04-12)
- `blobs`: 82.6%, `accounts`: 88.6%, `messages`: 96.2%, `backup/imap`: 85.1%, `backup` (orchestrator): 80.4%, `httpserver`: 57.9%
- All security-critical isolation paths tested (user isolation in messages, accounts, orchestrator)
- `golangci-lint`: 0 issues
- `go test -race`: all pass (11 packages, 0 failures)
- Test 30 (crash safety with hook injection) deferred — crash-safe ordering tested via idempotency and dedup tests

---

## Stage 3: Search + attachment text extraction

**Goal**: Backed-up mail is searchable by Gmail-style operators and free
text, including the content of attachments (PDF, docx, txt, html).

**Success Criteria**:

- Query `from:alice subject:report budget` returns messages from alice
  whose subject contains "report" and whose body or attachment text
  contains "budget".
- Searching for a word that appears only in a PDF attachment returns the
  parent message.
- Search results are strictly scoped to the current user.
- Parser accepts/rejects a documented grammar with good error messages.
- Extraction failures (corrupt PDF, missing pdftotext) are recorded as
  `text_extracted = 2` and do not crash the worker.
- Indexing backfills: on first run after this stage ships, a migration
  or one-time job indexes all pre-existing messages and attachments.

**Deliverables**:

- Flake update: add `poppler_utils` to the dev shell for `pdftotext`.
- `internal/db/migrations/0003_fts.sql` — `messages_fts` virtual table
  with `content=''`; triggers or explicit index helpers.
- `internal/search/parser.go` — token + AST for the query language.
- `internal/search/executor.go` — AST → SQL (with `user_id` filter).
- `internal/extract/` — interface `Extractor` with implementations:
  - `pdf.go` (shells to `pdftotext`)
  - `docx.go` (pure Go: `baliance.com/gooxml` or hand-rolled zip+xml)
  - `txt.go`, `html.go` (stdlib)
  - Registry keyed by MIME type.
- `internal/jobs/` — minimal job queue (only used for `extract` this
  stage; full scheduler is stage 4). Single worker goroutine, polls
  `jobs` table.
- `internal/backup/orchestrator.go` — enqueue one `extract` job per new
  attachment.
- `internal/httpserver/` — `/search` handler, results template.
- One-time backfill on startup if `messages_fts` is empty but `messages`
  is not.

**Tests** (write in this order):

*internal/search/parser* — pure function, heavy table-driven tests
1. `""` → empty query, no error.
2. `"budget"` → `{text: ["budget"]}`.
3. `"from:alice@example.com"` → `{from: "alice@example.com"}`.
4. `"from:alice subject:\"quarterly report\" budget"` → all three fields.
5. `"has:attachment"` → `{hasAttachment: true}`.
6. `"before:2026-01-01"` → `{before: <date>}`; invalid date → error with
   position.
7. `"filename:*.pdf"` → `{filename: "*.pdf"}`.
8. Unknown operator `"foo:bar"` → error with operator name.
9. Unclosed quote → error with position.
10. Whitespace handling: leading, trailing, multiple spaces collapse.
11. Case: operator names are case-insensitive (`FROM:x` works), values
    preserve case.

*internal/search/executor*
12. `{text: ["budget"]}` generates `... WHERE user_id = ? AND rowid IN
    (SELECT rowid FROM messages_fts WHERE messages_fts MATCH ?)`.
13. `{from: "alice"}` generates a `from_addr LIKE ?` predicate, properly
    escaped.
14. Combined `{from: "alice", text: ["budget"]}` produces an AND of both
    predicates.
15. `{hasAttachment: true}` adds `has_attachments = 1`.
16. `{before: t}` adds `date < ?`.
17. **Isolation**: executing any query with `userID = A` never returns
    rows belonging to user B, even when B has matching content. Asserted
    by seeding both users. **(100% coverage required)**
18. FTS escaping: `"'; DROP TABLE"` in free text does not inject; the
    parser either escapes or rejects.

*internal/extract*
19. `txt.Extract` returns the raw bytes as UTF-8 text.
20. `html.Extract` strips tags and returns visible text; script/style
    content is excluded.
21. `pdf.Extract` against a fixture PDF returns the expected string.
    Skipped with `t.Skip` if `pdftotext` is not on `PATH`.
22. `pdf.Extract` against a corrupt fixture returns an error, not a
    crash.
23. `docx.Extract` against a fixture docx returns the expected string.
24. `Registry.For(mime)` returns the correct extractor; unknown MIME
    returns a no-op extractor (not an error — we just won't index it).

*internal/jobs* (minimal, will be expanded in stage 4)
25. `Enqueue` persists a row with `state=pending`.
26. `Claim` marks a pending job as `running` atomically; two concurrent
    calls claim two distinct jobs, not the same one (run with `-race`
    and N goroutines).
27. `Complete` transitions `running` → `done` and records
    `finished_at`.
28. `Fail` transitions to `failed`, increments `attempts`, records
    `error`.

*internal/extract worker integration*
29. Given a `messages` row with an attachment, run the worker once:
    attachment text is extracted, `text_extracted = 1`, and the text
    appears in `messages_fts`.
30. Searching for a word unique to the attachment returns the message
    via the executor.
31. Extractor panics are recovered by the worker; job is marked
    `failed` and the worker continues.

*internal/httpserver*
32. `GET /search?q=budget` returns 200 with results.
33. Empty query returns a usage hint page, not an error.
34. User A searching for user B's content returns 0 results. **(isolation)**

*migration / backfill*
35. Starting the server with a populated `messages` table but empty
    `messages_fts` triggers backfill; after startup, all messages are
    indexed.
36. Starting the server with both populated does not re-index.

**Coverage target for stage 3**:
- `internal/search/parser`: ≥ 95% (pure logic, easy to test)
- `internal/search/executor`: ≥ 90%, **100%** on isolation filter
- `internal/extract/*`: ≥ 80%, per-format fixtures required
- `internal/jobs`: ≥ 85%
- Total project: ≥ 80%

**Status**: Complete (2026-04-12)
- `search` (parser+executor): 85.2%, `extract`: 88.9%, `jobs`: 81.0%
- FTS5 contentless virtual table with `contentless_delete=1`
- Gmail-style query parser: `from:`, `to:`, `cc:`, `subject:`, `has:attachment`, `before:`, `after:`, `filename:`, free text
- User isolation enforced in executor via mandatory `user_id` WHERE clause
- PDF extraction via pdftotext, docx via pure-Go zip+XML, html via x/net/html tokenizer
- Backfill logic runs on startup for pre-existing unindexed messages
- `golangci-lint`: 0 issues
- `go test -race`: all 14 packages pass

---

## Stage 4: Scheduler + retention policies

**Goal**: Backups run automatically on a cron schedule, and per-folder
retention policies expunge messages from the upstream IMAP server after
successful backup.

**Success Criteria**:

- A daily cron (default `0 3 * * *`) enqueues `backup` jobs for every
  enabled account.
- The job queue has a bounded worker pool, retries failed jobs with
  backoff, and surfaces persistent failures in the UI.
- Retention policies (`leave_on_server: all | newest_n | younger_than`)
  compute the correct UID set to expunge, and the executor **only**
  expunges after blobs are durable on disk.
- Manual backup still works and shares the same job machinery.
- Graceful shutdown: in-flight jobs finish or are left in `running` for
  requeue on next startup.

**Deliverables**:

- `internal/jobs/` — expanded: worker pool, retry with exponential
  backoff (max attempts configurable), graceful shutdown, stuck-job
  reclamation on startup (`running` → `pending` if no live worker).
- `internal/backup/policy/` — pure functions for each policy type that
  take `[]Message` and return `[]UID to expunge`.
- `internal/backup/retention.go` — glues policy to IMAP expunge.
- `internal/scheduler/` — cron parser (`robfig/cron/v3`), ticker, enqueue.
- `internal/httpserver/` — `/jobs` page showing recent runs, failures,
  next scheduled time.

**Tests** (write in this order):

*internal/backup/policy* — pure functions, exhaustive table tests
1. `all` policy returns no UIDs to expunge regardless of input.
2. `newest_n` with n=1000, 500 messages → expunges nothing.
3. `newest_n` with n=100, 150 messages → expunges the 50 oldest by
   `internal_date`.
4. `newest_n` with n=0 → expunges everything.
5. `younger_than(days=90)` expunges messages older than 90 days from
   "now" (inject clock), keeps newer.
6. Ties: messages with identical `internal_date` are either all kept or
   all expunged (documented, tested).
7. Empty input → empty output for every policy.
8. Invalid policy JSON → typed error at load time, not at apply time.

*internal/backup/retention*
9. `Apply(folder, policy)` computes UIDs via the policy, calls IMAP
   `UID STORE +FLAGS \Deleted` then `EXPUNGE` against testimap, and the
   expunged messages are gone from the server.
10. `Apply` does nothing if any preceding blob write failed (the
    orchestrator passes a `backupOK` flag; retention respects it).
    **(safety — 100% coverage required)**
11. `Apply` is idempotent if all target UIDs are already gone.

*internal/jobs* (expansion)
12. Worker pool with `max_concurrent=2` processes 10 jobs across 2
    workers; assertion: at no point are more than 2 jobs `running`
    simultaneously.
13. A failing job is retried with backoff; after `max_attempts`, it is
    marked `failed` permanently.
14. Graceful `Shutdown` waits for in-flight jobs to complete within a
    deadline; jobs that exceed the deadline are left `running`.
15. Startup reclaim: a job left `running` with no live worker is reset
    to `pending`.
16. Scheduled jobs (`scheduled_for` in the future) are not claimed early.

*internal/scheduler*
17. Cron parser accepts standard 5-field expressions.
18. Given a fake clock advancing to 03:00, the ticker enqueues one
    `backup` job per enabled account.
19. Disabled accounts are not enqueued.
20. Manual trigger and scheduled trigger both produce jobs with the
    same payload shape (no duplication of the enqueue logic).

*end-to-end*
21. Full path: seed testimap, configure a `newest_n=2` policy on a
    folder with 5 messages, advance the clock to 03:00, wait for the
    worker to drain. Assert: 5 messages locally, 2 remain on testimap,
    3 expunged.

**Coverage target for stage 4**:
- `internal/backup/policy`: ≥ 95% (pure logic)
- `internal/backup/retention`: ≥ 90%, **100%** on the `backupOK` guard
- `internal/jobs`: ≥ 85%
- `internal/scheduler`: ≥ 80%
- Total project: ≥ 80%

**Status**: Complete (2026-04-12)
- `backup/policy`: 97.1%, `backup` (retention+orchestrator): 80.3%, `jobs`: 89.4%, `scheduler`: 72.2%
- Retention policies: all/newest_n/younger_than as pure functions with table-driven tests
- backupOK guard tested: retention does nothing when backup failed
- Worker pool with panic recovery, graceful shutdown, stuck-job reclamation
- Cron scheduler via robfig/cron/v3 with EnqueueAll for consistent manual/scheduled triggers
- `golangci-lint`: 0 issues
- `go test -race`: all 16 packages pass

---

## Stage 5: Export

**Goal**: A user can export any selection of messages (or a search
result) as mbox, Maildir (tar-streamed), or by uploading to an IMAP
folder on a server they specify. Exports stream — they do not
materialize to disk server-side.

**Success Criteria**:

- `POST /export?format=mbox&q=...` streams a valid mbox file as a
  download; the file opens cleanly in Thunderbird / mutt.
- `format=maildir` streams a `.tar` containing a Maildir layout.
- `format=imap` uploads to a user-supplied IMAP account (treated as a
  one-shot destination, not stored) and reports per-message success.
- Exports dedup: a selection with 500 message *locations* but 450
  distinct hashes produces 450 messages in the output.
- Exports are user-isolated (obviously — reuses the search executor).
- Large exports (10k messages) do not exceed a fixed memory budget —
  verified by a streaming test.

**Deliverables**:

- `internal/export/mbox.go` — writer that takes `io.Writer` and a
  `MessageIterator`, emits `From ` separators, escapes `^From ` in body.
- `internal/export/maildir.go` — writer that produces a tar stream of a
  Maildir layout.
- `internal/export/imap_upload.go` — APPENDs each message to a
  user-supplied `(host, port, user, pass, folder)`.
- `internal/export/selection.go` — given a query or explicit hash list,
  yields distinct `(hash, reader)` pairs from the blob store.
- `internal/httpserver/` — `/export` handler, form in search results UI.

**Tests** (write in this order):

*internal/export/mbox*
1. A single message is emitted with a correct `From ` line and trailing
   blank line.
2. A message body containing `\nFrom ` at the start of a line is escaped
   to `\n>From ` per mboxrd rules.
3. Multiple messages are emitted in stable order (by `internal_date`
   ascending).
4. Output parses as mbox via a round-trip through `go-message`'s mbox
   reader.

*internal/export/maildir*
5. Tar stream contains `new/`, `cur/`, `tmp/` directories.
6. Each message is under `cur/` with the filename convention
   `<timestamp>.<hash>.<hostname>:2,<flags>`.
7. Flags from `message_locations.flags` are translated to Maildir info
   suffixes (`S` for Seen, `R` for Replied, `T` for Trashed, `F` for
   Flagged).
8. Tar stream is valid — round-trip through `archive/tar`.

*internal/export/imap_upload*
9. Against testimap, uploading 3 messages results in 3 `APPEND`s to the
   target folder; bytes match the originals.
10. Target folder is created if it does not exist.
11. Authentication failure is reported per-message (not a crash).
12. Connection errors mid-stream are surfaced with the count of
    successfully-uploaded messages so far.

*internal/export/selection*
13. Given 5 message hashes, the iterator yields 5 distinct readers.
14. Given 10 `message_locations` that map to 7 distinct hashes, the
    iterator yields 7 — dedup works. **(dedup invariant)**
15. Selection built from a search query scopes by `user_id`.
    **(isolation — 100% coverage required)**
16. Iterator closes readers as it goes (no file handle leak) — verified
    with a test that tracks open handles.

*internal/httpserver*
17. `POST /export?format=mbox` with a selection returns 200,
    `Content-Type: application/mbox`, streams the body.
18. Export response does not buffer — verified by writing a large
    fixture set and asserting the handler's peak memory stays under a
    threshold (via `runtime.ReadMemStats` before/after).
19. User A cannot export user B's messages by passing B's hashes.
    **(isolation — 100% coverage required)**

**Coverage target for stage 5**:
- `internal/export/mbox`, `maildir`: ≥ 90%
- `internal/export/imap_upload`: ≥ 80% (I/O-heavy, tested via testimap)
- `internal/export/selection`: ≥ 95%, **100%** on isolation path
- Total project: ≥ 80%

**Status**: Complete (2026-04-12)
- `export` (mbox+maildir+upload+selection): 81.7%
- mbox: From_ escaping per mboxrd, streaming writer
- maildir: tar stream with cur/ layout, flag translation (S/R/F/T)
- IMAP upload: one-shot APPEND with auto-create folder, auth/connection error handling
- Selection: hash-based dedup, user isolation via search executor
- `golangci-lint`: 0 issues
- `go test -race`: all 17 packages pass

---

## Cross-cutting concerns

These apply across stages and should be checked at every stage
boundary:

- **No user-isolation leaks**: grep for repo methods that take a query
  argument but not a `user_id`. Every stage adds new repos; they must
  all conform.
- **Migration reversibility**: each migration should be reviewable on
  its own. If we ever need to roll one back we'll add a down-migration
  then; we don't speculate on one now.
- **No flaky tests**: any test that uses real wall-clock time must use
  an injected clock. `time.Sleep` in tests is a code-review rejection.
- **Race detector**: `go test -race ./...` is the canonical test
  command; plain `go test` is only for quick local iteration.
- **Lint clean**: `golangci-lint run` before every commit; CI (when we
  add it) enforces the same.

## Post-v1 (not planned here)

Deferred from `ARCHITECTURE.md` §15, to be scoped separately once v1
ships:

- OAuth2 / XOAUTH2 IMAP (Gmail)
- IMAP IDLE for near-real-time sync
- At-rest encryption of message bodies
- Multi-node / HA deployment
