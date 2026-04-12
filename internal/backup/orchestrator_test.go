package backup

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hjiang/mnemosyne/internal/accounts"
	"github.com/hjiang/mnemosyne/internal/blobs"
	"github.com/hjiang/mnemosyne/internal/db"
	"github.com/hjiang/mnemosyne/internal/messages"
	"github.com/hjiang/mnemosyne/internal/testimap"
)

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

	acct, err := acctRepo.Create(1, "test", host, port, srv.Username, srv.Password, false)
	if err != nil {
		t.Fatal(err)
	}

	orch := NewOrchestrator(acctRepo, msgRepo, store)

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

	result, err := env.orchestrator.Run(env.accountID, env.userID)
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

	if _, err := env.orchestrator.Run(env.accountID, env.userID); err != nil {
		t.Fatal(err)
	}

	result, err := env.orchestrator.Run(env.accountID, env.userID)
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

	if _, err := env.orchestrator.Run(env.accountID, env.userID); err != nil {
		t.Fatal(err)
	}

	// Seed 3 more unique messages (different from the first batch).
	for i := 4; i <= 6; i++ {
		raw := fmt.Sprintf("From: sender%d@test.com\r\nTo: rcpt@test.com\r\nSubject: Test message %d\r\nMessage-ID: <msg%d@test>\r\nDate: Mon, 01 Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nBody of message %d\r\n", i, i, i, i)
		env.imapSrv.AppendMessage(t, "INBOX", []byte(raw))
	}

	result, err := env.orchestrator.Run(env.accountID, env.userID)
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

	result, err := env.orchestrator.Run(env.accountID, env.userID)
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

	if _, err := env.orchestrator.Run(env.accountID, env.userID); err != nil {
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

	result, err := env.orchestrator.Run(env.accountID, env.userID)
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

	result, err := env.orchestrator.Run(env.accountID, env.userID)
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

	result, err := env.orchestrator.Run(env.accountID, env.userID)
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
	orch := NewOrchestrator(acctRepo, msgRepo, store)

	// User A's server and account (unique messages for user A).
	srvA := testimap.New(t)
	srvA.AddFolder(t, "INBOX", 1)
	for i := 1; i <= 3; i++ {
		raw := fmt.Sprintf("From: alice%d@a.com\r\nTo: rcpt@a.com\r\nSubject: A msg %d\r\nMessage-ID: <a-msg%d@a>\r\nDate: Mon, 01 Jan 2024 00:00:00 +0000\r\nMIME-Version: 1.0\r\nContent-Type: text/plain\r\n\r\nUser A body %d\r\n", i, i, i, i)
		srvA.AppendMessage(t, "INBOX", []byte(raw))
	}
	hostA, portStrA, _ := net.SplitHostPort(srvA.Addr)
	portA, _ := strconv.Atoi(portStrA)
	acctA, _ := acctRepo.Create(1, "A", hostA, portA, srvA.Username, srvA.Password, false)
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
	acctB, _ := acctRepo.Create(2, "B", hostB, portB, srvB.Username, srvB.Password, false)
	folderB, _ := acctRepo.CreateFolder(acctB.ID, "INBOX")
	_ = acctRepo.SetFolderEnabled(folderB.ID, true)

	if _, err := orch.Run(acctA.ID, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := orch.Run(acctB.ID, 2); err != nil {
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

	result, err := env.orchestrator.Run(env.accountID, env.userID)
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

	result, err := env.orchestrator.Run(env.accountID, env.userID)
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

	_, err := env.orchestrator.Run(env.accountID, env.userID)
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

	_, err := env.orchestrator.Run(env.accountID, env.userID)
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

	result, err := env.orchestrator.Run(env.accountID, env.userID)
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
	if _, err := env.orchestrator.Run(env.accountID, env.userID); err != nil {
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
	_, err := env.orchestrator.Run(9999, env.userID)
	if err == nil {
		t.Fatal("expected error for nonexistent account")
	}
}
