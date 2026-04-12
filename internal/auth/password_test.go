package auth

import (
	"errors"
	"testing"
)

func TestHashPassword_Roundtrip(t *testing.T) {
	hash, err := HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if err := VerifyPassword(hash, "correct-horse-battery"); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestVerifyPassword_WrongPassword(t *testing.T) {
	hash, err := HashPassword("correct")
	if err != nil {
		t.Fatal(err)
	}
	err = VerifyPassword(hash, "wrong")
	if !errors.Is(err, ErrInvalidPassword) {
		t.Errorf("expected ErrInvalidPassword, got %v", err)
	}
}

func TestVerifyPassword_MalformedHash(t *testing.T) {
	err := VerifyPassword("not-a-bcrypt-hash", "anything")
	if !errors.Is(err, ErrInvalidPassword) {
		t.Errorf("expected ErrInvalidPassword for malformed hash, got %v", err)
	}
}

func TestHashPassword_SaltIsRandom(t *testing.T) {
	h1, err := HashPassword("same-password")
	if err != nil {
		t.Fatal(err)
	}
	h2, err := HashPassword("same-password")
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h2 {
		t.Error("two hashes of the same password should differ (random salt)")
	}
}
