// Package blobs provides a content-addressed filesystem blob store.
package blobs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// ErrNotFound indicates the requested blob does not exist.
var ErrNotFound = errors.New("blob not found")

// Store is a content-addressed filesystem blob store.
// Blobs are stored at <root>/<hash[0:2]>/<hash[2:4]>/<hash>.
type Store struct {
	root string
}

// NewStore creates a blob store rooted at the given directory.
func NewStore(root string) *Store {
	return &Store{root: root}
}

// Put writes the contents of r to the store and returns the sha256 hash.
// If a blob with the same hash already exists, Put is a no-op.
// Writes are atomic: data goes to a temp file, is fsynced, then renamed.
func (s *Store) Put(r io.Reader) ([]byte, error) {
	// Hash while writing to a temp file.
	tmpDir := filepath.Join(s.root, "tmp")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		return nil, fmt.Errorf("creating tmp dir: %w", err)
	}

	tmp, err := os.CreateTemp(tmpDir, "blob-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()

	h := sha256.New()
	w := io.MultiWriter(tmp, h)

	if _, err := io.Copy(w, r); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("writing blob: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("syncing blob: %w", err)
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("closing blob: %w", err)
	}

	hash := h.Sum(nil)

	// If already exists, discard the temp file.
	if s.Exists(hash) {
		_ = os.Remove(tmpPath)
		return hash, nil
	}

	// Rename into content-addressed path.
	destDir := s.dir(hash)
	if err := os.MkdirAll(destDir, 0o750); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("creating blob dir: %w", err)
	}

	destPath := s.path(hash)
	if err := os.Rename(tmpPath, destPath); err != nil {
		_ = os.Remove(tmpPath)
		return nil, fmt.Errorf("renaming blob: %w", err)
	}

	return hash, nil
}

// Get returns a reader for the blob with the given hash.
// The caller must close the returned ReadCloser.
func (s *Store) Get(hash []byte) (io.ReadCloser, error) {
	f, err := os.Open(s.path(hash))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("opening blob: %w", err)
	}
	return f, nil
}

// Exists returns true if a blob with the given hash is stored.
func (s *Store) Exists(hash []byte) bool {
	_, err := os.Stat(s.path(hash))
	return err == nil
}

func (s *Store) path(hash []byte) string {
	h := hex.EncodeToString(hash)
	return filepath.Join(s.root, h[:2], h[2:4], h)
}

func (s *Store) dir(hash []byte) string {
	h := hex.EncodeToString(hash)
	return filepath.Join(s.root, h[:2], h[2:4])
}
