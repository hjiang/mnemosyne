package httpserver

import (
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hjiang/mnemosyne/internal/accounts"
	"github.com/hjiang/mnemosyne/internal/auth"
	"github.com/hjiang/mnemosyne/internal/backup"
	"github.com/hjiang/mnemosyne/internal/blobs"
	"github.com/hjiang/mnemosyne/internal/db"
	"github.com/hjiang/mnemosyne/internal/jobs"
	"github.com/hjiang/mnemosyne/internal/messages"
	"github.com/hjiang/mnemosyne/internal/search"
	"github.com/hjiang/mnemosyne/internal/users"
)

type acctTestEnv struct {
	server   *Server
	accounts *accounts.Repo
	sessions *auth.SessionStore
	cookieA  string
	cookieB  string
	userAID  int64
	userBID  int64
}

func newAcctTestEnv(t *testing.T) *acctTestEnv {
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

	km, err := accounts.NewKeyManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	userRepo := users.NewRepo(database, clock.Now)
	sessions := auth.NewSessionStore(database, clock.Now, 1*time.Hour)
	acctRepo := accounts.NewRepo(database, km)
	msgRepo := messages.NewRepo(database)
	store := blobs.NewStore(filepath.Join(dir, "blobs"))
	orch := backup.NewOrchestrator(acctRepo, msgRepo, store)

	searchExec := search.NewExecutor(database)
	jobQueue := jobs.NewQueue(database, clock.Now)
	srv := New(userRepo, sessions, acctRepo, orch, jobQueue, msgRepo, searchExec, store)

	hashA, _ := auth.HashPassword("pass")
	uA, _ := userRepo.Create("a@test.com", hashA)
	hashB, _ := auth.HashPassword("pass")
	uB, _ := userRepo.Create("b@test.com", hashB)

	sessA, _ := sessions.Create(uA.ID)
	sessB, _ := sessions.Create(uB.ID)

	return &acctTestEnv{
		server:   srv,
		accounts: acctRepo,
		sessions: sessions,
		cookieA:  hex.EncodeToString(sessA.ID),
		cookieB:  hex.EncodeToString(sessB.ID),
		userAID:  uA.ID,
		userBID:  uB.ID,
	}
}

func (e *acctTestEnv) doRequest(t *testing.T, method, path string, cookie string, body url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var bodyReader *strings.Reader
	if body != nil {
		bodyReader = strings.NewReader(body.Encode())
	} else {
		bodyReader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if body != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: cookie})
	}
	rr := httptest.NewRecorder()
	e.server.ServeHTTP(rr, req)
	return rr
}

// Test 34: Unauthenticated POST /accounts redirects to login.
func TestAccounts_Unauthenticated_Redirects(t *testing.T) {
	env := newAcctTestEnv(t)

	rr := env.doRequest(t, "POST", "/accounts", "", url.Values{"label": {"Test"}})
	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if loc := rr.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

// Test 35: User A cannot see user B's account folders.
func TestAccounts_CrossUserFolders_404(t *testing.T) {
	env := newAcctTestEnv(t)

	// Create account for user B.
	acctB, err := env.accounts.Create(env.userBID, "B's account", "host", 993, "u", "p", true, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}

	// User A tries to access B's folders.
	rr := env.doRequest(t, "GET", fmt.Sprintf("/accounts/%d/folders", acctB.ID), env.cookieA, nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

// Test 36: POST /accounts/{id}/backup enqueues a backup job.
func TestAccounts_BackupRun(t *testing.T) {
	env := newAcctTestEnv(t)

	acct, err := env.accounts.Create(env.userAID, "Test", "host", 993, "u", "p", true, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}

	rr := env.doRequest(t, "POST", fmt.Sprintf("/accounts/%d/backup", acct.ID), env.cookieA, nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "enqueued") {
		t.Errorf("expected 'enqueued' in body, got: %s", body)
	}
}

// Test 37: Folder toggle persists.
func TestAccounts_FolderToggle(t *testing.T) {
	env := newAcctTestEnv(t)

	acct, _ := env.accounts.Create(env.userAID, "Test", "host", 993, "u", "p", true, "", 0, "", "")
	folder, _ := env.accounts.CreateFolder(acct.ID, "INBOX")

	// Toggle on.
	rr := env.doRequest(t, "POST",
		fmt.Sprintf("/accounts/%d/folders/%d/toggle", acct.ID, folder.ID),
		env.cookieA,
		url.Values{"enabled": {"on"}},
	)
	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}

	folders, _ := env.accounts.ListFolders(acct.ID)
	if len(folders) != 1 || !folders[0].Enabled {
		t.Error("expected folder to be enabled after toggle")
	}

	// Toggle off (no 'enabled' field).
	rr = env.doRequest(t, "POST",
		fmt.Sprintf("/accounts/%d/folders/%d/toggle", acct.ID, folder.ID),
		env.cookieA,
		url.Values{},
	)
	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}

	folders, _ = env.accounts.ListFolders(acct.ID)
	if len(folders) != 1 || folders[0].Enabled {
		t.Error("expected folder to be disabled after toggle off")
	}
}
