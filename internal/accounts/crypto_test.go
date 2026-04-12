package accounts

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestKeyManager_GeneratesAndLoads(t *testing.T) {
	dir := t.TempDir()

	// First call generates the key.
	km1, err := NewKeyManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(km1.key) != 32 {
		t.Fatalf("key length = %d, want 32", len(km1.key))
	}

	// Verify file has correct permissions.
	info, err := os.Stat(filepath.Join(dir, "secret.key"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file permissions = %o, want 0600", info.Mode().Perm())
	}

	// Second call loads the existing key.
	km2, err := NewKeyManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(km1.key, km2.key) {
		t.Error("loaded key differs from generated key")
	}
}

func TestEncryptDecrypt_Roundtrip(t *testing.T) {
	dir := t.TempDir()
	km, err := NewKeyManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	plaintext := []byte("my-imap-password")
	ct, err := km.Encrypt(plaintext)
	if err != nil {
		t.Fatal(err)
	}

	got, err := km.Decrypt(ct)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, plaintext) {
		t.Errorf("decrypted = %q, want %q", got, plaintext)
	}
}

func TestEncrypt_RandomNonce(t *testing.T) {
	dir := t.TempDir()
	km, err := NewKeyManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	plaintext := []byte("same-password")
	ct1, _ := km.Encrypt(plaintext)
	ct2, _ := km.Encrypt(plaintext)

	if bytes.Equal(ct1, ct2) {
		t.Error("two encryptions of the same plaintext should differ (random nonce)")
	}
}

func TestDecrypt_WrongKey(t *testing.T) {
	dir1 := t.TempDir()
	km1, _ := NewKeyManager(dir1)

	dir2 := t.TempDir()
	km2, _ := NewKeyManager(dir2)

	ct, _ := km1.Encrypt([]byte("secret"))
	_, err := km2.Decrypt(ct)
	if !errors.Is(err, ErrDecryptionFailed) {
		t.Errorf("expected ErrDecryptionFailed, got %v", err)
	}
}

func TestDecrypt_TruncatedCiphertext(t *testing.T) {
	dir := t.TempDir()
	km, _ := NewKeyManager(dir)

	_, err := km.Decrypt([]byte{1, 2, 3})
	if !errors.Is(err, ErrDecryptionFailed) {
		t.Errorf("expected ErrDecryptionFailed for truncated ciphertext, got %v", err)
	}
}

func TestKeyManager_BadKeyLength(t *testing.T) {
	dir := t.TempDir()
	// Write a key with wrong length.
	if err := os.WriteFile(filepath.Join(dir, "secret.key"), []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := NewKeyManager(dir)
	if err == nil {
		t.Fatal("expected error for wrong key length")
	}
}
