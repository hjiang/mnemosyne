package accounts

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/hjiang/mnemosyne/internal/db"
)

type testEnv struct {
	repo   *Repo
	userA  int64
	userB  int64
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

	km, err := NewKeyManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Create two users for isolation tests.
	database.Exec("INSERT INTO users (email, password_hash, created_at) VALUES (?, ?, ?)", "a@test.com", "h", 0) //nolint:errcheck,gosec
	database.Exec("INSERT INTO users (email, password_hash, created_at) VALUES (?, ?, ?)", "b@test.com", "h", 0) //nolint:errcheck,gosec

	return &testEnv{
		repo:  NewRepo(database, km),
		userA: 1,
		userB: 2,
	}
}

func TestCreate_And_GetByID(t *testing.T) {
	env := newTestEnv(t)

	acct, err := env.repo.Create(env.userA, "Gmail", "imap.gmail.com", 993, "alice", "pass123", true)
	if err != nil {
		t.Fatal(err)
	}
	if acct.ID == 0 {
		t.Error("expected non-zero ID")
	}
	if acct.Password != "pass123" {
		t.Errorf("Password = %q, want %q", acct.Password, "pass123")
	}

	got, err := env.repo.GetByID(acct.ID, env.userA)
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "Gmail" {
		t.Errorf("Label = %q, want %q", got.Label, "Gmail")
	}
	if got.Password != "pass123" {
		t.Errorf("Password = %q, want %q (decrypted)", got.Password, "pass123")
	}
	if !got.UseTLS {
		t.Error("expected UseTLS = true")
	}
}

// isolation — 100% coverage required
func TestList_UserIsolation(t *testing.T) {
	env := newTestEnv(t)

	_, err := env.repo.Create(env.userA, "A's account", "host", 993, "a", "pass", true)
	if err != nil {
		t.Fatal(err)
	}

	// User B should see nothing.
	list, err := env.repo.List(env.userB)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Errorf("user B sees %d accounts, want 0", len(list))
	}

	// User A sees their account.
	list, err = env.repo.List(env.userA)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Errorf("user A sees %d accounts, want 1", len(list))
	}
}

// isolation — 100% coverage required
func TestGetByID_WrongUser(t *testing.T) {
	env := newTestEnv(t)

	acct, err := env.repo.Create(env.userA, "A's account", "host", 993, "a", "pass", true)
	if err != nil {
		t.Fatal(err)
	}

	_, err = env.repo.GetByID(acct.ID, env.userB)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for wrong user, got %v", err)
	}
}

