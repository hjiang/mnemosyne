package httpserver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

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
	if s.tokenMgr == nil {
		http.Error(w, "Google OAuth not configured", http.StatusNotFound)
		return
	}

	// Validate state parameter.
	state := r.URL.Query().Get("state")
	userID, ok := s.tokenMgr.ValidateState(state)
	if !ok {
		http.Error(w, "invalid or expired state parameter", http.StatusBadRequest)
		return
	}

	// Check for errors from Google.
	if errMsg := r.URL.Query().Get("error"); errMsg != "" {
		s.render(w, r, "accounts.html", map[string]any{
			"Title": "Accounts",
			"Error": fmt.Sprintf("Google authorization failed: %s", errMsg),
		})
		return
	}

	// Exchange authorization code for tokens.
	code := r.URL.Query().Get("code")
	tok, err := s.tokenMgr.Exchange(r.Context(), code)
	if err != nil {
		log.Printf("oauth token exchange: %v", err)
		s.render(w, r, "accounts.html", map[string]any{
			"Title": "Accounts",
			"Error": "Failed to exchange authorization code.",
		})
		return
	}

	// Extract email from the ID token.
	email, err := extractEmailFromIDToken(tok)
	if err != nil {
		log.Printf("oauth extract email: %v", err)
		s.render(w, r, "accounts.html", map[string]any{
			"Title": "Accounts",
			"Error": "Failed to determine account email.",
		})
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
		s.render(w, r, "accounts.html", map[string]any{
			"Title": "Accounts",
			"Error": "Failed to create account.",
		})
		return
	}

	// Discover folders in the background using OAuth (outlives request context).
	go s.discoverFolders(acct) //nolint:gosec // G118 - intentionally outlives request

	http.Redirect(w, r, fmt.Sprintf("/accounts/%d/folders", acct.ID), http.StatusSeeOther)
}

// extractEmailFromIDToken pulls the email from the id_token extra field
// that Google returns alongside the access token.
func extractEmailFromIDToken(tok interface{ Extra(string) interface{} }) (string, error) {
	idToken, ok := tok.Extra("id_token").(string)
	if !ok || idToken == "" {
		return "", fmt.Errorf("no id_token in response")
	}

	// The ID token is a JWT: header.payload.signature (base64url-encoded).
	// We only need the payload to extract the email claim.
	parts := strings.SplitN(idToken, ".", 3)
	if len(parts) != 3 {
		return "", fmt.Errorf("malformed id_token")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decoding id_token payload: %w", err)
	}

	var claims struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("parsing id_token claims: %w", err)
	}
	if claims.Email == "" {
		return "", fmt.Errorf("no email claim in id_token")
	}
	return claims.Email, nil
}
