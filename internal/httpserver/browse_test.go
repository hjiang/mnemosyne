package httpserver

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hjiang/mnemosyne/internal/accounts"
	"github.com/hjiang/mnemosyne/internal/auth"
	"github.com/hjiang/mnemosyne/internal/blobs"
	"github.com/hjiang/mnemosyne/internal/db"
	"github.com/hjiang/mnemosyne/internal/messages"
	"github.com/hjiang/mnemosyne/internal/search"
	"github.com/hjiang/mnemosyne/internal/users"
)

type browseTestEnv struct {
	server   *Server
	accounts *accounts.Repo
	messages *messages.Repo
	cookieA  string
	cookieB  string
	userAID  int64
	userBID  int64
}

func newBrowseTestEnv(t *testing.T) *browseTestEnv {
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
	searchExec := search.NewExecutor(database)
	srv := New(userRepo, sessions, acctRepo, nil, nil, msgRepo, searchExec, store, nil)

	hashA, err := auth.HashPasswordForTesting("pass")
	if err != nil {
		t.Fatal(err)
	}
	uA, err := userRepo.Create("a@test.com", hashA)
	if err != nil {
		t.Fatal(err)
	}
	hashB, err := auth.HashPasswordForTesting("pass")
	if err != nil {
		t.Fatal(err)
	}
	uB, err := userRepo.Create("b@test.com", hashB)
	if err != nil {
		t.Fatal(err)
	}
	sessA, err := sessions.Create(uA.ID)
	if err != nil {
		t.Fatal(err)
	}
	sessB, err := sessions.Create(uB.ID)
	if err != nil {
		t.Fatal(err)
	}

	return &browseTestEnv{
		server:   srv,
		accounts: acctRepo,
		messages: msgRepo,
		cookieA:  hex.EncodeToString(sessA.ID),
		cookieB:  hex.EncodeToString(sessB.ID),
		userAID:  uA.ID,
		userBID:  uB.ID,
	}
}

func seedMessage(t *testing.T, repo *messages.Repo, userID, folderID int64, subject, from string, uid uint32) []byte {
	t.Helper()
	hash := sha256.Sum256([]byte(fmt.Sprintf("%d-%d-%s", userID, uid, subject)))
	date := int64(1700000000)
	if err := repo.Insert(&messages.Message{
		Hash: hash[:], UserID: userID, Subject: subject, FromAddr: from,
		Date: &date, Size: 100,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertLocation(&messages.Location{
		MessageHash: hash[:], FolderID: folderID, UID: uid, InternalDate: &date,
	}); err != nil {
		t.Fatal(err)
	}
	return hash[:]
}

func TestBrowse_NoFolder_RendersSidebar(t *testing.T) {
	env := newBrowseTestEnv(t)

	acct, err := env.accounts.Create(env.userAID, "Gmail", "h", 993, "u", "p", true, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	folder, err := env.accounts.CreateFolder(acct.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	seedMessage(t, env.messages, env.userAID, folder.ID, "Hello", "alice@x.com", 1)

	req := httptest.NewRequest("GET", "/browse", nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookieA})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Gmail") {
		t.Error("expected account label in sidebar")
	}
	if !strings.Contains(body, "INBOX") {
		t.Error("expected folder name in sidebar")
	}
}

func TestBrowse_FolderMessages_Rendered(t *testing.T) {
	env := newBrowseTestEnv(t)

	acct, err := env.accounts.Create(env.userAID, "Gmail", "h", 993, "u", "p", true, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	folder, err := env.accounts.CreateFolder(acct.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	seedMessage(t, env.messages, env.userAID, folder.ID, "Hello world", "alice@x.com", 1)

	req := httptest.NewRequest("GET", fmt.Sprintf("/browse/%d", folder.ID), nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookieA})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "Hello world") {
		t.Error("expected message subject in body")
	}
}

// accountExpanded reports whether the sidebar <details> wrapping the account
// with the given label was rendered open.
func accountExpanded(body, label string) bool {
	marker := "browse-account-header\">" + label
	li := strings.Index(body, marker)
	if li < 0 {
		return false
	}
	di := strings.LastIndex(body[:li], "<details")
	if di < 0 {
		return false
	}
	return strings.Contains(body[di:li], "open>")
}

func TestBrowse_OnlyActiveAccountExpanded(t *testing.T) {
	env := newBrowseTestEnv(t)

	acct1, err := env.accounts.Create(env.userAID, "AcctOne", "h", 993, "u", "p", true, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.accounts.CreateFolder(acct1.ID, "INBOX"); err != nil {
		t.Fatal(err)
	}
	acct2, err := env.accounts.Create(env.userAID, "AcctTwo", "h", 993, "u", "p", true, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	folder2, err := env.accounts.CreateFolder(acct2.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	seedMessage(t, env.messages, env.userAID, folder2.ID, "Hi", "x@x.com", 1)

	req := httptest.NewRequest("GET", fmt.Sprintf("/browse/%d", folder2.ID), nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookieA})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if got := strings.Count(body, "<details open>"); got != 1 {
		t.Errorf("expected exactly 1 expanded account, got %d", got)
	}
	if !accountExpanded(body, "AcctTwo") {
		t.Error("account containing the active folder should be expanded")
	}
	if accountExpanded(body, "AcctOne") {
		t.Error("account without the active folder should be collapsed")
	}
}

func TestBrowse_NoFolder_FirstAccountExpanded(t *testing.T) {
	env := newBrowseTestEnv(t)

	acct1, err := env.accounts.Create(env.userAID, "AcctOne", "h", 993, "u", "p", true, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.accounts.CreateFolder(acct1.ID, "INBOX"); err != nil {
		t.Fatal(err)
	}
	acct2, err := env.accounts.Create(env.userAID, "AcctTwo", "h", 993, "u", "p", true, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := env.accounts.CreateFolder(acct2.ID, "INBOX"); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/browse", nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookieA})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if got := strings.Count(rr.Body.String(), "<details open>"); got != 1 {
		t.Errorf("expected exactly 1 expanded account with no folder selected, got %d", got)
	}
}

func TestBrowse_CrossUserFolder_404(t *testing.T) {
	env := newBrowseTestEnv(t)

	acctB, err := env.accounts.Create(env.userBID, "B", "h", 993, "u", "p", true, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}
	folderB, err := env.accounts.CreateFolder(acctB.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	seedMessage(t, env.messages, env.userBID, folderB.ID, "secret", "b@x.com", 1)

	req := httptest.NewRequest("GET", fmt.Sprintf("/browse/%d", folderB.ID), nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookieA})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (cross-user isolation)", rr.Code, http.StatusNotFound)
	}
}

func TestBrowse_InvalidFolderID_404(t *testing.T) {
	env := newBrowseTestEnv(t)

	req := httptest.NewRequest("GET", "/browse/notanumber", nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookieA})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}
