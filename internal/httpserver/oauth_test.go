package httpserver

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hjiang/mnemosyne/internal/accounts"
	"github.com/hjiang/mnemosyne/internal/auth"
	"github.com/hjiang/mnemosyne/internal/backup"
	"github.com/hjiang/mnemosyne/internal/blobs"
	"github.com/hjiang/mnemosyne/internal/config"
	"github.com/hjiang/mnemosyne/internal/db"
	"github.com/hjiang/mnemosyne/internal/jobs"
	"github.com/hjiang/mnemosyne/internal/messages"
	"github.com/hjiang/mnemosyne/internal/oauth"
	"github.com/hjiang/mnemosyne/internal/search"
	"github.com/hjiang/mnemosyne/internal/users"
)

type oauthTestEnv struct {
	server   *Server
	accounts *accounts.Repo
	sessions *auth.SessionStore
	tokenMgr *oauth.TokenManager
	cookie   string
	userID   int64
}

func newOAuthTestEnv(t *testing.T, withTokenMgr bool) *oauthTestEnv {
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

	clock := &fakeClock{t: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	userRepo := users.NewRepo(database, clock.Now)
	sessions := auth.NewSessionStore(database, clock.Now, 1*time.Hour)
	acctRepo := accounts.NewRepo(database, km)
	msgRepo := messages.NewRepo(database)
	store := blobs.NewStore(filepath.Join(dir, "blobs"))
	orch := backup.NewOrchestrator(acctRepo, msgRepo, store, nil)
	searchExec := search.NewExecutor(database)
	jobQueue := jobs.NewQueue(database, clock.Now)

	var tm *oauth.TokenManager
	if withTokenMgr {
		tm = oauth.NewTokenManager(config.OAuthConfig{
			Google: &config.OAuthProviderConfig{ //nolint:gosec // test credentials
				ClientID:     "fake-client-id",
				ClientSecret: "fake-client-secret",
			},
		}, "http://localhost:8080", acctRepo)
	}

	srv := New(userRepo, sessions, acctRepo, orch, jobQueue, msgRepo, searchExec, store, tm)

	hash, _ := auth.HashPassword("pass")
	u, _ := userRepo.Create("test@test.com", hash)
	sess, _ := sessions.Create(u.ID)

	return &oauthTestEnv{
		server:   srv,
		accounts: acctRepo,
		sessions: sessions,
		tokenMgr: tm,
		cookie:   hex.EncodeToString(sess.ID),
		userID:   u.ID,
	}
}

// Test: /oauth/google/start returns 404 when tokenMgr is nil.
func TestOAuthStart_NoTokenMgr_Returns404(t *testing.T) {
	env := newOAuthTestEnv(t, false)

	req := httptest.NewRequest("GET", "/oauth/google/start", nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookie})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

// Test: /oauth/google/start redirects to Google when tokenMgr is configured.
func TestOAuthStart_Redirects(t *testing.T) {
	env := newOAuthTestEnv(t, true)

	req := httptest.NewRequest("GET", "/oauth/google/start", nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookie})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusSeeOther)
	}
	loc := rr.Header().Get("Location")
	if !strings.Contains(loc, "accounts.google.com") {
		t.Errorf("Location = %q, want redirect to Google", loc)
	}
	if !strings.Contains(loc, "fake-client-id") {
		t.Errorf("Location = %q, want to contain client_id", loc)
	}
}

// Test: /oauth/google/callback returns 404 when tokenMgr is nil.
func TestOAuthCallback_NoTokenMgr_Returns404(t *testing.T) {
	env := newOAuthTestEnv(t, false)

	req := httptest.NewRequest("GET", "/oauth/google/callback?code=abc&state=xyz", nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookie})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusNotFound)
	}
}

// Test: /oauth/google/callback rejects invalid state.
func TestOAuthCallback_InvalidState(t *testing.T) {
	env := newOAuthTestEnv(t, true)

	req := httptest.NewRequest("GET", "/oauth/google/callback?code=abc&state=bogus", nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookie})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rr.Body.String(), "invalid or expired state") {
		t.Errorf("body = %q, want state error message", rr.Body.String())
	}
}

// Test: /oauth/google/callback renders error when Google returns ?error=...
func TestOAuthCallback_GoogleError(t *testing.T) {
	env := newOAuthTestEnv(t, true)

	// Generate a valid state for the authenticated user.
	_, state, err := env.tokenMgr.AuthCodeURL(env.userID)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest("GET", "/oauth/google/callback?error=access_denied&state="+state, nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookie})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want %d (error rendered in page)", rr.Code, http.StatusOK)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "access_denied") {
		t.Errorf("body should contain Google error, got: %s", body)
	}
}

// Test: /oauth/google/callback rejects cross-user state (state was created
// for user A but callback is made by user B).
func TestOAuthCallback_CrossUserState(t *testing.T) {
	env := newOAuthTestEnv(t, true)

	// Create a second user.
	hash, _ := auth.HashPassword("pass")
	uB, _ := env.server.users.Create("other@test.com", hash)
	sessB, _ := env.sessions.Create(uB.ID)
	cookieB := hex.EncodeToString(sessB.ID)

	// Generate state for user A.
	_, state, err := env.tokenMgr.AuthCodeURL(env.userID)
	if err != nil {
		t.Fatal(err)
	}

	// User B tries to use user A's state.
	req := httptest.NewRequest("GET", "/oauth/google/callback?code=abc&state="+state, nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: cookieB})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusForbidden {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusForbidden)
	}
}

// Test: /oauth/google/callback returns 400 when code parameter is missing.
func TestOAuthCallback_MissingCode(t *testing.T) {
	env := newOAuthTestEnv(t, true)

	// Generate a valid state.
	_, state, err := env.tokenMgr.AuthCodeURL(env.userID)
	if err != nil {
		t.Fatal(err)
	}

	// Callback with valid state but no code (and no error).
	req := httptest.NewRequest("GET", "/oauth/google/callback?state="+state, nil)
	req.AddCookie(&http.Cookie{Name: "mnemosyne_session", Value: env.cookie})
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rr.Code, http.StatusBadRequest)
	}
	if !strings.Contains(rr.Body.String(), "missing authorization code") {
		t.Errorf("body = %q, want missing code error", rr.Body.String())
	}
}

// Test: /oauth/google/start requires authentication.
func TestOAuthStart_Unauthenticated_Redirects(t *testing.T) {
	env := newOAuthTestEnv(t, true)

	req := httptest.NewRequest("GET", "/oauth/google/start", nil)
	rr := httptest.NewRecorder()
	env.server.ServeHTTP(rr, req)

	if rr.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d (redirect to login)", rr.Code, http.StatusSeeOther)
	}
	if loc := rr.Header().Get("Location"); loc != "/login" {
		t.Errorf("Location = %q, want /login", loc)
	}
}
