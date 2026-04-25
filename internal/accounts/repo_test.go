package accounts

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/hjiang/mnemosyne/internal/db"
)

type testEnv struct {
	repo  *Repo
	userA int64
	userB int64
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

	acct, err := env.repo.Create(env.userA, "Gmail", "imap.gmail.com", 993, "alice", "pass123", true, "", 0, "", "")
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

	_, err := env.repo.Create(env.userA, "A's account", "host", 993, "a", "pass", true, "", 0, "", "")
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

	acct, err := env.repo.Create(env.userA, "A's account", "host", 993, "a", "pass", true, "", 0, "", "")
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

	acct, err := env.repo.Create(env.userA, "Test", "host", 993, "a", "pass", true, "", 0, "", "")
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
	acct, _ := env.repo.Create(env.userA, "Test", "host", 993, "a", "pass", true, "", 0, "", "")
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
	acct, _ := env.repo.Create(env.userA, "Test", "host", 993, "a", "pass", true, "", 0, "", "")
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
	acct, _ := env.repo.Create(env.userA, "Test", "host", 993, "a", "pass", true, "", 0, "", "")
	folder, _ := env.repo.CreateFolder(acct.ID, "INBOX")

	if err := env.repo.SetLastSeenUID(folder.ID, 42); err != nil {
		t.Fatal(err)
	}

	folders, _ := env.repo.ListFolders(acct.ID)
	if folders[0].LastSeenUID != 42 {
		t.Errorf("LastSeenUID = %d, want 42", folders[0].LastSeenUID)
	}
}

func TestSetLastSweptUID(t *testing.T) {
	env := newTestEnv(t)
	acct, _ := env.repo.Create(env.userA, "Test", "host", 993, "a", "pass", true, "", 0, "", "")
	folder, _ := env.repo.CreateFolder(acct.ID, "INBOX")

	if folder.LastSweptUID != 0 {
		t.Errorf("initial LastSweptUID = %d, want 0", folder.LastSweptUID)
	}

	if err := env.repo.SetLastSweptUID(folder.ID, 1234); err != nil {
		t.Fatal(err)
	}

	folders, _ := env.repo.ListFolders(acct.ID)
	if folders[0].LastSweptUID != 1234 {
		t.Errorf("LastSweptUID = %d, want 1234", folders[0].LastSweptUID)
	}

	if err := env.repo.SetLastSweptUID(folder.ID, 0); err != nil {
		t.Fatal(err)
	}
	got, _ := env.repo.GetFolderByID(folder.ID, env.userA)
	if got.LastSweptUID != 0 {
		t.Errorf("LastSweptUID after reset = %d, want 0", got.LastSweptUID)
	}
}

func TestResetCursors(t *testing.T) {
	env := newTestEnv(t)
	acct, _ := env.repo.Create(env.userA, "Test", "host", 993, "a", "pass", true, "", 0, "", "")
	folder, _ := env.repo.CreateFolder(acct.ID, "INBOX")

	if err := env.repo.SetLastSeenUID(folder.ID, 500); err != nil {
		t.Fatal(err)
	}
	if err := env.repo.SetLastSweptUID(folder.ID, 100); err != nil {
		t.Fatal(err)
	}

	if err := env.repo.ResetCursors(folder.ID); err != nil {
		t.Fatal(err)
	}

	got, err := env.repo.GetFolderByID(folder.ID, env.userA)
	if err != nil {
		t.Fatal(err)
	}
	if got.LastSeenUID != 0 {
		t.Errorf("LastSeenUID = %d, want 0", got.LastSeenUID)
	}
	if got.LastSweptUID != 0 {
		t.Errorf("LastSweptUID = %d, want 0", got.LastSweptUID)
	}
}

func TestSetLastSyncAt(t *testing.T) {
	env := newTestEnv(t)
	acct, _ := env.repo.Create(env.userA, "Test", "host", 993, "a", "pass", true, "", 0, "", "")

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
	acct, err := env.repo.Create(env.userA, "NoTLS", "host", 143, "a", "pass", false, "", 0, "", "")
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

	_, err = repo.Create(1, "x", "h", 993, "u", "p", true, "", 0, "", "")
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
	if err := repo.SetFolderPolicy(1, `{"leave_on_server":"all"}`); err == nil {
		t.Error("expected SetFolderPolicy error on closed DB")
	}

	if err := repo.Update(1, 1, "x", "h", 993, "u", "p", true, "", 0, "", ""); err == nil {
		t.Error("expected Update error on closed DB")
	}
}

