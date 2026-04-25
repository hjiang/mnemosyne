# Plan: Robust Mass Deletion Over Unstable Connections

## Context

After a one-shot backup of a folder containing 200K+ emails, applying a
retention policy (e.g. `younger_than: 30d` against a 10-year archive) produces
a Wave A deletion set of tens or hundreds of thousands of UIDs. Today the
orchestrator processes that set with one `STORE` round-trip per UID and only
batches `EXPUNGE` (default 50). On a flaky upstream this fails repeatedly,
makes little net progress per attempt, and the existing retry loop eventually
gives up because re-marking already-flagged messages doesn't increment
`NewDeletions`.

This plan makes mass deletion resumable, cheap on round-trips, and
progress-aware so the existing retry-while-progressing loop continues to do
the right thing.

## Symptoms / Root Causes

Located in `internal/backup/orchestrator.go` and `internal/backup/imap/client.go`:

1. **Per-UID `STORE` round-trips.** `orchestrator.go:359` and `:447` call
   `client.MarkDeleted([]uint32{uid})` one UID at a time. For 200K UIDs that's
   200K round-trips — each a chance for the connection to drop. `MarkDeleted`
   already takes a slice; the call sites under-use it.
2. **Random Wave-A iteration order.** `for uid := range expungeSet`
   (`orchestrator.go:355`) iterates a Go map; order changes every retry, so
   resumption can't skip prior work and progress accounting is noisy.
3. **No checkpoint of progress.** `LastSeenUID` checkpoints fetch progress
   but nothing checkpoints "Wave A processed up to UID X". After a drop, the
   next attempt re-marks already-`\Deleted` UIDs (idempotent but slow) and,
   crucially, `NewDeletions` only increments after `EXPUNGE` succeeds — so a
   drop between marking and expunging counts as **zero progress** and the
   retry loop gives up via the no-progress branch (`orchestrator.go:226`).
4. **`backedUp` and `existingLocs` loaded fully into memory each retry**
   (`orchestrator.go:347`). 200K rows × {uid, hash, ...} per retry attempt is
   wasteful; matters less than (1) but worth fixing while we're here.
5. **Wave A wraps every error in `connError`** (`orchestrator.go:376`). A
   genuine server-side error (e.g., quota, permission) gets retried as if
   transient until the no-progress guard trips. We should classify.
6. **No NOOP/keepalive between long batches.** Some servers (Gmail in
   particular) drop idle-ish connections that are stuck inside a multi-minute
   `EXPUNGE`.

## Goals

- Cut Wave A round-trip count by ~3 orders of magnitude (per-UID → batched).
- Make Wave A resumable across connection drops without redoing prior work.
- Ensure every successful server-side state change increments
  `result.NewDeletions` so the retry loop sees forward progress.
- Don't regress small-folder behavior or the existing crash-safe ordering.

## Non-Goals

- Wave B (delete-on-fetch) per-message marks: it interleaves with body fetch
  and is naturally bounded by fetch batch size; we'll batch it lightly but
  not introduce checkpointing there.
- Concurrent deletions / parallel connections — one connection per account
  remains the contract.
- UID EXPUNGE (RFC 4315) — useful but a separate optimization; keep mailbox-
  wide `EXPUNGE` for now and revisit if needed.

## Stages

### Stage 1: Batch `MarkDeleted` aligned with EXPUNGE cadence

**Goal**: Replace per-UID STORE with set-based STORE, using the same batch
boundary as EXPUNGE so every cycle is a single atomic checkpoint.

**Changes**
- `internal/backup/orchestrator.go`: in the Wave A loop
  (`orchestrator.go:355–372`), build batches of size `expungeBatchSize`
  (default 50) and for each batch call:
  1. `client.MarkDeleted(batch)` — one round-trip, set-based STORE.
  2. `client.Expunge()` — one round-trip.
  3. Update the cursor (Stage 2) and progress counter (Stage 3).
- One knob (`expungeBatchSize`) controls both, removing a degree of freedom
  and the "marked-but-not-expunged" intermediate state.
- Reuse the existing `MarkDeleted([]uint32)` signature — only the call sites
  change. Wave B (`orchestrator.go:447`) is left as-is for now (one UID per
  call there is bounded by fetch batch size).

**Tests** (in `orchestrator_test.go` and a new fake-client check)
- A mock client records call counts; assert that 500 UIDs in the expunge set
  produce ~10 `MarkDeleted` calls (500/50), not 500.
- Existing Wave A tests still pass with batched calls.

**Success Criteria**
- All existing tests green.
- New round-trip-count test passes.
- `golangci-lint run ./...` clean.

### Stage 2: Deterministic order + checkpoint

**Goal**: Make Wave A resumable.

**Changes**
- Sort the Wave A UID list ascending before processing.
- Add a column `wave_a_cursor INTEGER NOT NULL DEFAULT 0` to the `folders`
  table via a new migration `0008_wave_a_cursor.sql`.