func TestFolderCRUD(t *testing.T) {
	env := newTestEnv(t)

	acct, err := env.repo.Create(env.userA, "Test", "host", 993, "a", "pass", true)
	if err != nil {
		t.Fatal(err)
	}

	folder, err := env.repo.CreateFolder(acct.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	if folder.Name != "INBOX" {
		t.Errorf("Name = %q, want %q", folder.Name, "INBOX")
	}

	// List folders.
	folders, err := env.repo.ListFolders(acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(folders) != 1 {
		t.Fatalf("len(folders) = %d, want 1", len(folders))
	}
	if folders[0].Name != "INBOX" {
		t.Errorf("folder name = %q, want %q", folders[0].Name, "INBOX")
	}
	if folders[0].Enabled {
		t.Error("folder should be disabled by default")
	}
}

func TestSetFolderEnabled(t *testing.T) {
	env := newTestEnv(t)
	acct, _ := env.repo.Create(env.userA, "Test", "host", 993, "a", "pass", true)
	folder, _ := env.repo.CreateFolder(acct.ID, "INBOX")

	if err := env.repo.SetFolderEnabled(folder.ID, true); err != nil {
		t.Fatal(err)
	}

	folders, _ := env.repo.ListFolders(acct.ID)
	if !folders[0].Enabled {
		t.Error("expected folder to be enabled")
	}
}

func TestSetUIDValidity(t *testing.T) {
	env := newTestEnv(t)
	acct, _ := env.repo.Create(env.userA, "Test", "host", 993, "a", "pass", true)
	folder, _ := env.repo.CreateFolder(acct.ID, "INBOX")

	if err := env.repo.SetUIDValidity(folder.ID, 12345); err != nil {
		t.Fatal(err)
	}

	folders, _ := env.repo.ListFolders(acct.ID)
	if folders[0].UIDValidity == nil || *folders[0].UIDValidity != 12345 {
		t.Errorf("UIDValidity = %v, want 12345", folders[0].UIDValidity)
	}
}

func TestSetLastSeenUID(t *testing.T) {
	env := newTestEnv(t)
	acct, _ := env.repo.Create(env.userA, "Test", "host", 993, "a", "pass", true)
	folder, _ := env.repo.CreateFolder(acct.ID, "INBOX")

	if err := env.repo.SetLastSeenUID(folder.ID, 42); err != nil {
		t.Fatal(err)
	}

	folders, _ := env.repo.ListFolders(acct.ID)
	if folders[0].LastSeenUID != 42 {
		t.Errorf("LastSeenUID = %d, want 42", folders[0].LastSeenUID)
	}
}

func TestSetLastSyncAt(t *testing.T) {
	env := newTestEnv(t)
	acct, _ := env.repo.Create(env.userA, "Test", "host", 993, "a", "pass", true)

	if err := env.repo.SetLastSyncAt(acct.ID, 1712000000); err != nil {
		t.Fatal(err)
	}

	got, _ := env.repo.GetByID(acct.ID, env.userA)
	if got.LastSyncAt == nil || *got.LastSyncAt != 1712000000 {
		t.Errorf("LastSyncAt = %v, want 1712000000", got.LastSyncAt)
	}
}

func TestCreate_NoTLS(t *testing.T) {
	env := newTestEnv(t)
	acct, err := env.repo.Create(env.userA, "NoTLS", "host", 143, "a", "pass", false)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := env.repo.GetByID(acct.ID, env.userA)
	if got.UseTLS {
		t.Error("expected UseTLS = false")
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
	km, _ := NewKeyManager(dir)
	_ = database.Close() // Close to trigger errors.

	repo := NewRepo(database, km)

	_, err = repo.Create(1, "x", "h", 993, "u", "p", true)
	if err == nil {
		t.Error("expected Create error on closed DB")
	}

	_, err = repo.GetByID(1, 1)
	if err == nil {
		t.Error("expected GetByID error on closed DB")
	}

	_, err = repo.List(1)
	if err == nil {
		t.Error("expected List error on closed DB")
	}

	_, err = repo.CreateFolder(1, "INBOX")
	if err == nil {
		t.Error("expected CreateFolder error on closed DB")
	}

	_, err = repo.ListFolders(1)
	if err == nil {
		t.Error("expected ListFolders error on closed DB")
	}

	if err := repo.SetFolderEnabled(1, true); err == nil {
		t.Error("expected SetFolderEnabled error on closed DB")
	}
	if err := repo.SetUIDValidity(1, 1); err == nil {
		t.Error("expected SetUIDValidity error on closed DB")
	}
	if err := repo.SetLastSeenUID(1, 1); err == nil {
		t.Error("expected SetLastSeenUID error on closed DB")
	}
	if err := repo.SetLastSyncAt(1, 1); err == nil {
		t.Error("expected SetLastSyncAt error on closed DB")
	}
}

func TestCreateFolder_Idempotent(t *testing.T) {
	env := newTestEnv(t)
	acct, _ := env.repo.Create(env.userA, "Test", "host", 993, "a", "pass", true)

	_, err := env.repo.CreateFolder(acct.ID, "INBOX")
	if err != nil {
		t.Fatal(err)
	}
	// Second create should not error (ON CONFLICT DO NOTHING).
	_, err = env.repo.CreateFolder(acct.ID, "INBOX")
	if err != nil {
		t.Fatalf("expected idempotent create, got %v", err)
	}

	folders, _ := env.repo.ListFolders(acct.ID)
	if len(folders) != 1 {
		t.Errorf("expected 1 folder after duplicate create, got %d", len(folders))
	}
}
