package search

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/hjiang/mnemosyne/internal/db"
)

type execTestEnv struct {
	db       *sql.DB
	executor *Executor
}

func newExecTestEnv(t *testing.T) *execTestEnv {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}

	return &execTestEnv{
		db:       database,
		executor: NewExecutor(database),
	}
}

func seedUser(t *testing.T, database *sql.DB, email string) int64 {
	t.Helper()
	res, err := database.Exec(
		"INSERT INTO users (email, password_hash, created_at) VALUES (?, 'h', 0)", email)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func seedMessage(t *testing.T, database *sql.DB, userID int64, hash []byte, from, to, cc, subject, bodyText string, date int64, hasAtt bool) {
	t.Helper()
	attInt := 0
	if hasAtt {
		attInt = 1
	}
	_, err := database.Exec(
		`INSERT INTO messages (hash, user_id, message_id, from_addr, to_addrs, cc_addrs, subject, date, size, has_attachments, body_text)
		 VALUES (?, ?, '', ?, ?, ?, ?, ?, 100, ?, ?)`,
		hash, userID, from, to, cc, subject, date, attInt, bodyText)
	if err != nil {
		t.Fatal(err)
	}
}

func indexMessage(t *testing.T, database *sql.DB, rowid int64, subject, from, to, cc, bodyText string) {
	t.Helper()
	_, err := database.Exec(
		"INSERT INTO messages_fts(rowid, subject, from_addr, to_addrs, cc_addrs, body_text) VALUES (?, ?, ?, ?, ?, ?)",
		rowid, subject, from, to, cc, bodyText)
	if err != nil {
		t.Fatal(err)
	}
}

// Test 12: Free text search via FTS5.
func TestExecutor_FreeText(t *testing.T) {
	env := newExecTestEnv(t)
	uid := seedUser(t, env.db, "a@test.com")
	seedMessage(t, env.db, uid, []byte{1}, "alice@test.com", "bob@test.com", "", "Budget Report", "quarterly budget overview", 1000, false)
	indexMessage(t, env.db, 1, "Budget Report", "alice@test.com", "bob@test.com", "", "quarterly budget overview")

	seedMessage(t, env.db, uid, []byte{2}, "carol@test.com", "bob@test.com", "", "Other Topic", "unrelated content", 2000, false)
	indexMessage(t, env.db, 2, "Other Topic", "carol@test.com", "bob@test.com", "", "unrelated content")

	q := &Query{Text: []string{"budget"}}
	results, err := env.executor.Search(q, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if results[0].Subject != "Budget Report" {
		t.Errorf("subject = %q, want Budget Report", results[0].Subject)
	}
}

// Test 13: From operator with LIKE.
func TestExecutor_FromOperator(t *testing.T) {
	env := newExecTestEnv(t)
	uid := seedUser(t, env.db, "a@test.com")
	seedMessage(t, env.db, uid, []byte{1}, "alice@test.com", "", "", "From Alice", "", 1000, false)
	seedMessage(t, env.db, uid, []byte{2}, "bob@test.com", "", "", "From Bob", "", 2000, false)

	q := &Query{From: "alice"}
	results, err := env.executor.Search(q, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Subject != "From Alice" {
		t.Errorf("results = %v, want 1 result with 'From Alice'", results)
	}
}

// Test 14: Combined from + free text.
func TestExecutor_CombinedFromAndText(t *testing.T) {
	env := newExecTestEnv(t)
	uid := seedUser(t, env.db, "a@test.com")
	seedMessage(t, env.db, uid, []byte{1}, "alice@test.com", "", "", "Budget", "budget info", 1000, false)
	indexMessage(t, env.db, 1, "Budget", "alice@test.com", "", "", "budget info")

	seedMessage(t, env.db, uid, []byte{2}, "alice@test.com", "", "", "Vacation", "vacation plans", 2000, false)
	indexMessage(t, env.db, 2, "Vacation", "alice@test.com", "", "", "vacation plans")

	seedMessage(t, env.db, uid, []byte{3}, "bob@test.com", "", "", "Budget", "budget stuff", 3000, false)
	indexMessage(t, env.db, 3, "Budget", "bob@test.com", "", "", "budget stuff")

	q := &Query{From: "alice", Text: []string{"budget"}}
	results, err := env.executor.Search(q, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Subject != "Budget" || results[0].FromAddr != "alice@test.com" {
		t.Errorf("results = %v, want 1 result from alice with 'Budget'", results)
	}
}

// Test 15: has:attachment filter.
func TestExecutor_HasAttachment(t *testing.T) {
	env := newExecTestEnv(t)
	uid := seedUser(t, env.db, "a@test.com")
	seedMessage(t, env.db, uid, []byte{1}, "alice@test.com", "", "", "With Att", "", 1000, true)
	seedMessage(t, env.db, uid, []byte{2}, "alice@test.com", "", "", "No Att", "", 2000, false)

	q := &Query{HasAttachment: true}
	results, err := env.executor.Search(q, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Subject != "With Att" {
		t.Errorf("results = %v, want 1 result 'With Att'", results)
	}
}

// Test 16: before: date filter.
func TestExecutor_BeforeDate(t *testing.T) {
	env := newExecTestEnv(t)
	uid := seedUser(t, env.db, "a@test.com")
	seedMessage(t, env.db, uid, []byte{1}, "", "", "", "Old", "", 1000, false)   // epoch + 1000s
	seedMessage(t, env.db, uid, []byte{2}, "", "", "", "New", "", 999999, false) // much later

	before := time.Unix(2000, 0)
	q := &Query{Before: &before}
	results, err := env.executor.Search(q, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || results[0].Subject != "Old" {
		t.Errorf("results = %v, want 1 result 'Old'", results)
	}
}

// Test 17: User isolation — user A cannot see user B's messages.
func TestExecutor_UserIsolation(t *testing.T) {
	env := newExecTestEnv(t)
	uidA := seedUser(t, env.db, "a@test.com")
	uidB := seedUser(t, env.db, "b@test.com")

	seedMessage(t, env.db, uidA, []byte{1}, "alice@test.com", "", "", "Secret A", "secret data", 1000, false)
	indexMessage(t, env.db, 1, "Secret A", "alice@test.com", "", "", "secret data")

	seedMessage(t, env.db, uidB, []byte{2}, "bob@test.com", "", "", "Secret B", "secret data", 2000, false)
	indexMessage(t, env.db, 2, "Secret B", "bob@test.com", "", "", "secret data")

	// User A searches — should only see their own.
	q := &Query{Text: []string{"secret"}}
	resultsA, err := env.executor.Search(q, uidA)
	if err != nil {
		t.Fatal(err)
	}
	if len(resultsA) != 1 || resultsA[0].Subject != "Secret A" {
		t.Errorf("user A results = %v, want 1 result 'Secret A'", resultsA)
	}

	// User B searches — should only see their own.
	resultsB, err := env.executor.Search(q, uidB)
	if err != nil {
		t.Fatal(err)
	}
	if len(resultsB) != 1 || resultsB[0].Subject != "Secret B" {
		t.Errorf("user B results = %v, want 1 result 'Secret B'", resultsB)
	}

	// Cross-user: user A searching with B's content keywords finds nothing of B's.
	qFrom := &Query{From: "bob"}
	crossResults, err := env.executor.Search(qFrom, uidA)
	if err != nil {
		t.Fatal(err)
	}
	if len(crossResults) != 0 {
		t.Errorf("cross-user results = %d, want 0", len(crossResults))
	}
}

// Test 18: FTS escaping — SQL injection attempt.
func TestExecutor_FTSEscaping(t *testing.T) {
	env := newExecTestEnv(t)
	uid := seedUser(t, env.db, "a@test.com")
	seedMessage(t, env.db, uid, []byte{1}, "", "", "", "Normal", "hello world", 1000, false)
	indexMessage(t, env.db, 1, "Normal", "", "", "", "hello world")

	// This should not cause a SQL error or return unexpected results.
	q := &Query{Text: []string{"'; DROP TABLE messages; --"}}
	results, err := env.executor.Search(q, uid)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("injection attempt returned %d results, want 0", len(results))
	}
}
