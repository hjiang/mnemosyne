package accounts

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrDecryptionFailed indicates that a ciphertext could not be decrypted.
var ErrDecryptionFailed = errors.New("decryption failed")

// KeyManager handles loading or generating the server encryption key.
type KeyManager struct {
	keyPath string
	key     []byte
}

// NewKeyManager creates a key manager that stores the key at the given path.
// If the key file does not exist, a new 32-byte key is generated.
func NewKeyManager(dataDir string) (*KeyManager, error) {
	keyPath := filepath.Join(dataDir, "secret.key")

	key, err := os.ReadFile(keyPath) //nolint:gosec // G304 - path is derived from config data_dir
	if err == nil {
		if len(key) != 32 {
			return nil, fmt.Errorf("secret.key has %d bytes, expected 32", len(key))
		}
		return &KeyManager{keyPath: keyPath, key: key}, nil
	}

	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("reading secret.key: %w", err)
	}

	// Generate a new key.
	key = make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generating key: %w", err)
	}

	if err := os.WriteFile(keyPath, key, 0o600); err != nil {
		return nil, fmt.Errorf("writing secret.key: %w", err)
	}

	return &KeyManager{keyPath: keyPath, key: key}, nil
}

// Encrypt encrypts plaintext using AES-256-GCM. Returns nonce+ciphertext.
func (km *KeyManager) Encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(km.key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generating nonce: %w", err)
	}

	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

// Decrypt decrypts a nonce+ciphertext produced by Encrypt.
func (km *KeyManager) Decrypt(ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(km.key)
	if err != nil {
		return nil, fmt.Errorf("creating cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}

	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, ErrDecryptionFailed
	}

	nonce, ct := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, ErrDecryptionFailed
	}

	return plaintext, nil
}
