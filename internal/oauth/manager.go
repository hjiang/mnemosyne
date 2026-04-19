// Package oauth manages OAuth2 token lifecycle for IMAP authentication.
package oauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/hjiang/mnemosyne/internal/accounts"
	"github.com/hjiang/mnemosyne/internal/config"
)

// gmailIMAPScope grants full IMAP access to Gmail.
const gmailIMAPScope = "https://mail.google.com/"

// stateEntry tracks a pending OAuth state parameter.
type stateEntry struct {
	userID    int64
	expiresAt time.Time
}

// TokenManager handles the OAuth2 authorization flow and token refresh.
type TokenManager struct {
	googleCfg *oauth2.Config
	accounts  *accounts.Repo

	mu     sync.Mutex
	states map[string]stateEntry
}

// NewTokenManager creates a token manager for the configured OAuth providers.
func NewTokenManager(cfg config.OAuthConfig, baseURL string, acctRepo *accounts.Repo) *TokenManager {
	tm := &TokenManager{
		accounts: acctRepo,
		states:   make(map[string]stateEntry),
	}
	if cfg.Google != nil {
		tm.googleCfg = &oauth2.Config{
			ClientID:     cfg.Google.ClientID,
			ClientSecret: cfg.Google.ClientSecret,
			Endpoint:     google.Endpoint,
			RedirectURL:  baseURL + "/oauth/google/callback",
			Scopes:       []string{gmailIMAPScope, "email"},
		}
	}
	return tm
}

// AuthCodeURL generates an authorization URL and a random state parameter.
// The state is stored in memory and expires after 10 minutes.
func (tm *TokenManager) AuthCodeURL(userID int64) (string, string, error) {
	if tm.googleCfg == nil {
		return "", "", fmt.Errorf("google oauth not configured")
	}

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generating state: %w", err)
	}
	state := hex.EncodeToString(b)

	tm.mu.Lock()
	tm.states[state] = stateEntry{
		userID:    userID,
		expiresAt: time.Now().Add(10 * time.Minute),
	}
	tm.mu.Unlock()

	url := tm.googleCfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
	)
	return url, state, nil
}

// ValidateState checks and consumes a state parameter, returning the associated user ID.
func (tm *TokenManager) ValidateState(state string) (int64, bool) {
	tm.mu.Lock()
	defer tm.mu.Unlock()

	entry, ok := tm.states[state]
	if !ok {
		return 0, false
	}
	delete(tm.states, state)

	if time.Now().After(entry.expiresAt) {
		return 0, false
	}
	return entry.userID, true
}

// Exchange trades an authorization code for OAuth2 tokens.
func (tm *TokenManager) Exchange(ctx context.Context, code string) (*oauth2.Token, error) {
	if tm.googleCfg == nil {
		return nil, fmt.Errorf("google oauth not configured")
	}
	return tm.googleCfg.Exchange(ctx, code)
}

// tokenExpiryBuffer is how far before actual expiry we consider a token stale.
const tokenExpiryBuffer = 5 * time.Minute

// EnsureFreshToken checks the account's access token and refreshes it if
// expired. Returns a valid access token. This is the method called by
// the backup orchestrator before each IMAP dial.
func (tm *TokenManager) EnsureFreshToken(ctx context.Context, accountID, userID int64) (string, error) {
	acct, err := tm.accounts.GetByID(accountID, userID)
	if err != nil {
		return "", fmt.Errorf("loading account: %w", err)
	}
	if !acct.IsOAuth() {
		return "", fmt.Errorf("account %d is not an OAuth account", accountID)
	}

	// If the access token is still fresh, return it.
	if acct.TokenExpiry != nil {
		expiresAt := time.Unix(*acct.TokenExpiry, 0)
		if time.Now().Add(tokenExpiryBuffer).Before(expiresAt) {
			return acct.AccessToken, nil
		}
	}

	// Refresh the token.
	tok := &oauth2.Token{
		RefreshToken: acct.RefreshToken,
	}
	src := tm.googleCfg.TokenSource(ctx, tok)
	newTok, err := src.Token()
	if err != nil {
		return "", fmt.Errorf("refreshing token for account %d: %w", accountID, err)
	}

	// Google may rotate the refresh token.
	refreshToken := acct.RefreshToken
	if newTok.RefreshToken != "" {
		refreshToken = newTok.RefreshToken
	}

	expiry := newTok.Expiry.Unix()
	if err := tm.accounts.UpdateTokens(accountID, newTok.AccessToken, refreshToken, expiry); err != nil {
		return "", fmt.Errorf("storing refreshed tokens: %w", err)
	}

	return newTok.AccessToken, nil
}
