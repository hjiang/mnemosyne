package oauth

import (
	"testing"
	"time"

	"github.com/hjiang/mnemosyne/internal/config"
)

func TestAuthCodeURL_NotConfigured(t *testing.T) {
	tm := NewTokenManager(config.OAuthConfig{}, "http://localhost", nil)
	_, _, err := tm.AuthCodeURL(1)
	if err == nil {
		t.Fatal("expected error when google oauth not configured")
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
	tm := NewTokenManager(config.OAuthConfig{}, "http://localhost", nil)
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
