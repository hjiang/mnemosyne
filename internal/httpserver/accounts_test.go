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
	orch := backup.NewOrchestrator(acctRepo, msgRepo, store, nil)

	searchExec := search.NewExecutor(database)
	jobQueue := jobs.NewQueue(database, clock.Now)
	srv := New(userRepo, sessions, acctRepo, orch, jobQueue, msgRepo, searchExec, store, nil)

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

func TestAccounts_EditForm(t *testing.T) {
	env := newAcctTestEnv(t)

	acct, err := env.accounts.Create(env.userAID, "My Gmail", "imap.gmail.com", 993, "alice", "pass", true, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}

	rr := env.doRequest(t, "GET", fmt.Sprintf("/accounts/%d/edit", acct.ID), env.cookieA, nil)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "My Gmail") {
		t.Error("expected account label in edit form")
	}
	if !strings.Contains(body, "imap.gmail.com") {
		t.Error("expected host in edit form")
	}
}

// isolation — user A cannot edit user B's account
func TestAccounts_EditForm_CrossUser_404(t *testing.T) {
	env := newAcctTestEnv(t)

	acctB, err := env.accounts.Create(env.userBID, "B's account", "host", 993, "u", "p", true, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}

	rr := env.doRequest(t, "GET", fmt.Sprintf("/accounts/%d/edit", acctB.ID), env.cookieA, nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestAccounts_Update(t *testing.T) {
	env := newAcctTestEnv(t)

	acct, err := env.accounts.Create(env.userAID, "Old", "old.host", 993, "olduser", "oldpass", true, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}

	rr := env.doRequest(t, "POST", fmt.Sprintf("/accounts/%d/edit", acct.ID), env.cookieA, url.Values{
		"label":    {"New Label"},
		"host":     {"new.host.com"},
		"port":     {"143"},
		"username": {"newuser"},
		"password": {"newpass"},
	})
	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if loc := rr.Header().Get("Location"); loc != "/accounts" {
		t.Errorf("Location = %q, want /accounts", loc)
	}

	got, _ := env.accounts.GetByID(acct.ID, env.userAID)
	if got.Label != "New Label" {
		t.Errorf("Label = %q, want %q", got.Label, "New Label")
	}
	if got.Host != "new.host.com" {
		t.Errorf("Host = %q, want %q", got.Host, "new.host.com")
	}
	if got.Password != "newpass" {
		t.Errorf("Password = %q, want %q", got.Password, "newpass")
	}
}

func TestAccounts_Update_KeepsPassword(t *testing.T) {
	env := newAcctTestEnv(t)

	acct, err := env.accounts.Create(env.userAID, "Test", "host", 993, "user", "secret", true, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Submit with empty password field — should keep existing password.
	rr := env.doRequest(t, "POST", fmt.Sprintf("/accounts/%d/edit", acct.ID), env.cookieA, url.Values{
		"label":    {"Test"},
		"host":     {"host"},
		"port":     {"993"},
		"username": {"user"},
		"password": {""},
	})
	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}

	got, _ := env.accounts.GetByID(acct.ID, env.userAID)
	if got.Password != "secret" {
		t.Errorf("Password = %q, want %q (should be preserved)", got.Password, "secret")
	}
}

// isolation — user A cannot update user B's account
func TestAccounts_Update_CrossUser_404(t *testing.T) {
	env := newAcctTestEnv(t)

	acctB, err := env.accounts.Create(env.userBID, "B's account", "host", 993, "u", "p", true, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}

	rr := env.doRequest(t, "POST", fmt.Sprintf("/accounts/%d/edit", acctB.ID), env.cookieA, url.Values{
		"label":    {"Hacked"},
		"host":     {"evil.com"},
		"port":     {"993"},
		"username": {"hacker"},
		"password": {"hacked"},
	})
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}
