package backup

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hjiang/mnemosyne/internal/accounts"
	imapwrap "github.com/hjiang/mnemosyne/internal/backup/imap"
	"github.com/hjiang/mnemosyne/internal/blobs"
	"github.com/hjiang/mnemosyne/internal/db"
	"github.com/hjiang/mnemosyne/internal/messages"
	"github.com/hjiang/mnemosyne/internal/testimap"
)

// failingFetchClient wraps a real IMAPClient but returns errors for specific UIDs
// on FetchBody calls, simulating messages deleted between envelope listing and body fetch.
// It also counts FetchBody calls to verify skip behavior.
type failingFetchClient struct {
	IMAPClient
	failUIDs       map[uint32]bool
	fetchBodyCalls int
}

// eofFetchClient wraps a real IMAPClient and simulates a connection failure
// (unexpected EOF) during FetchBodies, returning only the first N bodies.
type eofFetchClient struct {
	IMAPClient
	succeedCount int // how many UIDs to return before simulating EOF
}

// eofEnvelopeClient wraps a real IMAPClient and simulates a connection failure
// during FetchEnvelopes, returning only the first N envelopes.
type eofEnvelopeClient struct {
	IMAPClient
	envelopeLimit int // how many envelopes to return before simulating EOF
}

func (f *failingFetchClient) FetchBody(uid uint32) ([]byte, error) {
	f.fetchBodyCalls++
	if f.failUIDs[uid] {
		return nil, fmt.Errorf("no message with UID %d", uid)
	}
	return f.IMAPClient.FetchBody(uid)
}

func (f *failingFetchClient) FetchBodies(uids []uint32) (map[uint32][]byte, []uint32, error) {
	found := make(map[uint32][]byte)
	var missing []uint32
	for _, uid := range uids {
		body, err := f.FetchBody(uid)
		if err != nil {
			missing = append(missing, uid)
			continue
		}
		found[uid] = body
	}
	return found, missing, nil
}

func (e *eofEnvelopeClient) FetchEnvelopes(startUID, endUID uint32) ([]imapwrap.Envelope, error) {
	envs, err := e.IMAPClient.FetchEnvelopes(startUID, endUID)
	if err != nil {
		return envs, err
	}
	if len(envs) <= e.envelopeLimit {
		return envs, nil
	}
	return envs[:e.envelopeLimit], fmt.Errorf("in response-data: unexpected EOF")
}

func (e *eofFetchClient) FetchBodies(uids []uint32) (map[uint32][]byte, []uint32, error) {
	found := make(map[uint32][]byte)
	var missing []uint32
	for i, uid := range uids {
		if i >= e.succeedCount {
			missing = append(missing, uids[i:]...)
			break
		}
		body, err := e.FetchBody(uid)
		if err != nil {
			missing = append(missing, uid)
			continue
		}
		found[uid] = body
	}
	return found, missing, fmt.Errorf("in response-data: unexpected EOF")
}

type testEnv struct {
	orchestrator *Orchestrator
	accountsRepo *accounts.Repo
	messagesRepo *messages.Repo
	blobStore    *blobs.Store
	imapSrv      *testimap.Server
	accountID    int64
	userID       int64
}

func newTestEnv(t *testing.T) *testEnv {
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

	// Create a user.
	database.Exec("INSERT INTO users (email, password_hash, created_at) VALUES (?, ?, ?)", "test@test.com", "h", 0) //nolint:errcheck,gosec

	acctRepo := accounts.NewRepo(database, km)
	msgRepo := messages.NewRepo(database)
	store := blobs.NewStore(filepath.Join(dir, "blobs"))

	srv := testimap.New(t)
	host, portStr, _ := net.SplitHostPort(srv.Addr)
	port, _ := strconv.Atoi(portStr)

	acct, err := acctRepo.Create(1, "test", host, port, srv.Username, srv.Password, false, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}

	orch := NewOrchestrator(acctRepo, msgRepo, store, nil)

	return &testEnv{
		orchestrator: orch,
		accountsRepo: acctRepo,
		messagesRepo: msgRepo,
		blobStore:    store,
		imapSrv:      srv,
		accountID:    acct.ID,
		userID:       1,
	}
}

func enableFolder(t *testing.T, env *testEnv, name string) int64 {
	t.Helper()
	env.imapSrv.AddFolder(t, name, 1)
	folder, err := env.accountsRepo.CreateFolder(env.accountID, name)
	if err != nil {
		t.Fatal(err)
	}
	if err := env.accountsRepo.SetFolderEnabled(folder.ID, true); err != nil {
		t.Fatal(err)
	}
	return folder.ID
}

