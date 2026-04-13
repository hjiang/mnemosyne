package messages

import (
	"crypto/sha256"
	"errors"
	"path/filepath"
	"testing"

	"github.com/hjiang/mnemosyne/internal/db"
)

func newTestRepo(t *testing.T) *Repo {
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

	// Create two users and an account+folder for testing.
	database.Exec("INSERT INTO users (email, password_hash, created_at) VALUES (?, ?, ?)", "a@test.com", "h", 0) //nolint:errcheck,gosec
	database.Exec("INSERT INTO users (email, password_hash, created_at) VALUES (?, ?, ?)", "b@test.com", "h", 0) //nolint:errcheck,gosec
	database.Exec("INSERT INTO imap_accounts (user_id, label, host, port, username, password_enc, use_tls) VALUES (1, 'test', 'h', 993, 'u', x'00', 1)") //nolint:errcheck,gosec
	database.Exec("INSERT INTO imap_folders (account_id, name) VALUES (1, 'INBOX')")                                                                      //nolint:errcheck,gosec
	database.Exec("INSERT INTO imap_folders (account_id, name) VALUES (1, 'Archive')")                                                                    //nolint:errcheck,gosec

	return NewRepo(database)
}

func testHash(data string) []byte {
	h := sha256.Sum256([]byte(data))
	return h[:]
}

func TestInsert_And_GetByHash(t *testing.T) {
	repo := newTestRepo(t)
	hash := testHash("msg1")
	date := int64(1700000000)

	err := repo.Insert(&Message{
		Hash: hash, UserID: 1, MessageID: "<msg1@test>",
		FromAddr: "alice@test.com", ToAddrs: "bob@test.com",
		Subject: "Hello", Date: &date, Size: 100,
	})
	if err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetByHash(hash, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Subject != "Hello" {
		t.Errorf("Subject = %q, want %q", got.Subject, "Hello")
	}
	if got.FromAddr != "alice@test.com" {
		t.Errorf("FromAddr = %q, want %q", got.FromAddr, "alice@test.com")
	}
}

func TestInsert_DedupByHash(t *testing.T) {
	repo := newTestRepo(t)
	hash := testHash("dedup")
	date := int64(1700000000)

	msg := &Message{
		Hash: hash, UserID: 1, MessageID: "<dup@test>",
		FromAddr: "a@test.com", Subject: "First", Date: &date, Size: 50,
	}
	if err := repo.Insert(msg); err != nil {
		t.Fatal(err)
	}

	// Second insert with same hash is a no-op.
	msg2 := &Message{
		Hash: hash, UserID: 1, MessageID: "<dup@test>",
		FromAddr: "a@test.com", Subject: "Second", Date: &date, Size: 50,
	}
	if err := repo.Insert(msg2); err != nil {
		t.Fatal(err)
	}

	got, _ := repo.GetByHash(hash, 1)
	if got.Subject != "First" {
		t.Errorf("Subject = %q, want %q (first insert wins)", got.Subject, "First")
	}
}

func TestInsertLocation(t *testing.T) {
	repo := newTestRepo(t)
	hash := testHash("loc-test")
	date := int64(1700000000)

	_ = repo.Insert(&Message{Hash: hash, UserID: 1, Subject: "test", Date: &date, Size: 10})

	loc := &Location{MessageHash: hash, FolderID: 1, UID: 1, InternalDate: &date, Flags: `\Seen`}
	if err := repo.InsertLocation(loc); err != nil {
		t.Fatal(err)
	}

	msgs, err := repo.ListByFolder(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("len(msgs) = %d, want 1", len(msgs))
	}
}

func TestLocationExistsByFolderAndUID(t *testing.T) {
	repo := newTestRepo(t)
	hash := testHash("loc-exists-test")
	date := int64(1700000000)
	_ = repo.Insert(&Message{Hash: hash, UserID: 1, Subject: "test", Date: &date, Size: 10})
	_ = repo.InsertLocation(&Location{MessageHash: hash, FolderID: 1, UID: 42})

	exists, err := repo.LocationExistsByFolderAndUID(1, 42)
	if err != nil {
		t.Fatal(err)
	}
	if !exists {
		t.Error("expected location to exist")
	}

	exists, err = repo.LocationExistsByFolderAndUID(1, 99)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("expected location to not exist for different UID")
	}

	exists, err = repo.LocationExistsByFolderAndUID(999, 42)
	if err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Error("expected location to not exist for different folder")
	}
}

