package auth

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hjiang/mnemosyne/internal/db"
)

// newTestSessionStore returns a SessionStore backed by an in-memory-like temp
// SQLite with migrations applied, and a controllable clock.
func newTestSessionStore(t *testing.T) (*SessionStore, *fakeClock) {
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

	// Seed a user for FK references.
	_, err = database.Exec(
		"INSERT INTO users (email, password_hash, created_at) VALUES (?, ?, ?)",
		"test@example.com", "fakehash", time.Now().Unix(),
	)
	if err != nil {
		t.Fatal(err)
	}

	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	store := NewSessionStore(database, clock.Now, 1*time.Hour)
	return store, clock
}

type fakeClock struct {
	t time.Time
}

func (c *fakeClock) Now() time.Time { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

func TestSession_Create(t *testing.T) {
	store, _ := newTestSessionStore(t)

	sess, err := store.Create(1)
	if err != nil {
		t.Fatal(err)
	}
	if len(sess.ID) != 32 {
		t.Errorf("session ID length = %d, want 32", len(sess.ID))
	}
	if sess.UserID != 1 {
		t.Errorf("UserID = %d, want 1", sess.UserID)
	}
	if sess.ExpiresAt.Sub(sess.CreatedAt) != 1*time.Hour {
		t.Errorf("TTL = %v, want 1h", sess.ExpiresAt.Sub(sess.CreatedAt))
	}
}

func TestSession_LookupValid(t *testing.T) {
	store, _ := newTestSessionStore(t)

	created, err := store.Create(1)
	if err != nil {
		t.Fatal(err)
	}

	found, err := store.Lookup(created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.UserID != 1 {
		t.Errorf("UserID = %d, want 1", found.UserID)
	}
}

func TestSession_LookupNotFound(t *testing.T) {
	store, _ := newTestSessionStore(t)

	_, err := store.Lookup(make([]byte, 32))
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound, got %v", err)
	}
}

func TestSession_LookupExpired(t *testing.T) {
	store, clock := newTestSessionStore(t)

	sess, err := store.Create(1)
	if err != nil {
		t.Fatal(err)
	}

	// Advance past TTL.
	clock.Advance(2 * time.Hour)

	_, err = store.Lookup(sess.ID)
	if !errors.Is(err, ErrSessionExpired) {
		t.Errorf("expected ErrSessionExpired, got %v", err)
	}

	// Verify the expired session was cleaned up.
	_, err = store.Lookup(sess.ID)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound after cleanup, got %v", err)
	}
}

func TestSession_Create_DBError(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	_ = database.Close() // Close to trigger error.

	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	store := NewSessionStore(database, clock.Now, 1*time.Hour)

	_, err = store.Create(1)
	if err == nil {
		t.Fatal("expected error on closed DB")
	}
}

func TestSession_Lookup_DBError(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	_ = database.Close()

	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	store := NewSessionStore(database, clock.Now, 1*time.Hour)

	_, err = store.Lookup(make([]byte, 32))
	if err == nil {
		t.Fatal("expected error on closed DB")
	}
	// Should NOT be ErrSessionNotFound — it's a real DB error.
	if errors.Is(err, ErrSessionNotFound) {
		t.Error("expected a DB error, not ErrSessionNotFound")
	}
}

func TestSession_Delete_DBError(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	_ = database.Close()

	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	store := NewSessionStore(database, clock.Now, 1*time.Hour)

	err = store.Delete(make([]byte, 32))
	if err == nil {
		t.Fatal("expected error on closed DB")
	}
}

func TestSession_Delete(t *testing.T) {
	store, _ := newTestSessionStore(t)

	sess, err := store.Create(1)
	if err != nil {
		t.Fatal(err)
	}

	if err := store.Delete(sess.ID); err != nil {
		t.Fatal(err)
	}

	_, err = store.Lookup(sess.ID)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("expected ErrSessionNotFound after delete, got %v", err)
	}
}
