package oauth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/hjiang/mnemosyne/internal/accounts"
	"github.com/hjiang/mnemosyne/internal/config"
	"github.com/hjiang/mnemosyne/internal/db"
)

func TestNewTokenManager_NotConfigured_ReturnsNil(t *testing.T) {
	tm := NewTokenManager(config.OAuthConfig{}, "http://localhost", nil)
	if tm != nil {
		t.Fatal("expected nil TokenManager when google oauth not configured")
	}
}

func TestAuthCodeURL_GeneratesUniqueStates(t *testing.T) {
	cfg := config.OAuthConfig{
		Google: &config.OAuthProviderConfig{
			ClientID:     "test-id",
			ClientSecret: "test-secret",
		},
	}
	tm := NewTokenManager(cfg, "http://localhost:8080", nil)

	url1, state1, err := tm.AuthCodeURL(1)
	if err != nil {
		t.Fatal(err)
	}
	url2, state2, err := tm.AuthCodeURL(1)
	if err != nil {
		t.Fatal(err)
	}

	if state1 == state2 {
		t.Error("expected unique states")
	}
	if url1 == "" || url2 == "" {
		t.Error("expected non-empty URLs")
	}
}

func TestValidateState_Valid(t *testing.T) {
	cfg := config.OAuthConfig{
		Google: &config.OAuthProviderConfig{
			ClientID:     "test-id",
			ClientSecret: "test-secret",
		},
	}
	tm := NewTokenManager(cfg, "http://localhost:8080", nil)

	_, state, err := tm.AuthCodeURL(42)
	if err != nil {
		t.Fatal(err)
	}

	userID, ok := tm.ValidateState(state)
	if !ok {
		t.Fatal("expected state to be valid")
	}
	if userID != 42 {
		t.Errorf("userID = %d, want 42", userID)
	}

	// Second use should fail (consumed).
	_, ok = tm.ValidateState(state)
	if ok {
		t.Error("expected state to be consumed after first use")
	}
}

func TestValidateState_Unknown(t *testing.T) {
	cfg := config.OAuthConfig{
		Google: &config.OAuthProviderConfig{
			ClientID:     "test-id",
			ClientSecret: "test-secret",
		},
	}
	tm := NewTokenManager(cfg, "http://localhost:8080", nil)
	_, ok := tm.ValidateState("nonexistent")
	if ok {
		t.Error("expected unknown state to be invalid")
	}
}

func TestValidateState_Expired(t *testing.T) {
	cfg := config.OAuthConfig{
		Google: &config.OAuthProviderConfig{
			ClientID:     "test-id",
			ClientSecret: "test-secret",
		},
	}
	tm := NewTokenManager(cfg, "http://localhost:8080", nil)

	_, state, err := tm.AuthCodeURL(1)
	if err != nil {
		t.Fatal(err)
	}

	// Manually expire the state.
	tm.mu.Lock()
	entry := tm.states[state]
	entry.expiresAt = time.Now().Add(-1 * time.Second)
	tm.states[state] = entry
	tm.mu.Unlock()

	_, ok := tm.ValidateState(state)
	if ok {
		t.Error("expected expired state to be invalid")
	}
}

// newTokenTestEnv sets up a TokenManager backed by a real SQLite accounts repo
// and a fake OAuth2 token endpoint for testing EnsureFreshToken.
func newTokenTestEnv(t *testing.T, tokenHandler http.HandlerFunc) (*TokenManager, *accounts.Repo, int64) {
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

	database.Exec("INSERT INTO users (email, password_hash, created_at) VALUES (?, ?, ?)", "test@test.com", "h", 0) //nolint:errcheck,gosec

	acctRepo := accounts.NewRepo(database, km)

	srv := httptest.NewServer(tokenHandler)
	t.Cleanup(srv.Close)

	tm := &TokenManager{
		accounts: acctRepo,
		states:   make(map[string]stateEntry),
		googleCfg: &oauth2.Config{
			ClientID:     "test-id",
			ClientSecret: "test-secret",
			Endpoint: oauth2.Endpoint{
				TokenURL: srv.URL + "/token",
			},
			Scopes: []string{"https://mail.google.com/"},
		},
	}

	return tm, acctRepo, 1
}