func TestInsertLocation_FKViolation(t *testing.T) {
	repo := newTestRepo(t)
	fakeHash := testHash("nonexistent")

	loc := &Location{MessageHash: fakeHash, FolderID: 1, UID: 99}
	err := repo.InsertLocation(loc)
	if err == nil {
		t.Fatal("expected FK violation for nonexistent message hash")
	}
}

func TestListByFolder_ReverseDate(t *testing.T) {
	repo := newTestRepo(t)
	dates := []int64{1700000000, 1700000100, 1700000050}
	subjects := []string{"oldest", "newest", "middle"}

	for i, s := range subjects {
		hash := testHash(s)
		_ = repo.Insert(&Message{Hash: hash, UserID: 1, Subject: s, Date: &dates[i], Size: 10})
		_ = repo.InsertLocation(&Location{MessageHash: hash, FolderID: 1, UID: uint32(i + 1)})
	}

	msgs, err := repo.ListByFolder(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 3 {
		t.Fatalf("len = %d, want 3", len(msgs))
	}
	if msgs[0].Subject != "newest" {
		t.Errorf("first = %q, want newest", msgs[0].Subject)
	}
	if msgs[2].Subject != "oldest" {
		t.Errorf("last = %q, want oldest", msgs[2].Subject)
	}
}

// isolation — 100% coverage required
func TestGetByHash_UserIsolation(t *testing.T) {
	repo := newTestRepo(t)
	hash := testHash("isolated")
	date := int64(1700000000)

	_ = repo.Insert(&Message{Hash: hash, UserID: 1, Subject: "secret", Date: &date, Size: 10})

	// User 2 cannot see user 1's message.
	_, err := repo.GetByHash(hash, 2)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for wrong user, got %v", err)
	}
}

// isolation — 100% coverage required
func TestExistsByHash_UserIsolation(t *testing.T) {
	repo := newTestRepo(t)
	hash := testHash("exists-iso")
	date := int64(1700000000)

	_ = repo.Insert(&Message{Hash: hash, UserID: 1, Subject: "x", Date: &date, Size: 10})

	exists, _ := repo.ExistsByHash(hash, 1)
	if !exists {
		t.Error("expected true for owner")
	}
	exists, _ = repo.ExistsByHash(hash, 2)
	if exists {
		t.Error("expected false for other user")
	}
}

// isolation — 100% coverage required
func TestFindByMessageID_UserIsolation(t *testing.T) {
	repo := newTestRepo(t)
	date := int64(1700000000)

	_ = repo.Insert(&Message{Hash: testHash("mid-a"), UserID: 1, MessageID: "<shared@test>", Subject: "A", Date: &date, Size: 10})

	found, _ := repo.FindByMessageID("<shared@test>", 1)
	if len(found) != 1 {
		t.Errorf("user 1 found %d, want 1", len(found))
	}
	found, _ = repo.FindByMessageID("<shared@test>", 2)
	if len(found) != 0 {
		t.Errorf("user 2 found %d, want 0", len(found))
	}
}

// isolation — 100% coverage required
func TestListByFolder_UserIsolation(t *testing.T) {
	repo := newTestRepo(t)
	hash := testHash("folder-iso")
	date := int64(1700000000)

	_ = repo.Insert(&Message{Hash: hash, UserID: 1, Subject: "x", Date: &date, Size: 10})
	_ = repo.InsertLocation(&Location{MessageHash: hash, FolderID: 1, UID: 1})

	msgs, _ := repo.ListByFolder(1, 2)
	if len(msgs) != 0 {
		t.Errorf("user 2 sees %d messages in user 1's folder, want 0", len(msgs))
	}
}

func TestCountLocationsByHash(t *testing.T) {
	repo := newTestRepo(t)
	hash := testHash("multi-loc")
	date := int64(1700000000)

	_ = repo.Insert(&Message{Hash: hash, UserID: 1, Subject: "x", Date: &date, Size: 10})
	_ = repo.InsertLocation(&Location{MessageHash: hash, FolderID: 1, UID: 1})
	_ = repo.InsertLocation(&Location{MessageHash: hash, FolderID: 2, UID: 1})

	count, err := repo.CountLocationsByHash(hash)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
}

func TestDeleteLocationsByFolder(t *testing.T) {
	repo := newTestRepo(t)
	hash := testHash("del-loc")
	date := int64(1700000000)

	_ = repo.Insert(&Message{Hash: hash, UserID: 1, Subject: "x", Date: &date, Size: 10})
	_ = repo.InsertLocation(&Location{MessageHash: hash, FolderID: 1, UID: 1})

	if err := repo.DeleteLocationsByFolder(1); err != nil {
		t.Fatal(err)
	}

	count, _ := repo.CountLocationsByHash(hash)
	if count != 0 {
		t.Errorf("count after delete = %d, want 0", count)
	}
}

