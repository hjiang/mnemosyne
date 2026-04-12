package httpserver

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hjiang/mnemosyne/internal/auth"
	"github.com/hjiang/mnemosyne/internal/db"
	"github.com/hjiang/mnemosyne/internal/users"
)

type testEnv struct {
	server   *Server
	users    *users.Repo
	sessions *auth.SessionStore
	clock    *fakeClock
}

type fakeClock struct {
	t time.Time
}

func (c *fakeClock) Now() time.Time        { return c.t }
func (c *fakeClock) Advance(d time.Duration) { c.t = c.t.Add(d) }

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

	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	userRepo := users.NewRepo(database, clock.Now)
	sessions := auth.NewSessionStore(database, clock.Now, 1*time.Hour)
	srv := New(userRepo, sessions, nil, nil, nil, nil, nil, nil)

	return &testEnv{
		server:   srv,
		users:    userRepo,
		sessions: sessions,
		clock:    clock,
	}
}

// createUser is a helper that creates a user with a bcrypt password.
func (e *testEnv) createUser(t *testing.T, email, password string) {
	t.Helper()
	hash, err := auth.HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.users.Create(email, hash)
	if err != nil {
		t.Fatal(err)
	}
}

// loginCookie returns a session cookie value for the given user ID.
func (e *testEnv) loginCookie(t *testing.T, userID int64) string {
	t.Helper()
	sess, err := e.sessions.Create(userID)
	if err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(sess.ID)
}

func TestLoginForm_Renders(t *testing.T) {
	env := newTestEnv(t)

	req := httptest.NewRequest("GET", "/login", nil)
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "<form") {
		t.Error("expected login form in response")
	}
}

func TestLogin_ValidCredentials(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice@example.com", "secret123")

	form := url.Values{"email": {"alice@example.com"}, "password": {"secret123"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if loc := rr.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want /", loc)
	}
	// Should have a session cookie.
	var hasCookie bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == "mnemosyne_session" && c.Value != "" {
			hasCookie = true
		}
	}
	if !hasCookie {
		t.Error("expected session cookie to be set")
	}
}

func TestLogin_WrongPassword(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice@example.com", "secret123")

	form := url.Values{"email": {"alice@example.com"}, "password": {"wrong"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	body := rr.Body.String()
	if !strings.Contains(body, "Invalid email or password") {
		t.Error("expected generic error message for wrong password")
	}
	// No session cookie should be set.
	for _, c := range rr.Result().Cookies() {
		if c.Name == "mnemosyne_session" && c.Value != "" {
			t.Error("session cookie should not be set on failed login")
		}
	}
}

func TestLogin_UnknownEmail_SameError(t *testing.T) {
	env := newTestEnv(t)

	form := url.Values{"email": {"nobody@example.com"}, "password": {"anything"}}
	req := httptest.NewRequest("POST", "/login", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	body := rr.Body.String()
	// Same error message as wrong password — no user enumeration.
	if !strings.Contains(body, "Invalid email or password") {
		t.Error("expected same generic error for unknown email")
	}
}

func TestLogout_ClearsCookie(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice@example.com", "secret123")
	cookieVal := env.loginCookie(t, 1)

	req := httptest.NewRequest("POST", "/logout", nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: cookieVal})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	if loc := rr.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
	// Cookie should be cleared.
	for _, c := range rr.Result().Cookies() {
		if c.Name == "mnemosyne_session" && c.MaxAge >= 0 {
			t.Error("expected session cookie to be cleared (MaxAge < 0)")
		}
	}
}

func TestHome_Unauthenticated_Redirects(t *testing.T) {
	env := newTestEnv(t)

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d (redirect to login)", rr.Code, http.StatusSeeOther)
	}
	if loc := rr.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}

func TestHome_Authenticated_ShowsEmail(t *testing.T) {
	env := newTestEnv(t)
	env.createUser(t, "alice@example.com", "secret123")
	cookieVal := env.loginCookie(t, 1)

	req := httptest.NewRequest("GET", "/", nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: cookieVal})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "alice@example.com") {
		t.Error("expected user's email in home page")
	}
}

func TestLogout_NoCookie(t *testing.T) {
	env := newTestEnv(t)

	req := httptest.NewRequest("POST", "/logout", nil)
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
}