// Test 25: Fresh backup stores all messages.
func TestOrchestrator_FreshBackup(t *testing.T) {
	env := newTestEnv(t)
	folderID := enableFolder(t, env, "INBOX")
	env.imapSrv.SeedMessages(t, "INBOX", 5)

	result, err := env.orchestrator.Run(env.accountID, env.userID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.NewMessages != 5 {
		t.Errorf("NewMessages = %d, want 5", result.NewMessages)
	}

	msgs, err := env.messagesRepo.ListByFolder(folderID, env.userID)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 5 {
		t.Errorf("messages in folder = %d, want 5", len(msgs))
	}
}

// Test 26: Idempotency — running again produces no new work.
func TestOrchestrator_Idempotent(t *testing.T) {
	env := newTestEnv(t)
	enableFolder(t, env, "INBOX")
	env.imapSrv.SeedMessages(t, "INBOX", 5)

	if _, err := env.orchestrator.Run(env.accountID, env.userID, nil); err != nil {
		t.Fatal(err)
	}

	result, err := env.orchestrator.Run(env.accountID, env.userID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.NewMessages != 0 {
		t.Errorf("NewMessages on second run = %d, want 0", result.NewMessages)
	}
}

// Test 27: Incremental — only new messages are fetched.
func TestOrchestrator_Incremental(t *testing.T) {
	env := newTestEnv(t)
	enableFolder(t, env, "INBOX")
	env.imapSrv.SeedMessages(t, "INBOX", 3)

	if _, err := env.orchestrator.Run(env.accountID, env.userID, nil); err != nil {
		t.Fatal(err)
	}

	// Seed 3 more unique messages (different from the first batch).
	for i := 4; i <= 6; i++ {
		raw := fmt.Sprintf("From: sender%d@test.com\r\nTo: rcpt@test.com\r\nSubject: Test message %d\r\nMessage-ID: <msg%d@test>\r\nDate: Mon, 01 Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nBody of message %d\r\n", i, i, i, i)
		env.imapSrv.AppendMessage(t, "INBOX", []byte(raw))
	}

	result, err := env.orchestrator.Run(env.accountID, env.userID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.NewMessages != 3 {
		t.Errorf("NewMessages = %d, want 3", result.NewMessages)
	}
}

// Test 28: Cross-folder dedup — same message in two folders.
func TestOrchestrator_CrossFolderDedup(t *testing.T) {
	env := newTestEnv(t)
	folderID1 := enableFolder(t, env, "INBOX")
	folderID2 := enableFolder(t, env, "Archive")

	// Seed the same message in both folders.
	raw := []byte("From: sender@test.com\r\nTo: rcpt@test.com\r\nSubject: Shared message\r\nMessage-ID: <shared@test>\r\nDate: Mon, 01 Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nShared body\r\n")
	env.imapSrv.AppendMessage(t, "INBOX", raw)
	env.imapSrv.AppendMessage(t, "Archive", raw)

	result, err := env.orchestrator.Run(env.accountID, env.userID, nil)
	if err != nil {
		t.Fatal(err)
	}

	// One unique message, but two locations.
	if result.NewMessages != 1 {
		t.Errorf("NewMessages = %d, want 1 (dedup)", result.NewMessages)
	}
	if result.NewLocations != 2 {
		t.Errorf("NewLocations = %d, want 2", result.NewLocations)
	}

	// Verify: one message visible from both folders.
	msgs1, _ := env.messagesRepo.ListByFolder(folderID1, env.userID)
	msgs2, _ := env.messagesRepo.ListByFolder(folderID2, env.userID)
	if len(msgs1) != 1 || len(msgs2) != 1 {
		t.Errorf("folder1=%d, folder2=%d, want 1 each", len(msgs1), len(msgs2))
	}

	// Same hash.
	if !bytes.Equal(msgs1[0].Hash, msgs2[0].Hash) {
		t.Error("expected same hash in both folders")
	}

	// One blob on disk.
	hash := sha256.Sum256(raw)
	if !env.blobStore.Exists(hash[:]) {
		t.Error("blob not found on disk")
	}
}

// Test 29: UIDVALIDITY reset triggers re-scan.
func TestOrchestrator_UIDValidityReset(t *testing.T) {
	env := newTestEnv(t)
	folderID := enableFolder(t, env, "INBOX")
	env.imapSrv.SeedMessages(t, "INBOX", 3)

	if _, err := env.orchestrator.Run(env.accountID, env.userID, nil); err != nil {
		t.Fatal(err)
	}

	// Simulate UIDVALIDITY reset by re-creating the folder on the test server.
	// The in-memory server assigns a new UIDVALIDITY when we re-add the folder.
	// We need to clear and re-seed.
	// Since imapmemserver doesn't support UIDVALIDITY cycling,
	// we simulate by setting a stored UIDValidity that differs from the server's.
	if err := env.accountsRepo.SetUIDValidity(folderID, 99999); err != nil {
		t.Fatal(err)
	}

	result, err := env.orchestrator.Run(env.accountID, env.userID, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Locations were cleared and re-created; messages already existed (same hashes).
	if result.NewLocations != 3 {
		t.Errorf("NewLocations after reset = %d, want 3", result.NewLocations)
	}
}

// Test 31: Attachments are stored with text_extracted = 0.
func TestOrchestrator_Attachments(t *testing.T) {
	env := newTestEnv(t)
	enableFolder(t, env, "INBOX")

	// Seed a message with a MIME attachment.
	boundary := "----=_Part_12345"
	raw := fmt.Sprintf("From: sender@test.com\r\nTo: rcpt@test.com\r\nSubject: With attachment\r\nMessage-ID: <att@test>\r\nDate: Mon, 01 Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"%s\"\r\n\r\n--%s\r\nContent-Type: text/plain\r\n\r\nBody text\r\n--%s\r\nContent-Type: application/pdf; name=\"report.pdf\"\r\nContent-Disposition: attachment; filename=\"report.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\nJVBERi0xLjQKMSAwIG9iago8PC9UeXBlL0NhdGFsb2c+PgplbmRvYmoK\r\n--%s--\r\n", boundary, boundary, boundary, boundary)
	env.imapSrv.AppendMessage(t, "INBOX", []byte(raw))

	result, err := env.orchestrator.Run(env.accountID, env.userID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.NewMessages != 1 {
		t.Errorf("NewMessages = %d, want 1", result.NewMessages)
	}

	// Check that the message was stored.
	h := sha256.Sum256([]byte(raw))
	msg, err := env.messagesRepo.GetByHash(h[:], env.userID)
	if err != nil {
		t.Fatal(err)
	}

	atts, err := env.messagesRepo.ListAttachments(msg.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 1 {
		t.Fatalf("attachments = %d, want 1", len(atts))
	}
	if atts[0].Filename != "report.pdf" {
		t.Errorf("filename = %q, want report.pdf", atts[0].Filename)
	}
	if atts[0].TextExtracted != 0 {
		t.Errorf("text_extracted = %d, want 0", atts[0].TextExtracted)
	}
	if !env.blobStore.Exists(atts[0].BlobHash) {
		t.Error("attachment blob not on disk")
	}
}

// Test 32: Messages with same Message-ID but different bodies are both stored.
func TestOrchestrator_DuplicateMessageID(t *testing.T) {
	env := newTestEnv(t)
	enableFolder(t, env, "INBOX")

	raw1 := []byte("From: a@test.com\r\nTo: b@test.com\r\nSubject: Version 1\r\nMessage-ID: <same@test>\r\nDate: Mon, 01 Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nBody version 1\r\n")
	raw2 := []byte("From: a@test.com\r\nTo: b@test.com\r\nSubject: Version 2\r\nMessage-ID: <same@test>\r\nDate: Mon, 01 Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nBody version 2\r\n")
	env.imapSrv.AppendMessage(t, "INBOX", raw1)
	env.imapSrv.AppendMessage(t, "INBOX", raw2)

	result, err := env.orchestrator.Run(env.accountID, env.userID, nil)
	if err != nil {
		t.Fatal(err)
	}

	// Both messages should be stored since they have different bodies/hashes.
	if result.NewMessages != 2 {
		t.Errorf("NewMessages = %d, want 2 (different bodies)", result.NewMessages)
	}
}

// Test 33: User isolation — two users can't see each other's messages.
func TestOrchestrator_UserIsolation(t *testing.T) {
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

	database.Exec("INSERT INTO users (email, password_hash, created_at) VALUES (?, ?, ?)", "a@test.com", "h", 0) //nolint:errcheck,gosec
	database.Exec("INSERT INTO users (email, password_hash, created_at) VALUES (?, ?, ?)", "b@test.com", "h", 0) //nolint:errcheck,gosec

	acctRepo := accounts.NewRepo(database, km)
	msgRepo := messages.NewRepo(database)
	store := blobs.NewStore(filepath.Join(dir, "blobs"))
	orch := NewOrchestrator(acctRepo, msgRepo, store, nil)

	// User A's server and account (unique messages for user A).
	srvA := testimap.New(t)
	srvA.AddFolder(t, "INBOX", 1)
	for i := 1; i <= 3; i++ {
		raw := fmt.Sprintf("From: alice%d@a.com\r\nTo: rcpt@a.com\r\nSubject: A msg %d\r\nMessage-ID: <a-msg%d@a>\r\nDate: Mon, 01 Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nUser A body %d\r\n", i, i, i, i)
		srvA.AppendMessage(t, "INBOX", []byte(raw))
	}
	hostA, portStrA, _ := net.SplitHostPort(srvA.Addr)
	portA, _ := strconv.Atoi(portStrA)
	acctA, _ := acctRepo.Create(1, "A", hostA, portA, srvA.Username, srvA.Password, false, "", 0, "", "")
	folderA, _ := acctRepo.CreateFolder(acctA.ID, "INBOX")
	_ = acctRepo.SetFolderEnabled(folderA.ID, true)

	// User B's server and account (unique messages for user B).
	srvB := testimap.New(t)
	srvB.AddFolder(t, "INBOX", 1)
	for i := 1; i <= 2; i++ {
		raw := fmt.Sprintf("From: bob%d@b.com\r\nTo: rcpt@b.com\r\nSubject: B msg %d\r\nMessage-ID: <b-msg%d@b>\r\nDate: Mon, 01 Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nUser B body %d\r\n", i, i, i, i)
		srvB.AppendMessage(t, "INBOX", []byte(raw))
	}
	hostB, portStrB, _ := net.SplitHostPort(srvB.Addr)
	portB, _ := strconv.Atoi(portStrB)
	acctB, _ := acctRepo.Create(2, "B", hostB, portB, srvB.Username, srvB.Password, false, "", 0, "", "")
	folderB, _ := acctRepo.CreateFolder(acctB.ID, "INBOX")
	_ = acctRepo.SetFolderEnabled(folderB.ID, true)

	if _, err := orch.Run(acctA.ID, 1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Run(acctB.ID, 2, nil); err != nil {
		t.Fatal(err)
	}

	// User A sees only their messages.
	msgsA, _ := msgRepo.ListByFolder(folderA.ID, 1)
	if len(msgsA) != 3 {
		t.Errorf("user A sees %d, want 3", len(msgsA))
	}
	msgsACross, _ := msgRepo.ListByFolder(folderA.ID, 2)
	if len(msgsACross) != 0 {
		t.Errorf("user B sees %d of A's messages, want 0", len(msgsACross))
	}

	// User B sees only their messages.
	msgsB, _ := msgRepo.ListByFolder(folderB.ID, 2)
	if len(msgsB) != 2 {
		t.Errorf("user B sees %d, want 2", len(msgsB))
	}
	msgsBCross, _ := msgRepo.ListByFolder(folderB.ID, 1)
	if len(msgsBCross) != 0 {
		t.Errorf("user A sees %d of B's messages, want 0", len(msgsBCross))
	}
}

// Test: Empty folder produces no errors and no work.
func TestOrchestrator_EmptyFolder(t *testing.T) {
	env := newTestEnv(t)
	enableFolder(t, env, "INBOX")
	// No messages seeded.

	result, err := env.orchestrator.Run(env.accountID, env.userID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.NewMessages != 0 {
		t.Errorf("NewMessages = %d, want 0", result.NewMessages)
	}
}

// Test: Disabled folders are skipped.
func TestOrchestrator_DisabledFolder(t *testing.T) {
	env := newTestEnv(t)
	env.imapSrv.AddFolder(t, "INBOX", 1)
	env.imapSrv.SeedMessages(t, "INBOX", 3)

	// Create folder but don't enable it.
	_, err := env.accountsRepo.CreateFolder(env.accountID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}

	result, err := env.orchestrator.Run(env.accountID, env.userID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.NewMessages != 0 {
		t.Errorf("NewMessages = %d, want 0 (folder disabled)", result.NewMessages)
	}
}

// Test: Backup extracts body text from plain-text email.
func TestOrchestrator_BodyTextExtracted(t *testing.T) {
	env := newTestEnv(t)
	enableFolder(t, env, "INBOX")

	raw := []byte("From: sender@test.com\r\nTo: rcpt@test.com\r\nSubject: Body test\r\nMessage-ID: <bodytest@test>\r\nDate: Mon, 01 Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nHello, this is the email body.\r\n")
	env.imapSrv.AppendMessage(t, "INBOX", raw)

	_, err := env.orchestrator.Run(env.accountID, env.userID, nil)
	if err != nil {
		t.Fatal(err)
	}

	h := sha256.Sum256(raw)
	msg, err := env.messagesRepo.GetByHash(h[:], env.userID)
	if err != nil {
		t.Fatal(err)
	}

	if msg.BodyText == "" {
		t.Error("BodyText is empty, want extracted body text")
	}
	if !strings.Contains(msg.BodyText, "Hello, this is the email body") {
		t.Errorf("BodyText = %q, want to contain email body text", msg.BodyText)
	}
}

// Test: Backup extracts body text from multipart email with text/plain part.
func TestOrchestrator_BodyTextExtracted_Multipart(t *testing.T) {
	env := newTestEnv(t)
	enableFolder(t, env, "INBOX")

	boundary := "----=_Part_99999"
	raw := fmt.Sprintf("From: sender@test.com\r\nTo: rcpt@test.com\r\nSubject: Multipart body\r\nMessage-ID: <mpbody@test>\r\nDate: Mon, 01 Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: multipart/alternative; boundary=\"%s\"\r\n\r\n--%s\r\nContent-Type: text/plain\r\n\r\nPlain text version of the email.\r\n--%s\r\nContent-Type: text/html\r\n\r\n<html><body><p>HTML version of the email.</p></body></html>\r\n--%s--\r\n", boundary, boundary, boundary, boundary)
	env.imapSrv.AppendMessage(t, "INBOX", []byte(raw))

	_, err := env.orchestrator.Run(env.accountID, env.userID, nil)
	if err != nil {
		t.Fatal(err)
	}

	h := sha256.Sum256([]byte(raw))
	msg, err := env.messagesRepo.GetByHash(h[:], env.userID)
	if err != nil {
		t.Fatal(err)
	}

	if msg.BodyText == "" {
		t.Error("BodyText is empty, want extracted body text from multipart")
	}
	if !strings.Contains(msg.BodyText, "Plain text version") {
		t.Errorf("BodyText = %q, want text/plain part content", msg.BodyText)
	}
}

// Test: ExtractBodyText handles HTML-only email.
func TestExtractBodyText_HTMLOnly(t *testing.T) {
	raw := []byte("From: sender@test.com\r\nTo: rcpt@test.com\r\nSubject: HTML only\r\nMessage-ID: <html@test>\r\nDate: Mon, 01 Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/html\r\n\r\n<html><body><p>Hello from HTML</p></body></html>\r\n")
	text := ExtractBodyText(raw)
	if !strings.Contains(text, "Hello from HTML") {
		t.Errorf("ExtractBodyText = %q, want to contain 'Hello from HTML'", text)
	}
}

// Test: ExtractBodyText handles nested multipart/mixed > multipart/alternative.
func TestExtractBodyText_NestedMultipart(t *testing.T) {
	raw := []byte("From: sender@test.com\r\n" +
		"To: rcpt@test.com\r\n" +
		"Subject: Nested\r\n" +
		"Message-ID: <nested@test>\r\n" +
		"Date: Mon, 01 Jan 2024 00:00:00 +0000\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"outer\"\r\n" +
		"\r\n" +
		"--outer\r\n" +
		"Content-Type: multipart/alternative; boundary=\"inner\"\r\n" +
		"\r\n" +
		"--inner\r\n" +
		"Content-Type: text/plain\r\n" +
		"\r\n" +
		"Hello plain\r\n" +
		"--inner\r\n" +
		"Content-Type: text/html\r\n" +
		"\r\n" +
		"<p>Hello html</p>\r\n" +
		"--inner--\r\n" +
		"--outer\r\n" +
		"Content-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment; filename=\"file.bin\"\r\n" +
		"\r\n" +
		"binarydata\r\n" +
		"--outer--\r\n")

	text := ExtractBodyText(raw)
	if text != "Hello plain" {
		t.Errorf("ExtractBodyText = %q, want %q", text, "Hello plain")
	}
}

// Test: ExtractBodyText falls back to HTML in nested multipart.
func TestExtractBodyText_NestedMultipartHTMLOnly(t *testing.T) {
	raw := []byte("From: sender@test.com\r\n" +
		"To: rcpt@test.com\r\n" +
		"Subject: Nested HTML\r\n" +
		"Message-ID: <nested-html@test>\r\n" +
		"Date: Mon, 01 Jan 2024 00:00:00 +0000\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"outer\"\r\n" +
		"\r\n" +
		"--outer\r\n" +
		"Content-Type: multipart/alternative; boundary=\"inner\"\r\n" +
		"\r\n" +
		"--inner\r\n" +
		"Content-Type: text/html\r\n" +
		"\r\n" +
		"<p>Only HTML body</p>\r\n" +
		"--inner--\r\n" +
		"--outer\r\n" +
		"Content-Type: application/octet-stream\r\n" +
		"Content-Disposition: attachment; filename=\"file.bin\"\r\n" +
		"\r\n" +
		"binarydata\r\n" +
		"--outer--\r\n")

	text := ExtractBodyText(raw)
	if !strings.Contains(text, "Only HTML body") {
		t.Errorf("ExtractBodyText = %q, want to contain %q", text, "Only HTML body")
	}
}

// Test: ExtractBodyText prefers body text over text/plain attachment.
func TestExtractBodyText_TextPlainAttachment(t *testing.T) {
	raw := []byte("From: sender@test.com\r\n" +
		"To: rcpt@test.com\r\n" +
		"Subject: testing attachment\r\n" +
		"Message-ID: <test@test>\r\n" +
		"Date: Mon, 13 Apr 2026 02:30:45 +0800\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"outer\"\r\n" +
		"\r\n" +
		"--outer\r\n" +
		"Content-Type: multipart/alternative; boundary=\"inner\"\r\n" +
		"\r\n" +
		"--inner\r\n" +
		"Content-Transfer-Encoding: 7bit\r\n" +
		"Content-Type: text/plain; charset=US-ASCII; format=flowed\r\n" +
		"\r\n" +
		"Just testing an email with attachment.\r\n" +
		"--inner\r\n" +
		"Content-Transfer-Encoding: quoted-printable\r\n" +
		"Content-Type: text/html; charset=UTF-8\r\n" +
		"\r\n" +
		"<html><body><p>Just testing an email with attachment.</p></body></html>\r\n" +
		"--inner--\r\n" +
		"--outer\r\n" +
		"Content-Transfer-Encoding: base64\r\n" +
		"Content-Type: text/plain; name=model.stl\r\n" +
		"Content-Disposition: attachment; filename=model.stl; size=3052\r\n" +
		"\r\n" +
		"c29saWQgCiBmYWNldCBub3JtYWwgLTEuMDAwMDAwZSswMCAgMC4wMDAwMDBlKzAw\r\n" +
		"--outer--\r\n")

	text := ExtractBodyText(raw)
	if text != "Just testing an email with attachment." {
		t.Errorf("ExtractBodyText = %q, want %q", text, "Just testing an email with attachment.")
	}
}

// Test: Retention policy is applied after backup — older messages are expunged from IMAP.
func TestOrchestrator_RetentionApplied(t *testing.T) {
	env := newTestEnv(t)
	folderID := enableFolder(t, env, "INBOX")

	// Seed 5 messages with distinct dates so newest_n ordering is deterministic.
	for i := 1; i <= 5; i++ {
		raw := fmt.Sprintf(
			"From: sender@test.com\r\nTo: rcpt@test.com\r\nSubject: msg %d\r\nMessage-ID: <ret%d@test>\r\nDate: Mon, 0%d Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nBody %d\r\n",
			i, i, i, i,
		)
		env.imapSrv.AppendMessage(t, "INBOX", []byte(raw))
	}

	// Set retention: keep newest 2.
	if err := env.accountsRepo.SetFolderPolicy(folderID, `{"leave_on_server":"newest_n","n":2}`); err != nil {
		t.Fatal(err)
	}

	result, err := env.orchestrator.Run(env.accountID, env.userID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.NewMessages != 5 {
		t.Errorf("NewMessages = %d, want 5", result.NewMessages)
	}

	// Verify: connect to IMAP server and check that only 2 messages remain.
	client := connectTestIMAP(t, env.imapSrv)
	info, err := client.SelectFolder("INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if info.NumMessages != 2 {
		t.Errorf("IMAP NumMessages after retention = %d, want 2", info.NumMessages)
	}
}

// Test: Default "all" policy does not delete any messages from IMAP.
func TestOrchestrator_RetentionDefaultAll(t *testing.T) {
	env := newTestEnv(t)
	enableFolder(t, env, "INBOX")
	env.imapSrv.SeedMessages(t, "INBOX", 5)

	// Default policy is "all" — no explicit SetFolderPolicy call.
	if _, err := env.orchestrator.Run(env.accountID, env.userID, nil); err != nil {
		t.Fatal(err)
	}

	client := connectTestIMAP(t, env.imapSrv)
	info, err := client.SelectFolder("INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if info.NumMessages != 5 {
		t.Errorf("IMAP NumMessages = %d, want 5 (default policy keeps all)", info.NumMessages)
	}
}

// Test: Invalid account returns an error.
func TestOrchestrator_InvalidAccount(t *testing.T) {
	env := newTestEnv(t)
	_, err := env.orchestrator.Run(9999, env.userID, nil)
	if err == nil {
		t.Fatal("expected error for nonexistent account")
	}
}

// Test: Progress callback is called before each folder sync.
func TestOrchestrator_ProgressCallback(t *testing.T) {
	env := newTestEnv(t)
	enableFolder(t, env, "INBOX")
	enableFolder(t, env, "Sent")
	env.imapSrv.SeedMessages(t, "INBOX", 3)
	env.imapSrv.SeedMessages(t, "Sent", 2)

	var updates []Progress
	onProgress := func(p Progress) {
		updates = append(updates, p)
	}

	_, err := env.orchestrator.Run(env.accountID, env.userID, onProgress)
	if err != nil {
		t.Fatal(err)
	}

	if len(updates) != 2 {
		t.Fatalf("got %d progress updates, want 2", len(updates))
	}

	// First update: before syncing first folder (no messages yet).
	if updates[0].FolderIndex != 1 {
		t.Errorf("update[0].FolderIndex = %d, want 1", updates[0].FolderIndex)
	}
	if updates[0].FolderTotal != 2 {
		t.Errorf("update[0].FolderTotal = %d, want 2", updates[0].FolderTotal)
	}
	if updates[0].NewMessages != 0 {
		t.Errorf("update[0].NewMessages = %d, want 0 (reported before sync)", updates[0].NewMessages)
	}

	// Second update: before syncing second folder (has messages from first).
	if updates[1].FolderIndex != 2 {
		t.Errorf("update[1].FolderIndex = %d, want 2", updates[1].FolderIndex)
	}
	if updates[1].NewMessages != 3 {
		t.Errorf("update[1].NewMessages = %d, want 3 (accumulated from first folder)", updates[1].NewMessages)
	}
}

// Test: LastSeenUID must not advance past UIDs that failed to fetch.
// This reproduces the bug where a message deleted between FetchEnvelopes and
// FetchBody causes maxUID to advance past it, permanently skipping the message.
func TestOrchestrator_LastSeenUID_NotAdvancedPastFailures(t *testing.T) {
	env := newTestEnv(t)
	folderID := enableFolder(t, env, "INBOX")
	env.imapSrv.SeedMessages(t, "INBOX", 5) // UIDs 1-5

	// Connect a real client, then wrap it to fail on UID 3.
	realClient := connectTestIMAP(t, env.imapSrv)
	wrapped := &failingFetchClient{
		IMAPClient: realClient,
		failUIDs:   map[uint32]bool{3: true},
	}

	// Get the folder record for syncFolder.
	folders, err := env.accountsRepo.ListFolders(env.accountID)
	if err != nil {
		t.Fatal(err)
	}
	var folder *accounts.Folder
	for _, f := range folders {
		if f.ID == folderID {
			folder = f
			break
		}
	}
	if folder == nil {
		t.Fatal("folder not found")
	}

	result := &Result{}
	if err := env.orchestrator.syncFolder(wrapped, folder, env.userID, result, &[]imapwrap.Envelope{}); err != nil {
		t.Fatal(err)
	}

	// Should have 1 error (UID 3 failed).
	if len(result.Errors) != 1 {
		t.Errorf("Errors = %d, want 1", len(result.Errors))
	}

	// Should have stored 4 messages (UIDs 1,2,4,5).
	if result.NewMessages != 4 {
		t.Errorf("NewMessages = %d, want 4", result.NewMessages)
	}

	// Critical assertion: LastSeenUID must be 2 (last contiguously successful UID),
	// NOT 5 (the max UID). This ensures UID 3 will be retried on the next run.
	updatedFolder, err := env.accountsRepo.ListFolders(env.accountID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range updatedFolder {
		if f.ID == folderID {
			if f.LastSeenUID != 2 {
				t.Errorf("LastSeenUID = %d, want 2 (should not advance past failed UID 3)", f.LastSeenUID)
			}
			return
		}
	}
	t.Fatal("folder not found after sync")
}

// Test: Retry after partial failure skips already-stored messages.
func TestOrchestrator_RetrySkipsAlreadyStored(t *testing.T) {
	env := newTestEnv(t)
	folderID := enableFolder(t, env, "INBOX")
	env.imapSrv.SeedMessages(t, "INBOX", 5)

	realClient := connectTestIMAP(t, env.imapSrv)

	// First run: UID 3 fails.
	wrapped := &failingFetchClient{
		IMAPClient: realClient,
		failUIDs:   map[uint32]bool{3: true},
	}
	folders, err := env.accountsRepo.ListFolders(env.accountID)
	if err != nil {
		t.Fatal(err)
	}
	var folder *accounts.Folder
	for _, f := range folders {
		if f.ID == folderID {
			folder = f
			break
		}
	}

	result := &Result{}
	if err := env.orchestrator.syncFolder(wrapped, folder, env.userID, result, &[]imapwrap.Envelope{}); err != nil {
		t.Fatal(err)
	}
	if result.NewMessages != 4 {
		t.Fatalf("first run: NewMessages = %d, want 4", result.NewMessages)
	}

	// Second run: no failures. Reload folder to get updated LastSeenUID.
	folders, err = env.accountsRepo.ListFolders(env.accountID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range folders {
		if f.ID == folderID {
			folder = f
			break
		}
	}

	wrapped2 := &failingFetchClient{
		IMAPClient: realClient,
		failUIDs:   map[uint32]bool{},
	}
	result2 := &Result{}
	if err := env.orchestrator.syncFolder(wrapped2, folder, env.userID, result2, &[]imapwrap.Envelope{}); err != nil {
		t.Fatal(err)
	}

	// UID 3 should be fetched and stored. UIDs 1,2,4,5 should be skipped (already stored).
	if wrapped2.fetchBodyCalls != 1 {
		t.Errorf("retry fetchBodyCalls = %d, want 1 (only UID 3 should be fetched)", wrapped2.fetchBodyCalls)
	}
	if result2.NewMessages != 1 {
		t.Errorf("retry NewMessages = %d, want 1", result2.NewMessages)
	}
	if len(result2.Errors) != 0 {
		t.Errorf("retry Errors = %d, want 0", len(result2.Errors))
	}

	// LastSeenUID should now be 5 (all messages stored).
	folders, err = env.accountsRepo.ListFolders(env.accountID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range folders {
		if f.ID == folderID {
			if f.LastSeenUID != 5 {
				t.Errorf("LastSeenUID after retry = %d, want 5", f.LastSeenUID)
			}
			return
		}
	}
	t.Fatal("folder not found")
}

// Test: Retention must NOT run when backup partially fails.
// This reproduces a data-loss bug where applyRetention always passed
// backupOK=true, causing upstream messages to be deleted even though
// they weren't all safely backed up locally.
func TestOrchestrator_NoRetentionOnPartialFailure(t *testing.T) {
	env := newTestEnv(t)
	folderID := enableFolder(t, env, "INBOX")

	// Seed 5 messages with distinct dates so retention ordering is deterministic.
	for i := 1; i <= 5; i++ {
		raw := fmt.Sprintf(
			"From: sender@test.com\r\nTo: rcpt@test.com\r\nSubject: msg %d\r\nMessage-ID: <nrf%d@test>\r\nDate: Mon, 0%d Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nBody %d\r\n",
			i, i, i, i,
		)
		env.imapSrv.AppendMessage(t, "INBOX", []byte(raw))
	}

	// Set retention: keep newest 2 → would delete UIDs 1,2,3.
	if err := env.accountsRepo.SetFolderPolicy(folderID, `{"leave_on_server":"newest_n","n":2}`); err != nil {
		t.Fatal(err)
	}

	// Connect a real client, then wrap it to fail on UID 3.
	realClient := connectTestIMAP(t, env.imapSrv)
	wrapped := &failingFetchClient{
		IMAPClient: realClient,
		failUIDs:   map[uint32]bool{3: true},
	}

	// Get folder record for syncFolder.
	folders, err := env.accountsRepo.ListFolders(env.accountID)
	if err != nil {
		t.Fatal(err)
	}
	var folder *accounts.Folder
	for _, f := range folders {
		if f.ID == folderID {
			folder = f
			break
		}
	}
	if folder == nil {
		t.Fatal("folder not found")
	}

	result := &Result{}
	if err := env.orchestrator.syncFolder(wrapped, folder, env.userID, result, &[]imapwrap.Envelope{}); err != nil {
		t.Fatal(err)
	}

	// Incremental retention: UIDs 1,2 are backed up and in the expunge set → deleted.
	// UID 3 is in the expunge set but NOT backed up → stays on server.
	// UIDs 4,5 are kept by policy → stays on server.
	// Expected: 3 messages (UIDs 3,4,5).
	freshClient := connectTestIMAP(t, env.imapSrv)
	info, err := freshClient.SelectFolder("INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if info.NumMessages != 3 {
		t.Errorf("IMAP NumMessages after partial backup = %d, want 3 (only confirmed backups in expunge set should be deleted)", info.NumMessages)
	}
}

// Test: Incremental retention deletes backed-up messages per-policy during sync,
// even when some messages fail — confirmed backups in the expunge set are deleted,
// failed messages are left on the server.
func TestOrchestrator_IncrementalRetentionPartialFailure(t *testing.T) {
	env := newTestEnv(t)
	folderID := enableFolder(t, env, "INBOX")

	// Seed 5 messages with distinct dates (oldest=1, newest=5).
	for i := 1; i <= 5; i++ {
		raw := fmt.Sprintf(
			"From: sender@test.com\r\nTo: rcpt@test.com\r\nSubject: msg %d\r\nMessage-ID: <ir%d@test>\r\nDate: Mon, 0%d Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nBody %d\r\n",
			i, i, i, i,
		)
		env.imapSrv.AppendMessage(t, "INBOX", []byte(raw))
	}

	// Keep newest 2 → expunge set is UIDs 1,2,3.
	if err := env.accountsRepo.SetFolderPolicy(folderID, `{"leave_on_server":"newest_n","n":2}`); err != nil {
		t.Fatal(err)
	}

	// Fail UID 3 — it's in the expunge set but not backed up.
	realClient := connectTestIMAP(t, env.imapSrv)
	wrapped := &failingFetchClient{
		IMAPClient: realClient,
		failUIDs:   map[uint32]bool{3: true},
	}

	folders, err := env.accountsRepo.ListFolders(env.accountID)
	if err != nil {
		t.Fatal(err)
	}
	var folder *accounts.Folder
	for _, f := range folders {
		if f.ID == folderID {
			folder = f
			break
		}
	}
	if folder == nil {
		t.Fatal("folder not found")
	}

	result := &Result{}
	if err := env.orchestrator.syncFolder(wrapped, folder, env.userID, result, &[]imapwrap.Envelope{}); err != nil {
		t.Fatal(err)
	}

	// UIDs 1,2: backed up + in expunge set → deleted from server
	// UID 3: in expunge set but NOT backed up → stays on server
	// UIDs 4,5: backed up + kept by policy → stays on server
	// Expected: 3 messages on server (UIDs 3, 4, 5)
	freshClient := connectTestIMAP(t, env.imapSrv)
	info, err := freshClient.SelectFolder("INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if info.NumMessages != 3 {
		t.Errorf("IMAP NumMessages = %d, want 3 (UIDs 3,4,5 should remain)", info.NumMessages)
	}
}

// Test: Previously-backed-up messages get deleted when retention policy tightens.
func TestOrchestrator_RetentionDeletesPreviouslyBackedUp(t *testing.T) {
	env := newTestEnv(t)
	folderID := enableFolder(t, env, "INBOX")

	// Seed 5 messages with distinct dates.
	for i := 1; i <= 5; i++ {
		raw := fmt.Sprintf(
			"From: sender@test.com\r\nTo: rcpt@test.com\r\nSubject: msg %d\r\nMessage-ID: <rpb%d@test>\r\nDate: Mon, 0%d Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nBody %d\r\n",
			i, i, i, i,
		)
		env.imapSrv.AppendMessage(t, "INBOX", []byte(raw))
	}

	// First run: "all" policy — keep everything on server.
	result, err := env.orchestrator.Run(env.accountID, env.userID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.NewMessages != 5 {
		t.Fatalf("first run: NewMessages = %d, want 5", result.NewMessages)
	}

	// Tighten policy: keep newest 2 → should delete UIDs 1,2,3.
	if err := env.accountsRepo.SetFolderPolicy(folderID, `{"leave_on_server":"newest_n","n":2}`); err != nil {
		t.Fatal(err)
	}

	// Second run: no new messages, but retention should clean up.
	if _, err := env.orchestrator.Run(env.accountID, env.userID, nil); err != nil {
		t.Fatal(err)
	}

	// Verify: only 2 messages left on server.
	client := connectTestIMAP(t, env.imapSrv)
	info, err := client.SelectFolder("INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if info.NumMessages != 2 {
		t.Errorf("IMAP NumMessages = %d, want 2 after policy tightening", info.NumMessages)
	}
}

// Test: When FetchBodies returns a partial result with a connection error (EOF),
// only one error should be reported (the connection error), not individual
// "no message" errors for each unreceived UID.
func TestOrchestrator_BatchEOF_NoSpuriousErrors(t *testing.T) {
	env := newTestEnv(t)
	folderID := enableFolder(t, env, "INBOX")
	env.imapSrv.SeedMessages(t, "INBOX", 5) // UIDs 1-5

	realClient := connectTestIMAP(t, env.imapSrv)
	// Return only first 2 bodies, then simulate EOF.
	wrapped := &eofFetchClient{
		IMAPClient:   realClient,
		succeedCount: 2,
	}

	folders, err := env.accountsRepo.ListFolders(env.accountID)
	if err != nil {
		t.Fatal(err)
	}
	var folder *accounts.Folder
	for _, f := range folders {
		if f.ID == folderID {
			folder = f
			break
		}
	}
	if folder == nil {
		t.Fatal("folder not found")
	}

	result := &Result{}
	syncErr := env.orchestrator.syncFolder(wrapped, folder, env.userID, result, &[]imapwrap.Envelope{})

	// syncFolder should return a *connError wrapping the EOF.
	var ce *connError
	if !errors.As(syncErr, &ce) {
		t.Fatalf("expected *connError, got: %v", syncErr)
	}

	// Should have exactly 1 error in result.Errors: the connection-level EOF.
	// NOT 3 individual "no message with UID" errors for the unreceived UIDs.
	if len(result.Errors) != 1 {
		t.Errorf("Errors = %d, want 1; got: %v", len(result.Errors), result.Errors)
	}
	if len(result.Errors) > 0 && !strings.Contains(result.Errors[0].Error(), "unexpected EOF") {
		t.Errorf("expected EOF error, got: %v", result.Errors[0])
	}

	// The 2 successfully received messages should still be stored.
	if result.NewMessages != 2 {
		t.Errorf("NewMessages = %d, want 2", result.NewMessages)
	}

	// LastSeenUID should be 2 (last contiguously successful), not 5.
	folders, err = env.accountsRepo.ListFolders(env.accountID)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range folders {
		if f.ID == folderID {
			if f.LastSeenUID != 2 {
				t.Errorf("LastSeenUID = %d, want 2 (should not advance past EOF batch)", f.LastSeenUID)
			}
			return
		}
	}
	t.Fatal("folder not found after sync")
}

// Test: Run() reconnects and retries when a connection error occurs mid-sync,
// as long as forward progress is being made.
func TestOrchestrator_RetryOnConnectionFailure(t *testing.T) {
	env := newTestEnv(t)
	enableFolder(t, env, "INBOX")
	env.imapSrv.SeedMessages(t, "INBOX", 5) // UIDs 1-5

	dialCount := 0
	env.orchestrator.dialFunc = func(_, _, _ string, _ bool, _ *imapwrap.ProxyConfig) (IMAPClient, error) {
		dialCount++
		realClient := connectTestIMAP(t, env.imapSrv)
		if dialCount <= 1 {
			// First connection: EOF after 2 messages.
			return &eofFetchClient{IMAPClient: realClient, succeedCount: 2}, nil
		}
		// Subsequent connections: work normally.
		return realClient, nil
	}

	result, err := env.orchestrator.Run(env.accountID, env.userID, nil)
	if err != nil {
		t.Fatal(err)
	}

	// All 5 messages should be stored across the two attempts.
	if result.NewMessages != 5 {
		t.Errorf("NewMessages = %d, want 5", result.NewMessages)
	}

	// Should have dialed at least twice (initial + one reconnect).
	if dialCount < 2 {
		t.Errorf("dialCount = %d, want >= 2", dialCount)
	}
}

// Test: Run() stops retrying when no progress is made.
func TestOrchestrator_RetryStopsWithoutProgress(t *testing.T) {
	env := newTestEnv(t)
	enableFolder(t, env, "INBOX")
	env.imapSrv.SeedMessages(t, "INBOX", 3)

	dialCount := 0
	env.orchestrator.dialFunc = func(_, _, _ string, _ bool, _ *imapwrap.ProxyConfig) (IMAPClient, error) {
		dialCount++
		realClient := connectTestIMAP(t, env.imapSrv)
		// Every connection: EOF before any messages are received.
		return &eofFetchClient{IMAPClient: realClient, succeedCount: 0}, nil
	}

	result, err := env.orchestrator.Run(env.accountID, env.userID, nil)
	if err != nil {
		t.Fatal(err)
	}

	// No messages should be stored.
	if result.NewMessages != 0 {
		t.Errorf("NewMessages = %d, want 0", result.NewMessages)
	}

	// Dialed twice: first attempt fetches envelopes (counts as progress), retries.
	// Second attempt has cached envelopes (no new envelopes) and bodies still fail,
	// so no progress is made and it gives up.
	if dialCount != 2 {
		t.Errorf("dialCount = %d, want 2 (retry once for envelope progress, then stop)", dialCount)
	}

	// Should have errors reported.
	if len(result.Errors) == 0 {
		t.Error("expected errors to be reported")
	}
}

// Test: Run() retries when envelope fetching makes partial progress.
// First connection returns only 2 of 5 envelopes + EOF, second works normally.
func TestOrchestrator_EnvelopeFetchEOF_PartialRetry(t *testing.T) {
	env := newTestEnv(t)
	enableFolder(t, env, "INBOX")
	env.imapSrv.SeedMessages(t, "INBOX", 5) // UIDs 1-5

	dialCount := 0
	env.orchestrator.dialFunc = func(_, _, _ string, _ bool, _ *imapwrap.ProxyConfig) (IMAPClient, error) {
		dialCount++
		realClient := connectTestIMAP(t, env.imapSrv)
		if dialCount <= 1 {
			// First connection: envelope EOF after 2 envelopes.
			return &eofEnvelopeClient{IMAPClient: realClient, envelopeLimit: 2}, nil
		}
		// Subsequent connections: work normally.
		return realClient, nil
	}

	result, err := env.orchestrator.Run(env.accountID, env.userID, nil)
	if err != nil {
		t.Fatal(err)
	}

	// All 5 messages should be stored across the two attempts.
	if result.NewMessages != 5 {
		t.Errorf("NewMessages = %d, want 5", result.NewMessages)
	}

	// Should have dialed at least twice (initial + one reconnect).
	if dialCount < 2 {
		t.Errorf("dialCount = %d, want >= 2", dialCount)
	}
}

// Test: Run() stops retrying when envelope fetching makes no progress.
func TestOrchestrator_EnvelopeFetchEOF_NoProgress(t *testing.T) {
	env := newTestEnv(t)
	enableFolder(t, env, "INBOX")
	env.imapSrv.SeedMessages(t, "INBOX", 3)

	dialCount := 0
	env.orchestrator.dialFunc = func(_, _, _ string, _ bool, _ *imapwrap.ProxyConfig) (IMAPClient, error) {
		dialCount++
		realClient := connectTestIMAP(t, env.imapSrv)
		// Every connection: 0 envelopes before EOF.
		return &eofEnvelopeClient{IMAPClient: realClient, envelopeLimit: 0}, nil
	}

	result, err := env.orchestrator.Run(env.accountID, env.userID, nil)
	if err != nil {
		t.Fatal(err)
	}

	// No messages should be stored.
	if result.NewMessages != 0 {
		t.Errorf("NewMessages = %d, want 0", result.NewMessages)
	}

	// Should have dialed exactly once — no progress, no retry.
	if dialCount != 1 {
		t.Errorf("dialCount = %d, want 1 (should not retry without progress)", dialCount)
	}

	// Should have errors reported.
	if len(result.Errors) == 0 {
		t.Error("expected errors to be reported")
	}
}