func TestInsertAttachment_And_List(t *testing.T) {
	repo := newTestRepo(t)
	msgHash := testHash("with-att")
	date := int64(1700000000)

	_ = repo.Insert(&Message{Hash: msgHash, UserID: 1, Subject: "x", Date: &date, Size: 10, HasAttachments: true})

	attHash := testHash("attachment-blob")
	err := repo.InsertAttachment(&Attachment{
		MessageHash: msgHash, Filename: "report.pdf",
		MimeType: "application/pdf", Size: 5000,
		BlobHash: attHash, TextExtracted: 0,
	})
	if err != nil {
		t.Fatal(err)
	}

	atts, err := repo.ListAttachments(msgHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(atts) != 1 {
		t.Fatalf("len(atts) = %d, want 1", len(atts))
	}
	if atts[0].Filename != "report.pdf" {
		t.Errorf("Filename = %q, want %q", atts[0].Filename, "report.pdf")
	}
	if atts[0].TextExtracted != 0 {
		t.Errorf("TextExtracted = %d, want 0", atts[0].TextExtracted)
	}
}

func TestGetAttachment(t *testing.T) {
	repo := newTestRepo(t)
	msgHash := testHash("att-get")
	date := int64(1700000000)

	_ = repo.Insert(&Message{Hash: msgHash, UserID: 1, Subject: "x", Date: &date, Size: 10, HasAttachments: true})

	attHash := testHash("att-blob")
	att := &Attachment{
		MessageHash: msgHash, Filename: "doc.pdf",
		MimeType: "application/pdf", Size: 1234,
		BlobHash: attHash, TextExtracted: 0,
	}
	if err := repo.InsertAttachment(att); err != nil {
		t.Fatal(err)
	}

	got, err := repo.GetAttachment(att.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Filename != "doc.pdf" {
		t.Errorf("Filename = %q, want %q", got.Filename, "doc.pdf")
	}
	if got.Size != 1234 {
		t.Errorf("Size = %d, want 1234", got.Size)
	}
}

func TestGetAttachment_UserIsolation(t *testing.T) {
	repo := newTestRepo(t)
	msgHash := testHash("att-iso")
	date := int64(1700000000)

	_ = repo.Insert(&Message{Hash: msgHash, UserID: 1, Subject: "x", Date: &date, Size: 10, HasAttachments: true})

	att := &Attachment{
		MessageHash: msgHash, Filename: "secret.pdf",
		MimeType: "application/pdf", Size: 100,
		BlobHash: testHash("att-iso-blob"), TextExtracted: 0,
	}
	if err := repo.InsertAttachment(att); err != nil {
		t.Fatal(err)
	}

	// User 2 cannot access user 1's attachment.
	_, err := repo.GetAttachment(att.ID, 2)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for wrong user, got %v", err)
	}
}

func TestGetAttachment_NotFound(t *testing.T) {
	repo := newTestRepo(t)

	_, err := repo.GetAttachment(9999, 1)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestRepo_DBErrors(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	_ = database.Close()

	repo := NewRepo(database)
	hash := testHash("err")
	date := int64(0)

	if err := repo.Insert(&Message{Hash: hash, UserID: 1, Date: &date, Size: 1}); err == nil {
		t.Error("expected Insert error on closed DB")
	}
	if _, err := repo.GetByHash(hash, 1); err == nil {
		t.Error("expected GetByHash error on closed DB")
	}
	if _, err := repo.ExistsByHash(hash, 1); err == nil {
		t.Error("expected ExistsByHash error on closed DB")
	}
	if _, err := repo.FindByMessageID("x", 1); err == nil {
		t.Error("expected FindByMessageID error on closed DB")
	}
	if _, err := repo.ListByFolder(1, 1); err == nil {
		t.Error("expected ListByFolder error on closed DB")
	}
	if err := repo.InsertLocation(&Location{MessageHash: hash, FolderID: 1, UID: 1}); err == nil {
		t.Error("expected InsertLocation error on closed DB")
	}
	if _, err := repo.CountLocationsByHash(hash); err == nil {
		t.Error("expected CountLocationsByHash error on closed DB")
	}
	if err := repo.DeleteLocationsByFolder(1); err == nil {
		t.Error("expected DeleteLocationsByFolder error on closed DB")
	}
	if err := repo.InsertAttachment(&Attachment{MessageHash: hash, Size: 1, BlobHash: hash}); err == nil {
		t.Error("expected InsertAttachment error on closed DB")
	}
	if _, err := repo.ListAttachments(hash); err == nil {
		t.Error("expected ListAttachments error on closed DB")
	}
	if _, err := repo.GetAttachment(1, 1); err == nil {
		t.Error("expected GetAttachment error on closed DB")
	}
}

func TestInsertLocation_Idempotent(t *testing.T) {
	repo := newTestRepo(t)
	hash := testHash("loc-idemp")
	date := int64(1700000000)

	_ = repo.Insert(&Message{Hash: hash, UserID: 1, Subject: "x", Date: &date, Size: 10})
	loc := &Location{MessageHash: hash, FolderID: 1, UID: 42}
	_ = repo.InsertLocation(loc)

	// Second insert with same folder+uid is a no-op.
	err := repo.InsertLocation(loc)
	if err != nil {
		t.Fatalf("expected idempotent insert, got %v", err)
	}
}

func TestListByFolderPaged(t *testing.T) {
	repo := newTestRepo(t)

	// Insert 5 messages with increasing dates into folder 1.
	for i := range 5 {
		hash := testHash("paged-" + string(rune('a'+i)))
		date := int64(1700000000 + i*100)
		_ = repo.Insert(&Message{Hash: hash, UserID: 1, Subject: "msg" + string(rune('a'+i)), Date: &date, Size: 10})
		_ = repo.InsertLocation(&Location{MessageHash: hash, FolderID: 1, UID: uint32(i + 1)})
	}

	// First page of 2.
	msgs, err := repo.ListByFolderPaged(1, 1, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("page 1: len = %d, want 2", len(msgs))
	}
	// Date DESC: newest first.
	if msgs[0].Subject != "msge" {
		t.Errorf("page 1 first = %q, want msge", msgs[0].Subject)
	}

	// Second page of 2.
	msgs, err = repo.ListByFolderPaged(1, 1, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 2 {
		t.Fatalf("page 2: len = %d, want 2", len(msgs))
	}

	// Third page: only 1 remaining.
	msgs, err = repo.ListByFolderPaged(1, 1, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("page 3: len = %d, want 1", len(msgs))
	}
}

func TestListByFolderPaged_UserIsolation(t *testing.T) {
	repo := newTestRepo(t)
	hash := testHash("paged-iso")
	date := int64(1700000000)

	_ = repo.Insert(&Message{Hash: hash, UserID: 1, Subject: "x", Date: &date, Size: 10})
	_ = repo.InsertLocation(&Location{MessageHash: hash, FolderID: 1, UID: 1})

	msgs, _ := repo.ListByFolderPaged(1, 2, 50, 0)
	if len(msgs) != 0 {
		t.Errorf("user 2 sees %d messages, want 0", len(msgs))
	}
}

func TestCountByFolder(t *testing.T) {
	repo := newTestRepo(t)
	for i := range 3 {
		hash := testHash("count-" + string(rune('a'+i)))
		date := int64(1700000000 + i)
		_ = repo.Insert(&Message{Hash: hash, UserID: 1, Subject: "x", Date: &date, Size: 10})
		_ = repo.InsertLocation(&Location{MessageHash: hash, FolderID: 1, UID: uint32(i + 1)})
	}

	count, err := repo.CountByFolder(1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}

	// Wrong user sees 0.
	count, _ = repo.CountByFolder(1, 2)
	if count != 0 {
		t.Errorf("user 2 count = %d, want 0", count)
	}
}

func TestCountByFoldersForUser(t *testing.T) {
	repo := newTestRepo(t)

	// 2 messages in folder 1, 1 in folder 2.
	for i := range 3 {
		hash := testHash("fcount-" + string(rune('a'+i)))
		date := int64(1700000000 + i)
		_ = repo.Insert(&Message{Hash: hash, UserID: 1, Subject: "x", Date: &date, Size: 10})
		folderID := int64(1)
		if i == 2 {
			folderID = 2
		}
		_ = repo.InsertLocation(&Location{MessageHash: hash, FolderID: folderID, UID: uint32(i + 1)})
	}

	counts, err := repo.CountByFoldersForUser(1)
	if err != nil {
		t.Fatal(err)
	}
	if counts[1] != 2 {
		t.Errorf("folder 1 count = %d, want 2", counts[1])
	}
	if counts[2] != 1 {
		t.Errorf("folder 2 count = %d, want 1", counts[2])
	}

	// User 2 has no messages.
	counts, _ = repo.CountByFoldersForUser(2)
	if len(counts) != 0 {
		t.Errorf("user 2 counts = %d entries, want 0", len(counts))
	}
}