func TestSetFolderPolicy(t *testing.T) {
	env := newTestEnv(t)
	acct, _ := env.repo.Create(env.userA, "Test", "host", 993, "a", "pass", true, "", 0, "", "")
	folder, _ := env.repo.CreateFolder(acct.ID, "INBOX")

	newPolicy := `{"leave_on_server":"newest_n","n":50}`
	if err := env.repo.SetFolderPolicy(folder.ID, newPolicy); err != nil {
		t.Fatal(err)
	}

	folders, _ := env.repo.ListFolders(acct.ID)
	if folders[0].PolicyJSON != newPolicy {
		t.Errorf("PolicyJSON = %q, want %q", folders[0].PolicyJSON, newPolicy)
	}
}

func TestCreateFolder_Idempotent(t *testing.T) {
	env := newTestEnv(t)
	acct, _ := env.repo.Create(env.userA, "Test", "host", 993, "a", "pass", true, "", 0, "", "")

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

func TestCreate_WithProxy(t *testing.T) {
	env := newTestEnv(t)

	acct, err := env.repo.Create(env.userA, "Corp", "imap.corp.com", 993, "user", "pass", true,
		"proxy.corp.com", 1080, "proxyuser", "proxypass")
	if err != nil {
		t.Fatal(err)
	}
	if acct.ProxyHost != "proxy.corp.com" {
		t.Errorf("ProxyHost = %q, want %q", acct.ProxyHost, "proxy.corp.com")
	}
	if acct.ProxyPort != 1080 {
		t.Errorf("ProxyPort = %d, want 1080", acct.ProxyPort)
	}

	got, err := env.repo.GetByID(acct.ID, env.userA)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProxyHost != "proxy.corp.com" {
		t.Errorf("ProxyHost = %q, want %q", got.ProxyHost, "proxy.corp.com")
	}
	if got.ProxyPort != 1080 {
		t.Errorf("ProxyPort = %d, want 1080", got.ProxyPort)
	}
	if got.ProxyUsername != "proxyuser" {
		t.Errorf("ProxyUsername = %q, want %q", got.ProxyUsername, "proxyuser")
	}
	if got.ProxyPassword != "proxypass" {
		t.Errorf("ProxyPassword = %q, want %q (decrypted)", got.ProxyPassword, "proxypass")
	}
}

func TestCreate_WithoutProxy(t *testing.T) {
	env := newTestEnv(t)

	acct, err := env.repo.Create(env.userA, "Personal", "imap.gmail.com", 993, "user", "pass", true,
		"", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}

	got, err := env.repo.GetByID(acct.ID, env.userA)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProxyHost != "" {
		t.Errorf("ProxyHost = %q, want empty", got.ProxyHost)
	}
	if got.ProxyPassword != "" {
		t.Errorf("ProxyPassword = %q, want empty", got.ProxyPassword)
	}
}

func TestList_WithProxy(t *testing.T) {
	env := newTestEnv(t)

	_, err := env.repo.Create(env.userA, "WithProxy", "host", 993, "u", "p", true,
		"proxy.example.com", 1080, "pu", "pp")
	if err != nil {
		t.Fatal(err)
	}

	accounts, err := env.repo.List(env.userA)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accounts))
	}
	if accounts[0].ProxyHost != "proxy.example.com" {
		t.Errorf("ProxyHost = %q, want %q", accounts[0].ProxyHost, "proxy.example.com")
	}
	if accounts[0].ProxyPassword != "pp" {
		t.Errorf("ProxyPassword = %q, want %q", accounts[0].ProxyPassword, "pp")
	}
}

func TestUpdate(t *testing.T) {
	env := newTestEnv(t)

	acct, err := env.repo.Create(env.userA, "Old Label", "old.host.com", 993, "olduser", "oldpass", true, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}

	err = env.repo.Update(acct.ID, env.userA, "New Label", "new.host.com", 143, "newuser", "newpass", false, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}

	got, err := env.repo.GetByID(acct.ID, env.userA)
	if err != nil {
		t.Fatal(err)
	}
	if got.Label != "New Label" {
		t.Errorf("Label = %q, want %q", got.Label, "New Label")
	}
	if got.Host != "new.host.com" {
		t.Errorf("Host = %q, want %q", got.Host, "new.host.com")
	}
	if got.Port != 143 {
		t.Errorf("Port = %d, want 143", got.Port)
	}
	if got.Username != "newuser" {
		t.Errorf("Username = %q, want %q", got.Username, "newuser")
	}
	if got.Password != "newpass" {
		t.Errorf("Password = %q, want %q", got.Password, "newpass")
	}
	if got.UseTLS {
		t.Error("expected UseTLS = false")
	}
}

