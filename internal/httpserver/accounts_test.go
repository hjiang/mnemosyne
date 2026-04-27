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

	hashA, _ := auth.HashPasswordForTesting("pass")
	uA, _ := userRepo.Create("a@test.com", hashA)
	hashB, _ := auth.HashPasswordForTesting("pass")
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
func TestAccounts_FolderRefresh_ShowsError(t *testing.T) {
	env := newAcctTestEnv(t)

	acct, _ := env.accounts.Create(env.userAID, "Test", "host", 993, "u", "p", true, "", 0, "", "")
	env.accounts.CreateFolder(acct.ID, "INBOX") //nolint:errcheck,gosec

	// The IMAP server is unreachable, so the handler should render the
	// folders page with an error message instead of redirecting.
	rr := env.doRequest(t, "POST",
		fmt.Sprintf("/accounts/%d/folders/refresh", acct.ID),
		env.cookieA,
		nil,
	)
	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Folder refresh failed") {
		t.Error("expected error message in response body")
	}
}

func TestAccounts_FolderRefresh_CrossUser_404(t *testing.T) {
	env := newAcctTestEnv(t)

	acctB, _ := env.accounts.Create(env.userBID, "B's", "host", 993, "u", "p", true, "", 0, "", "")

	rr := env.doRequest(t, "POST",
		fmt.Sprintf("/accounts/%d/folders/refresh", acctB.ID),
		env.cookieA,
		nil,
	)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestAccounts_FolderPolicy_NewestN(t *testing.T) {
	env := newAcctTestEnv(t)

	acct, _ := env.accounts.Create(env.userAID, "Test", "h", 993, "u", "p", true, "", 0, "", "")
	folder, _ := env.accounts.CreateFolder(acct.ID, "INBOX")

	rr := env.doRequest(t, "POST",
		fmt.Sprintf("/accounts/%d/folders/%d/policy", acct.ID, folder.ID),
		env.cookieA,
		url.Values{"policy_type": {"newest_n"}, "policy_n": {"50"}},
	)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}

	folders, _ := env.accounts.ListFolders(acct.ID)
	if !strings.Contains(folders[0].PolicyJSON, `"leave_on_server":"newest_n"`) {
		t.Errorf("PolicyJSON = %q, expected newest_n", folders[0].PolicyJSON)
	}
	if !strings.Contains(folders[0].PolicyJSON, `"n":50`) {
		t.Errorf("PolicyJSON = %q, expected n=50", folders[0].PolicyJSON)
	}
}

func TestAccounts_FolderPolicy_InvalidN_400(t *testing.T) {
	env := newAcctTestEnv(t)

	acct, _ := env.accounts.Create(env.userAID, "Test", "h", 993, "u", "p", true, "", 0, "", "")
	folder, _ := env.accounts.CreateFolder(acct.ID, "INBOX")

	rr := env.doRequest(t, "POST",
		fmt.Sprintf("/accounts/%d/folders/%d/policy", acct.ID, folder.ID),
		env.cookieA,
		url.Values{"policy_type": {"newest_n"}, "policy_n": {"0"}},
	)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestAccounts_FolderPolicy_UnknownType_400(t *testing.T) {
	env := newAcctTestEnv(t)

	acct, _ := env.accounts.Create(env.userAID, "Test", "h", 993, "u", "p", true, "", 0, "", "")
	folder, _ := env.accounts.CreateFolder(acct.ID, "INBOX")

	rr := env.doRequest(t, "POST",
		fmt.Sprintf("/accounts/%d/folders/%d/policy", acct.ID, folder.ID),
		env.cookieA,
		url.Values{"policy_type": {"bogus"}},
	)
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestAccounts_FolderPolicy_CrossUser_404(t *testing.T) {
	env := newAcctTestEnv(t)

	acctB, _ := env.accounts.Create(env.userBID, "B", "h", 993, "u", "p", true, "", 0, "", "")
	folderB, _ := env.accounts.CreateFolder(acctB.ID, "INBOX")

	rr := env.doRequest(t, "POST",
		fmt.Sprintf("/accounts/%d/folders/%d/policy", acctB.ID, folderB.ID),
		env.cookieA,
		url.Values{"policy_type": {"all"}},
	)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (cross-user)", rr.Code, http.StatusNotFound)
	}
}

func TestAccounts_FolderResync_ResetsLastSeenUID(t *testing.T) {
	env := newAcctTestEnv(t)

	acct, _ := env.accounts.Create(env.userAID, "Test", "h", 993, "u", "p", true, "", 0, "", "")
	folder, _ := env.accounts.CreateFolder(acct.ID, "INBOX")
	if err := env.accounts.SetLastSeenUID(folder.ID, 42); err != nil {
		t.Fatal(err)
	}

	rr := env.doRequest(t, "POST",
		fmt.Sprintf("/accounts/%d/folders/%d/resync", acct.ID, folder.ID),
		env.cookieA, nil,
	)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}

	folders, _ := env.accounts.ListFolders(acct.ID)
	if folders[0].LastSeenUID != 0 {
		t.Errorf("LastSeenUID = %d, want 0", folders[0].LastSeenUID)
	}
}

