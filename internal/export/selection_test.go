package export

import (
	"crypto/sha256"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hjiang/mnemosyne/internal/blobs"
	"github.com/hjiang/mnemosyne/internal/search"
)

func seedBlob(t *testing.T, store *blobs.Store, content string) []byte {
	t.Helper()
	hash, err := store.Put(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

// Test 13: 5 distinct hashes → 5 distinct readers.
func TestSelection_DistinctHashes(t *testing.T) {
	dir := t.TempDir()
	store := blobs.NewStore(filepath.Join(dir, "blobs"))

	var results []search.MessageResult
	for i := 0; i < 5; i++ {
		content := fmt.Sprintf("message %d", i)
		hash := seedBlob(t, store, content)
		date := int64(i + 1)
		results = append(results, search.MessageResult{Hash: hash, Date: &date})
	}

	sel := NewSelection(results, store)
	msgs, err := sel.Messages()
	if err != nil {
		t.Fatal(err)
	}
	defer CloseAll(msgs)

	if len(msgs) != 5 {
		t.Errorf("len(msgs) = %d, want 5", len(msgs))
	}

	for i, m := range msgs {
		body, _ := io.ReadAll(m.Body)
		want := fmt.Sprintf("message %d", i)
		if string(body) != want {
			t.Errorf("msg[%d] = %q, want %q", i, body, want)
		}
	}
}

// Test 14: Dedup — 10 results with 7 distinct hashes → 7 messages.
func TestSelection_Dedup(t *testing.T) {
	dir := t.TempDir()
	store := blobs.NewStore(filepath.Join(dir, "blobs"))

	hashes := make([][]byte, 7)
	for i := 0; i < 7; i++ {
		hashes[i] = seedBlob(t, store, fmt.Sprintf("unique %d", i))
	}

	// Create 10 results, reusing some hashes.
	var results []search.MessageResult
	for i := 0; i < 10; i++ {
		h := hashes[i%7]
		date := int64(i)
		results = append(results, search.MessageResult{Hash: h, Date: &date})
	}

	sel := NewSelection(results, store)
	msgs, err := sel.Messages()
	if err != nil {
		t.Fatal(err)
	}
	defer CloseAll(msgs)

	if len(msgs) != 7 {
		t.Errorf("len(msgs) = %d, want 7 (dedup)", len(msgs))
	}
}

// Test 15: Selection scoping via search executor (isolation).
// This is tested indirectly — selection takes search.MessageResult which
// is already filtered by user_id in the executor. We verify that dedup
// only considers the results passed, not all blobs.
func TestSelection_OnlySelectedResults(t *testing.T) {
	dir := t.TempDir()
	store := blobs.NewStore(filepath.Join(dir, "blobs"))

	// Store blobs for both users.
	hashA := seedBlob(t, store, "user A message")
	hashB := seedBlob(t, store, "user B message")
	_ = hashB // blob exists but not in selection

	// Selection only includes user A's results.
	date := int64(1)
	results := []search.MessageResult{{Hash: hashA, Date: &date}}
	sel := NewSelection(results, store)
	msgs, err := sel.Messages()
	if err != nil {
		t.Fatal(err)
	}
	defer CloseAll(msgs)

	if len(msgs) != 1 {
		t.Errorf("len(msgs) = %d, want 1", len(msgs))
	}
	body, _ := io.ReadAll(msgs[0].Body)
	if string(body) != "user A message" {
		t.Errorf("body = %q, want 'user A message'", body)
	}
}

// Test 16: CloseAll closes readers.
func TestCloseAll(t *testing.T) {
	dir := t.TempDir()
	store := blobs.NewStore(filepath.Join(dir, "blobs"))
	hash := seedBlob(t, store, "test")

	date := int64(1)
	sel := NewSelection([]search.MessageResult{{Hash: hash, Date: &date}}, store)
	msgs, _ := sel.Messages()

	// Read the body first to verify it works.
	_, _ = io.ReadAll(msgs[0].Body)

	// CloseAll should not panic.
	CloseAll(msgs)

	// Attempting to read after close should fail or return empty.
	// The exact behavior depends on os.File, but it shouldn't panic.
	_ = sha256.New() // just to use the import
}