// isolation — 100% coverage required
func TestUpdate_UserIsolation(t *testing.T) {
	env := newTestEnv(t)

	acct, err := env.repo.Create(env.userA, "A's account", "host", 993, "a", "pass", true, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}

	err = env.repo.Update(acct.ID, env.userB, "Hacked", "evil.com", 993, "hacker", "hacked", true, "", 0, "", "")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for wrong user, got %v", err)
	}

	// Verify original values unchanged.
	got, _ := env.repo.GetByID(acct.ID, env.userA)
	if got.Label != "A's account" {
		t.Errorf("Label = %q, want %q (should be unchanged)", got.Label, "A's account")
	}
}

func TestUpdate_WithProxy(t *testing.T) {
	env := newTestEnv(t)

	acct, err := env.repo.Create(env.userA, "Test", "host", 993, "u", "p", true, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}

	// Add proxy settings via update.
	err = env.repo.Update(acct.ID, env.userA, "Test", "host", 993, "u", "p", true,
		"proxy.example.com", 1080, "pu", "pp")
	if err != nil {
		t.Fatal(err)
	}

	got, err := env.repo.GetByID(acct.ID, env.userA)
	if err != nil {
		t.Fatal(err)
	}
	if got.ProxyHost != "proxy.example.com" {
		t.Errorf("ProxyHost = %q, want %q", got.ProxyHost, "proxy.example.com")
	}
	if got.ProxyPort != 1080 {
		t.Errorf("ProxyPort = %d, want 1080", got.ProxyPort)
	}
	if got.ProxyUsername != "pu" {
		t.Errorf("ProxyUsername = %q, want %q", got.ProxyUsername, "pu")
	}
	if got.ProxyPassword != "pp" {
		t.Errorf("ProxyPassword = %q, want %q", got.ProxyPassword, "pp")
	}

	// Remove proxy settings via update.
	err = env.repo.Update(acct.ID, env.userA, "Test", "host", 993, "u", "p", true, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}

	got, _ = env.repo.GetByID(acct.ID, env.userA)
	if got.ProxyHost != "" {
		t.Errorf("ProxyHost = %q, want empty", got.ProxyHost)
	}
	if got.ProxyPassword != "" {
		t.Errorf("ProxyPassword = %q, want empty", got.ProxyPassword)
	}
}

func TestIsOAuth_UnrecognizedAuthType(t *testing.T) {
	// An account with a typo'd or unknown auth_type should NOT be treated as OAuth.
	a := &Account{AuthType: "oauth_github"}
	if a.IsOAuth() {
		t.Errorf("IsOAuth() = true for unrecognized auth_type %q, want false", a.AuthType)
	}
}

func TestIsOAuth_RecognizedTypes(t *testing.T) {
	tests := []struct {
		authType string
		want     bool
	}{
		{"", false},
		{"password", false},
		{"oauth_google", true},
	}
	for _, tt := range tests {
		a := &Account{AuthType: tt.authType}
		if got := a.IsOAuth(); got != tt.want {
			t.Errorf("IsOAuth() for %q = %v, want %v", tt.authType, got, tt.want)
		}
	}
}

func TestCreateOAuth_RejectsInvalidAuthType(t *testing.T) {
	env := newTestEnv(t)

	_, err := env.repo.CreateOAuth(env.userA, "Bad", "user@example.com", "oauth_github", "refresh", "access", 9999)
	if err == nil {
		t.Fatal("expected error for unsupported auth type")
	}
	if !errors.Is(err, ErrUnsupportedAuthType) {
		t.Errorf("error = %v, want ErrUnsupportedAuthType", err)
	}
}

func TestCreateOAuth_AcceptsValidAuthType(t *testing.T) {
	env := newTestEnv(t)

	acct, err := env.repo.CreateOAuth(env.userA, "Google", "user@example.com", "oauth_google", "refresh", "access", 9999)
	if err != nil {
		t.Fatal(err)
	}
	if acct.AuthType != "oauth_google" {
		t.Errorf("AuthType = %q, want %q", acct.AuthType, "oauth_google")
	}
}