func TestAccounts_FolderResync_CrossUser_404(t *testing.T) {
	env := newAcctTestEnv(t)

	acctB, _ := env.accounts.Create(env.userBID, "B", "h", 993, "u", "p", true, "", 0, "", "")
	folderB, _ := env.accounts.CreateFolder(acctB.ID, "INBOX")

	rr := env.doRequest(t, "POST",
		fmt.Sprintf("/accounts/%d/folders/%d/resync", acctB.ID, folderB.ID),
		env.cookieA, nil,
	)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestAccounts_Create_DiscoveryFails_RollsBack(t *testing.T) {
	env := newAcctTestEnv(t)

	// Override the IMAP-discovery seam so we never touch the network.
	env.server.discoverFolders = func(_ *accounts.Account) error {
		return fmt.Errorf("simulated dial failure")
	}

	rr := env.doRequest(t, "POST", "/accounts", env.cookieA, url.Values{
		"label":    {"Bad"},
		"host":     {"unreachable"},
		"port":     {"993"},
		"username": {"u"},
		"password": {"p"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (re-rendered accounts page with error)", rr.Code, http.StatusOK)
	}
	if !strings.Contains(rr.Body.String(), "IMAP connection failed") {
		t.Error("expected user-facing error on accounts page")
	}

	// Account row must have been rolled back.
	accts, _ := env.accounts.List(env.userAID)
	if len(accts) != 0 {
		t.Errorf("expected 0 accounts after rollback, got %d", len(accts))
	}
}

func TestAccounts_Create_Success(t *testing.T) {
	env := newAcctTestEnv(t)

	var called bool
	env.server.discoverFolders = func(acct *accounts.Account) error {
		called = true
		_, _ = env.accounts.CreateFolder(acct.ID, "INBOX")
		return nil
	}

	rr := env.doRequest(t, "POST", "/accounts", env.cookieA, url.Values{
		"label":    {"OK"},
		"host":     {"imap.x.com"},
		"port":     {"993"},
		"username": {"u"},
		"password": {"p"},
		"use_tls":  {"on"},
	})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if !called {
		t.Error("discoverFolders was not invoked")
	}
	loc := rr.Header().Get("Location")
	if !strings.HasPrefix(loc, "/accounts/") || !strings.HasSuffix(loc, "/folders") {
		t.Errorf("Location = %q, want /accounts/{id}/folders", loc)
	}
}

func TestAccounts_Create_InvalidPort_RendersError(t *testing.T) {
	env := newAcctTestEnv(t)

	rr := env.doRequest(t, "POST", "/accounts", env.cookieA, url.Values{
		"label":    {"X"},
		"host":     {"h"},
		"port":     {"notanumber"},
		"username": {"u"},
		"password": {"p"},
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if !strings.Contains(rr.Body.String(), "Invalid port") {
		t.Error("expected 'Invalid port' error in response")
	}
}

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
