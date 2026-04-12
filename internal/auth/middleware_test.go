package auth

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/hjiang/mnemosyne/internal/db"
)

func newTestMiddleware(t *testing.T) (*SessionStore, *fakeClock, http.Handler) {
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
	// Seed a user.
	_, err = database.Exec(
		"INSERT INTO users (email, password_hash, created_at) VALUES (?, ?, ?)",
		"user@example.com", "fakehash", time.Now().Unix(),
	)
	if err != nil {
		t.Fatal(err)
	}

	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	store := NewSessionStore(database, clock.Now, 1*time.Hour)

	handler := RequireAuth(store, "/login")(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok"))
		}),
	)

	return store, clock, handler
}

func TestMiddleware_NoCookie(t *testing.T) {
	_, _, handler := newTestMiddleware(t)

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if loc := rr.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

func TestMiddleware_BogusCookie(t *testing.T) {
	_, _, handler := newTestMiddleware(t)

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: "not-hex"})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	// Cookie should be cleared.
	found := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == cookieName && c.MaxAge < 0 {
			found = true
		}
	}
	if !found {
		t.Error("expected session cookie to be cleared")
	}
}

func TestMiddleware_ValidSession(t *testing.T) {
	store, _, handler := newTestMiddleware(t)

	sess, err := store.Create(1)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: hex.EncodeToString(sess.ID)})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
}

func TestMiddleware_ExpiredSession(t *testing.T) {
	store, clock, handler := newTestMiddleware(t)

	sess, err := store.Create(1)
	if err != nil {
		t.Fatal(err)
	}

	clock.Advance(2 * time.Hour)

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: hex.EncodeToString(sess.ID)})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	// Cookie should be cleared.
	found := false
	for _, c := range rr.Result().Cookies() {
		if c.Name == cookieName && c.MaxAge < 0 {
			found = true
		}
	}
	if !found {
		t.Error("expected session cookie to be cleared on expiry")
	}
}

func TestMiddleware_UnknownSessionID(t *testing.T) {
	_, _, handler := newTestMiddleware(t)

	fakeID := make([]byte, 32)
	fakeID[0] = 0xff
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: hex.EncodeToString(fakeID)})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
}

func TestMiddleware_UserIDInContext(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = database.Close() }()
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}
	_, err = database.Exec(
		"INSERT INTO users (email, password_hash, created_at) VALUES (?, ?, ?)",
		"ctx@example.com", "fakehash", time.Now().Unix(),
	)
	if err != nil {
		t.Fatal(err)
	}

	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	store := NewSessionStore(database, clock.Now, 1*time.Hour)

	var capturedUID int64
	handler := RequireAuth(store, "/login")(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			capturedUID = UserIDFromContext(r.Context())
			w.WriteHeader(http.StatusOK)
		}),
	)

	sess, _ := store.Create(1)
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: hex.EncodeToString(sess.ID)})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if capturedUID != 1 {
		t.Errorf("UserID from context = %d, want 1", capturedUID)
	}
}

func TestMiddleware_DBError(t *testing.T) {
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}

	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	store := NewSessionStore(database, clock.Now, 1*time.Hour)

	// Close DB to trigger a non-standard error during Lookup.
	_ = database.Close()

	handler := RequireAuth(store, "/login")(
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)

	validHex := hex.EncodeToString(make([]byte, 32))
	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: validHex})
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusInternalServerError)
	}
}

func TestContext_NoUserID(t *testing.T) {
	ctx := httptest.NewRequest("GET", "/", nil).Context()
	if uid := UserIDFromContext(ctx); uid != 0 {
		t.Errorf("expected 0 for missing user ID, got %d", uid)
	}
}

func TestSetSessionCookie(t *testing.T) {
	rr := httptest.NewRecorder()
	id := []byte{0x01, 0x02, 0x03}
	SetSessionCookie(rr, id)

	cookies := rr.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	c := cookies[0]
	if c.Name != cookieName {
		t.Errorf("cookie name = %q, want %q", c.Name, cookieName)
	}
	if c.Value != hex.EncodeToString(id) {
		t.Errorf("cookie value = %q, want %q", c.Value, hex.EncodeToString(id))
	}
	if !c.HttpOnly {
		t.Error("expected HttpOnly")
	}
}
