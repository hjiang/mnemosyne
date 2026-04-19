package httpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/hjiang/mnemosyne/internal/auth"
)

// oauthGoogleStart redirects the user to Google's OAuth consent screen.
func (s *Server) oauthGoogleStart(w http.ResponseWriter, r *http.Request) {
	if s.tokenMgr == nil {
		http.Error(w, "Google OAuth not configured", http.StatusNotFound)
		return
	}

	userID := auth.UserIDFromContext(r.Context())
	url, _, err := s.tokenMgr.AuthCodeURL(userID)
	if err != nil {
		http.Error(w, "failed to generate auth URL", http.StatusInternalServerError)
		return
	}

	http.Redirect(w, r, url, http.StatusSeeOther)
}

// oauthGoogleCallback handles the redirect from Google after authorization.
func (s *Server) oauthGoogleCallback(w http.ResponseWriter, r *http.Request) {
	if s.tokenMgr == nil || s.accounts == nil {
		http.Error(w, "Google OAuth not configured", http.StatusNotFound)
		return
	}

	// Validate state parameter.
	state := r.URL.Query().Get("state")
	stateUserID, ok := s.tokenMgr.ValidateState(state)
	if !ok {
		http.Error(w, "invalid or expired state parameter", http.StatusBadRequest)
		return
	}

	// Verify the authenticated user matches the one who started the flow.
	userID := auth.UserIDFromContext(r.Context())
	if stateUserID != userID {
		http.Error(w, "authenticated user does not match OAuth state", http.StatusForbidden)
		return
	}

	// Check for errors from Google.
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		s.renderOAuthError(w, r, userID, fmt.Sprintf("Google authorization failed: %s", errMsg))
		return
	}

	// Exchange authorization code for tokens.
	code := r.URL.Query().Get("code")
	tok, err := s.tokenMgr.Exchange(r.Context(), code)
	if err != nil {
		log.Printf("oauth token exchange: %v", err)
		s.renderOAuthError(w, r, userID, "Failed to exchange authorization code.")
		return
	}

	if tok.RefreshToken == "" {
		log.Print("oauth: no refresh token returned; user may need to revoke and re-authorize")
		s.renderOAuthError(w, r, userID, "Google did not return a refresh token. Please revoke Mnemosyne's access in your Google Account permissions and try again.")
		return
	}

	// Fetch the user's email from Google's userinfo endpoint.
	email, err := fetchGoogleEmail(r.Context(), tok.AccessToken)
	if err != nil {
		log.Printf("oauth fetch email: %v", err)
		s.renderOAuthError(w, r, userID, "Failed to determine account email.")
		return
	}

	// Create the account.
	label := fmt.Sprintf("Google - %s", email)
	acct, err := s.accounts.CreateOAuth(
		userID, label, email, "oauth_google",
		tok.RefreshToken, tok.AccessToken, tok.Expiry.Unix(),
	)
	if err != nil {
		log.Printf("oauth create account: %v", err)
		s.renderOAuthError(w, r, userID, "Failed to create account.")
		return
	}

	// Discover folders in the background using OAuth (outlives request context).
	go s.discoverFolders(acct) //nolint:gosec // G118 - intentionally outlives request

	http.Redirect(w, r, fmt.Sprintf("/accounts/%d/folders", acct.ID), http.StatusSeeOther)
}

// renderOAuthError renders the accounts page with an error message,
// preserving the full page data (account list, OAuth button state).
func (s *Server) renderOAuthError(w http.ResponseWriter, r *http.Request, userID int64, errMsg string) {
	accts, _ := s.accounts.List(userID)
	s.render(w, r, "accounts.html", map[string]any{
		"Title":              "Accounts",
		"Accounts":           accts,
		"OAuthGoogleEnabled": s.tokenMgr != nil,
		"Error":              errMsg,
	})
}

// userinfoTimeout is the maximum time to wait for Google's userinfo endpoint.
const userinfoTimeout = 10 * time.Second

// fetchGoogleEmail calls Google's userinfo endpoint to get the authenticated
// user's email address. This is more robust than parsing the id_token JWT
// because the endpoint validates the access token server-side.
func fetchGoogleEmail(ctx context.Context, accessToken string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, userinfoTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://www.googleapis.com/oauth2/v3/userinfo", nil)
	if err != nil {
		return "", fmt.Errorf("creating userinfo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetching userinfo: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("userinfo returned status %d", resp.StatusCode)
	}

	var info struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", fmt.Errorf("decoding userinfo: %w", err)
	}
	if info.Email == "" {
		return "", fmt.Errorf("no email in userinfo response")
	}
	if !info.EmailVerified {
		return "", fmt.Errorf("email in userinfo response is not verified")
	}
	return info.Email, nil
}
