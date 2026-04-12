package httpserver

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hjiang/mnemosyne/internal/auth"
	"github.com/hjiang/mnemosyne/internal/db"
	"github.com/hjiang/mnemosyne/internal/search"
	"github.com/hjiang/mnemosyne/internal/users"
)

type searchTestEnv struct {
	server  *Server
	cookieA string
	cookieB string
	userAID int64
	userBID int64
}

func newSearchTestEnv(t *testing.T) *searchTestEnv {
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

	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	userRepo := users.NewRepo(database, clock.Now)
	sessions := auth.NewSessionStore(database, clock.Now, 1*time.Hour)
	searchExec := search.NewExecutor(database)
	srv := New(userRepo, sessions, nil, nil, nil, nil, searchExec, nil)

	hashA, _ := auth.HashPassword("pass")
	uA, _ := userRepo.Create("a@test.com", hashA)
	hashB, _ := auth.HashPassword("pass")
	uB, _ := userRepo.Create("b@test.com", hashB)

	sessA, _ := sessions.Create(uA.ID)
	sessB, _ := sessions.Create(uB.ID)

	// Seed messages for user A.
	database.Exec( //nolint:errcheck,gosec
		`INSERT INTO messages (hash, user_id, message_id, from_addr, to_addrs, subject, date, size, has_attachments, body_text)
		 VALUES (x'01', ?, '', 'alice@test.com', 'bob@test.com', 'Budget Report', 1000, 100, 0, 'quarterly budget')`,
		uA.ID)
	database.Exec( //nolint:errcheck,gosec
		`INSERT INTO messages_fts(rowid, subject, from_addr, to_addrs, cc_addrs, body_text)
		 VALUES ((SELECT rowid FROM messages WHERE hash = x'01'), 'Budget Report', 'alice@test.com', 'bob@test.com', '', 'quarterly budget')`)

	// Seed messages for user B.
	database.Exec( //nolint:errcheck,gosec
		`INSERT INTO messages (hash, user_id, message_id, from_addr, to_addrs, subject, date, size, has_attachments, body_text)
		 VALUES (x'02', ?, '', 'carol@test.com', 'dave@test.com', 'Secret Data', 2000, 100, 0, 'confidential')`,
		uB.ID)
	database.Exec( //nolint:errcheck,gosec
		`INSERT INTO messages_fts(rowid, subject, from_addr, to_addrs, cc_addrs, body_text)
		 VALUES ((SELECT rowid FROM messages WHERE hash = x'02'), 'Secret Data', 'carol@test.com', 'dave@test.com', '', 'confidential')`)

	return &searchTestEnv{
		server:  srv,
		cookieA: hex.EncodeToString(sessA.ID),
		cookieB: hex.EncodeToString(sessB.ID),
		userAID: uA.ID,
		userBID: uB.ID,
	}
}

// Test 32: GET /search?q=budget returns results.
func TestSearch_WithResults(t *testing.T) {
	env := newSearchTestEnv(t)

	req := httptest.NewRequest("GET", "/search?q=budget", nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookieA})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Budget Report") {
		t.Errorf("expected 'Budget Report' in results, body=%s", body)
	}
	if !strings.Contains(body, "1 result") {
		t.Error("expected '1 result' count")
	}
}

// Test 33: Empty query shows hint page.
func TestSearch_EmptyQuery(t *testing.T) {
	env := newSearchTestEnv(t)

	req := httptest.NewRequest("GET", "/search", nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookieA})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "from:") {
		t.Error("expected usage hint in response")
	}
}

// Test 34: User A searching for user B's content gets 0 results.
func TestSearch_UserIsolation(t *testing.T) {
	env := newSearchTestEnv(t)

	// User A searches for B's content.
	req := httptest.NewRequest("GET", "/search?q=confidential", nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookieA})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if strings.Contains(body, "Secret Data") {
		t.Error("user A should not see user B's messages")
	}
}
