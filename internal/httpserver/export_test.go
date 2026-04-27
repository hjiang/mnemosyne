package httpserver

import (
	"bytes"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hjiang/mnemosyne/internal/auth"
	"github.com/hjiang/mnemosyne/internal/blobs"
	"github.com/hjiang/mnemosyne/internal/db"
	"github.com/hjiang/mnemosyne/internal/messages"
	"github.com/hjiang/mnemosyne/internal/search"
	"github.com/hjiang/mnemosyne/internal/users"
)

type exportTestEnv struct {
	server   *Server
	messages *messages.Repo
	blobs    *blobs.Store
	cookieA  string
	userAID  int64
	userBID  int64
}

func newExportTestEnv(t *testing.T) *exportTestEnv {
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
	msgRepo := messages.NewRepo(database)
	store := blobs.NewStore(filepath.Join(dir, "blobs"))
	searchExec := search.NewExecutor(database)
	srv := New(userRepo, sessions, nil, nil, nil, msgRepo, searchExec, store, nil)

	hashA, err := auth.HashPasswordForTesting("pass")
	if err != nil {
		t.Fatal(err)
	}
	uA, err := userRepo.Create("a@test.com", hashA)
	if err != nil {
		t.Fatal(err)
	}
	sessA, err := sessions.Create(uA.ID)
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

	return &exportTestEnv{
		server:   srv,
		messages: msgRepo,
		blobs:    store,
		cookieA:  hex.EncodeToString(sessA.ID),
		userAID:  uA.ID,
		userBID:  uB.ID,
	}
}

func (e *exportTestEnv) post(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", target, nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: e.cookieA})
	rr := httptest.NewRecorder()
	e.server.ServeHTTP(rr, req)
	return rr
}

func TestExport_MissingQuery_400(t *testing.T) {
	env := newExportTestEnv(t)
	rr := env.post(t, "/export?format=mbox")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestExport_NoResults_404(t *testing.T) {
	env := newExportTestEnv(t)
	rr := env.post(t, "/export?format=mbox&q=nothingmatches")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestExport_UnknownFormat_400(t *testing.T) {
	env := newExportTestEnv(t)

	// Seed one message so search returns a hit.
	raw := []byte("From: a@x.com\r\nSubject: Hi\r\n\r\nBody\r\n")
	hash, err := env.blobs.Put(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	date := int64(1700000000)
	if err := env.messages.Insert(&messages.Message{
		Hash: hash, UserID: env.userAID, Subject: "Hi", FromAddr: "a@x.com",
		Date: &date, Size: int64(len(raw)),
	}); err != nil {
		t.Fatal(err)
	}

	rr := env.post(t, "/export?format=zip&q=subject:Hi")
	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}

func TestExport_Mbox_Success(t *testing.T) {
	env := newExportTestEnv(t)

	raw := []byte("From: alice@x.com\r\nSubject: ExportMe\r\n\r\nHello body\r\n")
	hash, err := env.blobs.Put(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	date := int64(1700000000)
	if err := env.messages.Insert(&messages.Message{
		Hash: hash, UserID: env.userAID, Subject: "ExportMe", FromAddr: "alice@x.com",
		Date: &date, Size: int64(len(raw)),
	}); err != nil {
		t.Fatal(err)
	}

	rr := env.post(t, "/export?format=mbox&q=subject:ExportMe")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/mbox" {
		t.Errorf("Content-Type = %q, want application/mbox", ct)
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, "export.mbox") {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if !strings.Contains(rr.Body.String(), "Hello body") {
		t.Error("expected mbox body to contain message body")
	}
}

func TestExport_DefaultsToMbox(t *testing.T) {
	env := newExportTestEnv(t)

	raw := []byte("From: a@x.com\r\nSubject: Defaulted\r\n\r\nbody\r\n")
	hash, err := env.blobs.Put(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	date := int64(1700000000)
	if err := env.messages.Insert(&messages.Message{
		Hash: hash, UserID: env.userAID, Subject: "Defaulted", FromAddr: "a@x.com",
		Date: &date, Size: int64(len(raw)),
	}); err != nil {
		t.Fatal(err)
	}

	// Omit format param entirely.
	rr := env.post(t, "/export?q=subject:Defaulted")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/mbox" {
		t.Errorf("Content-Type = %q, want application/mbox (default)", ct)
	}
}

func TestExport_CrossUserIsolation_404(t *testing.T) {
	env := newExportTestEnv(t)

	// Seed a message owned by user B; user A's session must not see it.
	raw := []byte("From: b@x.com\r\nSubject: OtherUser\r\n\r\nbody\r\n")
	hash, err := env.blobs.Put(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	date := int64(1700000000)
	if err := env.messages.Insert(&messages.Message{
		Hash: hash, UserID: env.userBID, Subject: "OtherUser", FromAddr: "b@x.com",
		Date: &date, Size: int64(len(raw)),
	}); err != nil {
		t.Fatal(err)
	}

	// User A queries for it — should get 404 because user A owns no matching messages.
	rr := env.post(t, "/export?format=mbox&q=subject:OtherUser")
	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (user isolation)", rr.Code, http.StatusNotFound)
	}
}
