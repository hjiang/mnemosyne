package httpserver

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/hjiang/mnemosyne/internal/auth"
	"github.com/hjiang/mnemosyne/internal/blobs"
	"github.com/hjiang/mnemosyne/internal/db"
	"github.com/hjiang/mnemosyne/internal/messages"
	"github.com/hjiang/mnemosyne/internal/users"
)

type messageTestEnv struct {
	server   *Server
	messages *messages.Repo
	blobs    *blobs.Store
	cookieA  string
	cookieB  string
}

func newMessageTestEnv(t *testing.T) *messageTestEnv {
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
	blobStore := blobs.NewStore(filepath.Join(dir, "blobs"))
	srv := New(userRepo, sessions, nil, nil, nil, msgRepo, nil, blobStore, nil)

	hashA, _ := auth.HashPassword("pass")
	uA, _ := userRepo.Create("a@test.com", hashA)
	hashB, _ := auth.HashPassword("pass")
	uB, _ := userRepo.Create("b@test.com", hashB)

	sessA, _ := sessions.Create(uA.ID)
	sessB, _ := sessions.Create(uB.ID)

	return &messageTestEnv{
		server:   srv,
		messages: msgRepo,
		blobs:    blobStore,
		cookieA:  hex.EncodeToString(sessA.ID),
		cookieB:  hex.EncodeToString(sessB.ID),
	}
}

func TestAttachmentDownload(t *testing.T) {
	env := newMessageTestEnv(t)

	// Insert a message for user A (ID=1).
	msgHash := sha256.Sum256([]byte("test-msg"))
	date := int64(1700000000)
	err := env.messages.Insert(&messages.Message{
		Hash: msgHash[:], UserID: 1, Subject: "Test",
		Date: &date, Size: 10,
		HasAttachments: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Store a blob.
	content := []byte("hello attachment content")
	blobHash, err := env.blobs.Put(bytes.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}

	// Insert an attachment.
	att := &messages.Attachment{
		MessageHash: msgHash[:],
		Filename:    "hello.txt",
		MimeType:    "text/plain",
		Size:        int64(len(content)),
		BlobHash:    blobHash,
	}
	if err := env.messages.InsertAttachment(att); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/attachment/"+strconv.FormatInt(att.ID, 10), nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookieA})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	if ct := rr.Header().Get("Content-Type"); ct != "text/plain" {
		t.Errorf("Content-Type = %q, want text/plain", ct)
	}
	if cd := rr.Header().Get("Content-Disposition"); cd != `attachment; filename="hello.txt"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if rr.Body.String() != string(content) {
		t.Errorf("body = %q, want %q", rr.Body.String(), content)
	}
}

func TestAttachmentDownload_UserIsolation(t *testing.T) {
	env := newMessageTestEnv(t)

	// Insert a message for user A (ID=1).
	msgHash := sha256.Sum256([]byte("iso-msg"))
	date := int64(1700000000)
	_ = env.messages.Insert(&messages.Message{
		Hash: msgHash[:], UserID: 1, Subject: "Secret",
		Date: &date, Size: 10, HasAttachments: true,
	})

	content := []byte("secret data")
	blobHash, _ := env.blobs.Put(bytes.NewReader(content))

	att := &messages.Attachment{
		MessageHash: msgHash[:], Filename: "secret.pdf",
		MimeType: "application/pdf", Size: int64(len(content)),
		BlobHash: blobHash,
	}
	_ = env.messages.InsertAttachment(att)

	// User B tries to download user A's attachment.
	req := httptest.NewRequest("GET", "/attachment/"+strconv.FormatInt(att.ID, 10), nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookieB})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d (user isolation)", rr.Code, http.StatusNotFound)
	}
}

func TestAttachmentDownload_NotFound(t *testing.T) {
	env := newMessageTestEnv(t)

	req := httptest.NewRequest("GET", "/attachment/9999", nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookieA})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

func TestReprocess_UpdatesSubject(t *testing.T) {
	env := newMessageTestEnv(t)

	// Raw RFC 822 message with an RFC 2047 GB2312-encoded subject ("你好！").
	raw := []byte("From: sender@test.com\r\nTo: rcpt@test.com\r\nSubject: =?GB2312?B?xOO6w6Oh?=\r\nMessage-ID: <rfc2047@test>\r\nDate: Mon, 01 Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nBody\r\n")

	// Store the blob and insert a message with the undecoded subject
	// (simulating what happened before the Dial fix).
	blobHash, err := env.blobs.Put(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	date := int64(1700000000)
	err = env.messages.Insert(&messages.Message{
		Hash:    blobHash,
		UserID:  1,
		Subject: "=?GB2312?B?xOO6w6Oh?=", // stored undecoded
		Date:    &date,
		Size:    int64(len(raw)),
	})
	if err != nil {
		t.Fatal(err)
	}

	hashHex := hex.EncodeToString(blobHash)
	req := httptest.NewRequest("POST", "/message/"+hashHex+"/reprocess", nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookieA})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}

	// Verify the subject was updated to the decoded value.
	msg, err := env.messages.GetByHash(blobHash, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := "你好！"
	if msg.Subject != want {
		t.Errorf("Subject = %q, want %q", msg.Subject, want)
	}
}

func TestAttachmentDownload_InvalidID(t *testing.T) {
	env := newMessageTestEnv(t)

	req := httptest.NewRequest("GET", "/attachment/abc", nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookieA})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
}