func TestEnsureFreshToken_ReturnsCachedWhenFresh(t *testing.T) {
	tokenCalls := 0
	tm, acctRepo, userID := newTokenTestEnv(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tokenCalls++
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))

	// Create an OAuth account with a token that expires far in the future.
	futureExpiry := time.Now().Add(1 * time.Hour).Unix()
	acct, err := acctRepo.CreateOAuth(userID, "test", "user@example.com", "oauth_google", "refresh-tok", "cached-access-tok", futureExpiry)
	if err != nil {
		t.Fatal(err)
	}

	tok, err := tm.EnsureFreshToken(context.Background(), acct.ID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "cached-access-tok" {
		t.Errorf("token = %q, want %q (cached)", tok, "cached-access-tok")
	}
	if tokenCalls != 0 {
		t.Errorf("token endpoint called %d times, want 0 (should use cache)", tokenCalls)
	}
}

func TestEnsureFreshToken_RefreshesExpiredToken(t *testing.T) {
	tm, acctRepo, userID := newTokenTestEnv(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access-tok",
			"token_type":   "Bearer",
			"expires_in":   3600,
		})
	}))

	// Create an OAuth account with an expired token.
	pastExpiry := time.Now().Add(-1 * time.Hour).Unix()
	acct, err := acctRepo.CreateOAuth(userID, "test", "user@example.com", "oauth_google", "refresh-tok", "old-access-tok", pastExpiry)
	if err != nil {
		t.Fatal(err)
	}

	tok, err := tm.EnsureFreshToken(context.Background(), acct.ID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if tok != "new-access-tok" {
		t.Errorf("token = %q, want %q", tok, "new-access-tok")
	}

	// Verify tokens were persisted.
	updated, err := acctRepo.GetByID(acct.ID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.AccessToken != "new-access-tok" {
		t.Errorf("persisted access token = %q, want %q", updated.AccessToken, "new-access-tok")
	}
}

func TestEnsureFreshToken_RotatesRefreshToken(t *testing.T) {
	tm, acctRepo, userID := newTokenTestEnv(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "new-access",
			"refresh_token": "rotated-refresh",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))

	pastExpiry := time.Now().Add(-1 * time.Hour).Unix()
	acct, err := acctRepo.CreateOAuth(userID, "test", "user@example.com", "oauth_google", "original-refresh", "old-access", pastExpiry)
	if err != nil {
		t.Fatal(err)
	}

	_, err = tm.EnsureFreshToken(context.Background(), acct.ID, userID)
	if err != nil {
		t.Fatal(err)
	}

	updated, err := acctRepo.GetByID(acct.ID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.RefreshToken != "rotated-refresh" {
		t.Errorf("refresh token = %q, want %q (rotated)", updated.RefreshToken, "rotated-refresh")
	}
}

func TestEnsureFreshToken_KeepsRefreshTokenWhenNotRotated(t *testing.T) {
	tm, acctRepo, userID := newTokenTestEnv(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "new-access",
			"token_type":   "Bearer",
			"expires_in":   3600,
			// No refresh_token in response — Google doesn't always rotate.
		})
	}))

	pastExpiry := time.Now().Add(-1 * time.Hour).Unix()
	acct, err := acctRepo.CreateOAuth(userID, "test", "user@example.com", "oauth_google", "original-refresh", "old-access", pastExpiry)
	if err != nil {
		t.Fatal(err)
	}

	_, err = tm.EnsureFreshToken(context.Background(), acct.ID, userID)
	if err != nil {
		t.Fatal(err)
	}

	updated, err := acctRepo.GetByID(acct.ID, userID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.RefreshToken != "original-refresh" {
		t.Errorf("refresh token = %q, want %q (kept original)", updated.RefreshToken, "original-refresh")
	}
}