func TestList_OmitsOAuthTokens(t *testing.T) {
	env := newTestEnv(t)

	_, err := env.repo.CreateOAuth(env.userA, "Google", "user@example.com", "oauth_google", "secret-refresh", "secret-access", 9999)
	if err != nil {
		t.Fatal(err)
	}

	accts, err := env.repo.List(env.userA)
	if err != nil {
		t.Fatal(err)
	}
	if len(accts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(accts))
	}
	if accts[0].RefreshToken != "" {
		t.Errorf("List should not decrypt RefreshToken, got %q", accts[0].RefreshToken)
	}
	if accts[0].AccessToken != "" {
		t.Errorf("List should not decrypt AccessToken, got %q", accts[0].AccessToken)
	}
	// AuthType and other metadata should still be present.
	if accts[0].AuthType != "oauth_google" {
		t.Errorf("AuthType = %q, want %q", accts[0].AuthType, "oauth_google")
	}
}

func TestMarkFoldersOffServer(t *testing.T) {
	env := newTestEnv(t)
	acct, _ := env.repo.Create(env.userA, "Test", "host", 993, "a", "pass", true, "", 0, "", "")

	env.repo.CreateFolder(acct.ID, "INBOX")  //nolint:errcheck,gosec
	env.repo.CreateFolder(acct.ID, "Sent")   //nolint:errcheck,gosec
	env.repo.CreateFolder(acct.ID, "Drafts") //nolint:errcheck,gosec

	// Mark "Sent" as no longer on server.
	if err := env.repo.MarkFoldersOffServer(acct.ID, []string{"INBOX", "Drafts"}); err != nil {
		t.Fatal(err)
	}

	// ListFolders returns all (including off-server).
	all, err := env.repo.ListFolders(acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("ListFolders: got %d, want 3", len(all))
	}

	// ListActiveFolders excludes off-server.
	active, err := env.repo.ListActiveFolders(acct.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 2 {
		t.Fatalf("ListActiveFolders: got %d, want 2", len(active))
	}
	for _, f := range active {
		if f.Name == "Sent" {
			t.Error("ListActiveFolders should not include off-server folder 'Sent'")
		}
	}
}

func TestCreateFolder_RestoresOnServer(t *testing.T) {
	env := newTestEnv(t)
	acct, _ := env.repo.Create(env.userA, "Test", "host", 993, "a", "pass", true, "", 0, "", "")

	env.repo.CreateFolder(acct.ID, "INBOX") //nolint:errcheck,gosec

	// Mark all folders off-server.
	if err := env.repo.MarkFoldersOffServer(acct.ID, []string{}); err != nil {
		t.Fatal(err)
	}

	active, _ := env.repo.ListActiveFolders(acct.ID)
	if len(active) != 0 {
		t.Fatalf("expected 0 active folders, got %d", len(active))
	}

	// Re-creating the folder should restore on_server.
	env.repo.CreateFolder(acct.ID, "INBOX") //nolint:errcheck,gosec

	active, _ = env.repo.ListActiveFolders(acct.ID)
	if len(active) != 1 {
		t.Fatalf("expected 1 active folder after re-create, got %d", len(active))
	}
	if active[0].Name != "INBOX" {
		t.Errorf("expected INBOX, got %q", active[0].Name)
	}
}

func TestDelete(t *testing.T) {
	env := newTestEnv(t)
	acct, _ := env.repo.Create(env.userA, "Test", "host", 993, "a", "pass", true, "", 0, "", "")

	if err := env.repo.Delete(acct.ID, env.userA); err != nil {
		t.Fatal(err)
	}

	_, err := env.repo.GetByID(acct.ID, env.userA)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestDelete_UserIsolation(t *testing.T) {
	env := newTestEnv(t)
	acct, _ := env.repo.Create(env.userA, "Test", "host", 993, "a", "pass", true, "", 0, "", "")

	// userB should not be able to delete userA's account.
	err := env.repo.Delete(acct.ID, env.userB)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for wrong user, got %v", err)
	}

	// Account should still exist for userA.
	_, err = env.repo.GetByID(acct.ID, env.userA)
	if err != nil {
		t.Errorf("account should still exist, got %v", err)
	}
}
