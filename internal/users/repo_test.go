package users

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hjiang/mnemosyne/internal/db"
)

func newTestRepo(t *testing.T) (*Repo, *sql.DB) {
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
	return NewRepo(database, time.Now), database
}

func TestCreate_And_GetByEmail(t *testing.T) {
	repo, _ := newTestRepo(t)

	u, err := repo.Create("alice@example.com", "hash123")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID == 0 {
		t.Error("expected non-zero ID")
	}
	if u.Email != "alice@example.com" {
		t.Errorf("Email = %q, want %q", u.Email, "alice@example.com")
	}

	found, err := repo.GetByEmail("alice@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if found.ID != u.ID {
		t.Errorf("ID = %d, want %d", found.ID, u.ID)
	}
	if found.PasswordHash != "hash123" {
		t.Errorf("PasswordHash = %q, want %q", found.PasswordHash, "hash123")
	}
}

func TestCreate_DuplicateEmail(t *testing.T) {
	repo, _ := newTestRepo(t)

	_, err := repo.Create("dup@example.com", "hash1")
	if err != nil {
		t.Fatal(err)
	}

	_, err = repo.Create("dup@example.com", "hash2")
	if !errors.Is(err, ErrDuplicateEmail) {
		t.Errorf("expected ErrDuplicateEmail, got %v", err)
	}
}

func TestCreate_DuplicateEmail_CaseInsensitive(t *testing.T) {
	repo, _ := newTestRepo(t)

	_, err := repo.Create("user@example.com", "hash1")
	if err != nil {
		t.Fatal(err)
	}

	_, err = repo.Create("USER@example.com", "hash2")
	if !errors.Is(err, ErrDuplicateEmail) {
		t.Errorf("expected ErrDuplicateEmail for case-insensitive dup, got %v", err)
	}
}

func TestGetByEmail_CaseInsensitive(t *testing.T) {
	repo, _ := newTestRepo(t)

	_, err := repo.Create("CamelCase@Example.COM", "hash")
	if err != nil {
		t.Fatal(err)
	}

	found, err := repo.GetByEmail("camelcase@example.com")
	if err != nil {
		t.Fatalf("case-insensitive lookup failed: %v", err)
	}
	if found.Email != "CamelCase@Example.COM" {
		t.Errorf("Email = %q, want original casing", found.Email)
	}
}

func TestGetByEmail_NotFound(t *testing.T) {
	repo, _ := newTestRepo(t)

	_, err := repo.GetByEmail("nobody@example.com")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestGetByID_Success(t *testing.T) {
	repo, _ := newTestRepo(t)

	created, err := repo.Create("byid@example.com", "hash")
	if err != nil {
		t.Fatal(err)
	}

	found, err := repo.GetByID(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Email != "byid@example.com" {
		t.Errorf("Email = %q, want %q", found.Email, "byid@example.com")
	}
}

func TestGetByID_NotFound(t *testing.T) {
	repo, _ := newTestRepo(t)

	_, err := repo.GetByID(99999)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestCreate_DBError(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	_ = database.Close()

	repo := NewRepo(database, time.Now)
	_, err = repo.Create("err@example.com", "hash")
	if err == nil {
		t.Fatal("expected error on closed DB")
	}
}

func TestGetByEmail_DBError(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	_ = database.Close()

	repo := NewRepo(database, time.Now)
	_, err = repo.GetByEmail("err@example.com")
	if err == nil {
		t.Fatal("expected error on closed DB")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("expected DB error, not ErrNotFound")
	}
}

func TestGetByID_DBError(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	_ = database.Close()

	repo := NewRepo(database, time.Now)
	_, err = repo.GetByID(1)
	if err == nil {
		t.Fatal("expected error on closed DB")
	}
	if errors.Is(err, ErrNotFound) {
		t.Error("expected DB error, not ErrNotFound")
	}
}