func TestEnsureFreshToken_ErrorOnEmptyRefreshToken(t *testing.T) {
	tm, acctRepo, userID := newTokenTestEnv(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))

	pastExpiry := time.Now().Add(-1 * time.Hour).Unix()
	acct, err := acctRepo.CreateOAuth(userID, "test", "user@example.com", "oauth_google", "", "old-access", pastExpiry)
	if err != nil {
		t.Fatal(err)
	}

	_, err = tm.EnsureFreshToken(context.Background(), acct.ID, userID)
	if err == nil {
		t.Fatal("expected error for empty refresh token")
	}
}

func TestEnsureFreshToken_ErrorOnNonOAuthAccount(t *testing.T) {
	tm, acctRepo, userID := newTokenTestEnv(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "should not be called", http.StatusInternalServerError)
	}))

	// Create a password-type account.
	acct, err := acctRepo.Create(userID, "test", "imap.example.com", 993, "user", "pass", true, "", 0, "", "")
	if err != nil {
		t.Fatal(err)
	}

	_, err = tm.EnsureFreshToken(context.Background(), acct.ID, userID)
	if err == nil {
		t.Fatal("expected error for non-OAuth account")
	}
}

func TestExchange_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "acc-tok",
			"refresh_token": "ref-tok",
			"token_type":    "Bearer",
			"expires_in":    3600,
		})
	}))
	t.Cleanup(srv.Close)

	tm := &TokenManager{
		states: make(map[string]stateEntry),
		googleCfg: &oauth2.Config{
			ClientID:     "id",
			ClientSecret: "secret",
			Endpoint:     oauth2.Endpoint{TokenURL: srv.URL + "/token"},
		},
	}

	tok, err := tm.Exchange(context.Background(), "valid-code")
	if err != nil {
		t.Fatal(err)
	}
	if tok.AccessToken != "acc-tok" {
		t.Errorf("AccessToken = %q, want acc-tok", tok.AccessToken)
	}
	if tok.RefreshToken != "ref-tok" {
		t.Errorf("RefreshToken = %q, want ref-tok", tok.RefreshToken)
	}
}

func TestExchange_NotConfigured(t *testing.T) {
	tm := &TokenManager{states: make(map[string]stateEntry)}
	_, err := tm.Exchange(context.Background(), "code")
	if err == nil {
		t.Fatal("expected error when google oauth not configured")
	}
}

func TestSetGoogleEndpoint_Overrides(t *testing.T) {
	tm := NewTokenManager(config.OAuthConfig{
		Google: &config.OAuthProviderConfig{ClientID: "id", ClientSecret: "secret"},
	}, "http://localhost", nil)
	override := oauth2.Endpoint{AuthURL: "http://x/auth", TokenURL: "http://x/token"} //nolint:gosec // test-only fake URLs
	tm.SetGoogleEndpoint(override)
	if tm.googleCfg.Endpoint.TokenURL != "http://x/token" {
		t.Errorf("TokenURL = %q, want http://x/token", tm.googleCfg.Endpoint.TokenURL)
	}
}

func TestPruneExpiredStates_HardCap(t *testing.T) {
	tm := &TokenManager{states: make(map[string]stateEntry)}
	// Fill past the cap with non-expired entries; the hard-cap branch should
	// kick in and drop the oldest until len(states) < maxPendingStates.
	now := time.Now()
	for i := 0; i < maxPendingStates+10; i++ {
		tm.states[string(rune('a'+i))+"-state"] = stateEntry{
			userID:    int64(i),
			expiresAt: now.Add(time.Duration(i) * time.Minute),
		}
	}
	tm.mu.Lock()
	tm.pruneExpiredStatesLocked()
	n := len(tm.states)
	tm.mu.Unlock()
	if n >= maxPendingStates {
		t.Errorf("after prune, len(states) = %d, want < %d", n, maxPendingStates)
	}
}