- New repo methods on the folders/accounts repo:
  `SetWaveACursor(folderID, uid int64) error` and reading via the existing
  folder load (add field to the struct).
- After each successful **MarkDeleted + EXPUNGE pair**, persist the highest
  UID in the batch as the cursor. Because the batch boundaries are aligned,
  the cursor never points into a half-completed state.
- On entering Wave A, drop UIDs `<= cursor` from the to-mark list. Reset the
  cursor to 0 once the entire Wave A set is exhausted (so the next sync
  starts fresh).
- Reset cursor on UIDVALIDITY change (alongside the existing
  `DeleteLocationsByFolder` reset path at `orchestrator.go:275`).

**Why this works**: marks are idempotent on the server, but skipping them
saves the round-trip and — more importantly — gives the retry loop a way to
observe progress between attempts even if the EXPUNGE never lands.

**Tests**
- Inject a client that fails after N marks; assert cursor advances; on retry,
  assert no UIDs `<= cursor` are re-marked.
- UIDVALIDITY change clears cursor.
- Cursor cleared after full Wave A success.

### Stage 3: Progress accounting

**Goal**: Confirm `NewDeletions` semantics still match reality after the
batch alignment.

**Changes**
- With Stage 1's aligned batches, `NewDeletions` already increments after
  each successful mark+expunge pair — no semantic change needed.
- Verify the retry loop at `orchestrator.go:227–229` still sees forward
  progress correctly: each completed batch ticks `NewDeletions` by
  `len(batch)`. The cursor (Stage 2) also implicitly records progress, so
  even a connError mid-batch leaves a durable trace.
- This stage is now mostly a sanity check + comment update at
  `orchestrator.go:46`.

### Stage 4: Stream `backedUp` lookup instead of loading everything

**Goal**: Avoid loading 200K location rows per retry.

**Changes**
- Replace `ListLocationsByFolder` + map build (`orchestrator.go:347–351`)
  with `LocationExistsByFolderAndUID` calls inside the Wave A loop, OR add a
  new repo method `FilterBackedUpUIDs(folderID, uids []uint32) ([]uint32, error)`
  that does one `WHERE folder_id = ? AND uid IN (...)` per batch.
- The batched filter approach matches Stage 1's batch boundary cleanly: one
  SQL round-trip + one IMAP STORE + one IMAP EXPUNGE per 50 UIDs.

**Tests**
- Repo test for `FilterBackedUpUIDs` covering: empty input, all-present,
  mixed, none-present, user isolation (folder belongs to another user's
  account → returns nothing).

### Stage 5: Distinguish transient from permanent Wave A errors

**Goal**: Don't burn the no-progress budget on permanent server errors.

**Changes**
- In Wave A, only wrap in `connError` when the error matches the same
  classifier used elsewhere (network error, EOF, broken pipe, timeouts).
  Permanent errors (e.g., `NO`, `BAD`) bubble up as plain errors → fall
  through to the non-connError branch at `orchestrator.go:222` and stop
  retrying that folder, but continue to the next folder.
- Audit current classifier (search for where `connError` is constructed
  today) and reuse it; if there isn't a single helper, introduce
  `isTransient(err) bool` in `orchestrator.go`.

**Tests**
- Fake client returning a synthetic `BAD` response → orchestrator records
  the error and moves on, does not retry.
- Fake client returning `io.EOF` → orchestrator reconnects and retries.

### Stage 6 (optional): NOOP keepalive and tunable batch sizes via config

**Goal**: Make batch sizes operator-tunable for servers that misbehave.

**Changes**
- Expose `mark_batch_size`, `expunge_batch_size` under a `backup:` section in
  config (`internal/config`).
- Optionally call `client.Noop()` between very large EXPUNGE batches if we
  observe Gmail dropping. Defer until empirical evidence.

## Rollout

- Land Stages 1–5 in order, each as its own commit with passing tests +
  `golangci-lint`.
- Manually verify against the user's 200K-message folder: expect Wave A to
  complete in O(N/1000) round-trips instead of O(N), and to resume after
  forced disconnects without redoing prior work.
- Update `docs/ARCHITECTURE.md` (the Wave A bullet) and the `CLAUDE.md`
  retention/deletion description after Stage 3 lands (semantics change).

## Risks

- **Server-side STORE size limits.** Some IMAP servers reject very large UID
  sets in a single command. At 50 UIDs/batch this is a non-issue — every
  mainstream server handles 50-element UID sets without complaint.
- **Cursor migration on existing folders.** The cursor defaults to 0, which
  is exactly the "start from the beginning" semantic — no special handling
  needed for in-flight installations.
- **Counter rename.** `NewDeletions` is part of the `Result` struct; check
  for callers in the HTTP/jobs layers before renaming. Splitting into
  `NewMarks` + `NewExpunges` keeps `NewDeletions` available as an alias if
  needed.
