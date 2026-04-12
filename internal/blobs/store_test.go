package blobs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	return NewStore(filepath.Join(t.TempDir(), "blobs"))
}

func TestPut_WritesAndReturnsHash(t *testing.T) {
	s := newTestStore(t)
	data := []byte("hello world")
	expected := sha256.Sum256(data)

	hash, err := s.Put(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hash, expected[:]) {
		t.Errorf("hash = %x, want %x", hash, expected[:])
	}

	// Verify file exists at the expected path.
	h := hex.EncodeToString(hash)
	path := filepath.Join(s.root, h[:2], h[2:4], h)
	if _, err := os.Stat(path); err != nil {
		t.Errorf("blob file not found at %s: %v", path, err)
	}
}

func TestPut_Idempotent(t *testing.T) {
	s := newTestStore(t)
	data := []byte("idempotent data")

	hash1, err := s.Put(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	// Get mtime of the blob file.
	path := s.path(hash1)
	info1, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}

	hash2, err := s.Put(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	if !bytes.Equal(hash1, hash2) {
		t.Errorf("hashes differ on second Put")
	}

	// File should not have been rewritten.
	info2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info2.ModTime() != info1.ModTime() {
		t.Error("blob file was rewritten on idempotent Put")
	}
}

func TestGet_RoundTrip(t *testing.T) {
	s := newTestStore(t)
	data := []byte("round trip test data")

	hash, err := s.Put(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	rc, err := s.Get(hash)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close() //nolint:errcheck

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("Get returned %q, want %q", got, data)
	}
}

func TestExists(t *testing.T) {
	s := newTestStore(t)

	if s.Exists(make([]byte, 32)) {
		t.Error("Exists returned true for unknown hash")
	}

	hash, err := s.Put(bytes.NewReader([]byte("exists test")))
	if err != nil {
		t.Fatal(err)
	}
	if !s.Exists(hash) {
		t.Error("Exists returned false for stored hash")
	}
}

func TestPut_AtomicOnWriteFailure(t *testing.T) {
	s := newTestStore(t)

	// Use a reader that fails mid-stream.
	failReader := &failingReader{
		data:    []byte("partial data that will fail"),
		failAt:  10,
	}

	_, err := s.Put(failReader)
	if err == nil {
		t.Fatal("expected error from failing reader")
	}

	// No temp files should remain.
	tmpDir := filepath.Join(s.root, "tmp")
	if entries, err := os.ReadDir(tmpDir); err == nil {
		for _, e := range entries {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestPut_ConcurrentSameBytes(t *testing.T) {
	s := newTestStore(t)
	data := []byte("concurrent test data")
	expected := sha256.Sum256(data)

	const n = 10
	var wg sync.WaitGroup
	errs := make([]error, n)
	hashes := make([][]byte, n)

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			hashes[idx], errs[idx] = s.Put(bytes.NewReader(data))
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Errorf("goroutine %d: %v", i, errs[i])
		}
		if !bytes.Equal(hashes[i], expected[:]) {
			t.Errorf("goroutine %d: hash mismatch", i)
		}
	}

	// Only one blob file should exist.
	if !s.Exists(expected[:]) {
		t.Error("blob missing after concurrent Puts")
	}
}

func TestGet_NotFound(t *testing.T) {
	s := newTestStore(t)

	_, err := s.Get(make([]byte, 32))
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestPut_EmptyData(t *testing.T) {
	s := newTestStore(t)
	hash, err := s.Put(bytes.NewReader(nil))
	if err != nil {
		t.Fatal(err)
	}
	// sha256 of empty input is the well-known e3b0c44298fc1c149afbf4c8996fb924...
	expected := sha256.Sum256(nil)
	if !bytes.Equal(hash, expected[:]) {
		t.Errorf("hash = %x, want %x", hash, expected[:])
	}
	if !s.Exists(hash) {
		t.Error("empty blob should exist after Put")
	}
}

func TestPut_LargeBlob(t *testing.T) {
	s := newTestStore(t)
	// 1MB of data to exercise the io.Copy path fully.
	data := make([]byte, 1<<20)
	for i := range data {
		data[i] = byte(i % 256)
	}
	hash, err := s.Put(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	rc, err := s.Get(hash)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close() //nolint:errcheck
	got, _ := io.ReadAll(rc)
	if !bytes.Equal(got, data) {
		t.Error("large blob round-trip mismatch")
	}
}

func TestPut_ReadOnlyRoot(t *testing.T) {
	// Store rooted in a non-existent, non-creatable path.
	s := NewStore("/proc/nonexistent/blobs")
	_, err := s.Put(bytes.NewReader([]byte("fail")))
	if err == nil {
		t.Fatal("expected error when root is not writable")
	}
}

func TestPut_DestDirCreationFailure(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	// First put to create the tmp dir successfully.
	data := []byte("dest dir failure test")

	// Make the root read-only after tmp dir is created to cause MkdirAll
	// for the content-addressed dir to fail.
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o750); err != nil {
		t.Fatal(err)
	}
	// Make root read-only so we can't create the hash subdirs.
	if err := os.Chmod(root, 0o555); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(root, 0o750) }) //nolint:gosec

	_, err := s.Put(bytes.NewReader(data))
	if err == nil {
		t.Fatal("expected error when dest dir cannot be created")
	}
}

func TestPut_TempFileCreationFailure(t *testing.T) {
	root := t.TempDir()
	s := NewStore(root)

	// Create tmp dir, then make it read-only so CreateTemp fails.
	tmpDir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmpDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tmpDir, 0o555); err != nil { //nolint:gosec
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(tmpDir, 0o750) }) //nolint:gosec

	_, err := s.Put(bytes.NewReader([]byte("fail at create temp")))
	if err == nil {
		t.Fatal("expected error when tmp dir is read-only")
	}
}

func TestGet_NonExistError(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get([]byte{0xde, 0xad, 0xbe, 0xef, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// failingReader returns data up to failAt bytes, then errors.
type failingReader struct {
	data   []byte
	failAt int
	pos    int
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.pos >= r.failAt {
		return 0, errors.New("injected read failure")
	}
	end := r.pos + len(p)
	if end > len(r.data) {
		end = len(r.data)
	}
	if end > r.failAt {
		end = r.failAt
	}
	n := copy(p, r.data[r.pos:end])
	r.pos += n
	if r.pos >= r.failAt {
		return n, errors.New("injected read failure")
	}
	if r.pos >= len(r.data) {
		return n, io.EOF
	}
	return n, nil
}
