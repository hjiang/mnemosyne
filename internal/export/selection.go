package export

import (
	"encoding/hex"
	"fmt"
	"io"
	"time"

	"github.com/hjiang/mnemosyne/internal/blobs"
	"github.com/hjiang/mnemosyne/internal/search"
)

// Selection yields distinct messages for export, deduplicating by hash.
type Selection struct {
	store    *blobs.Store
	messages []search.MessageResult
}

// NewSelection creates a selection from search results and a blob store.
func NewSelection(results []search.MessageResult, store *blobs.Store) *Selection {
	return &Selection{
		store:    store,
		messages: dedup(results),
	}
}

// Messages returns the export messages, opening blob readers lazily.
func (s *Selection) Messages() ([]Message, error) {
	msgs := make([]Message, 0, len(s.messages))
	for _, r := range s.messages {
		rc, err := s.store.Get(r.Hash)
		if err != nil {
			return nil, fmt.Errorf("opening blob %s: %w", hex.EncodeToString(r.Hash), err)
		}
		var date time.Time
		if r.Date != nil {
			date = time.Unix(*r.Date, 0)
		}
		msgs = append(msgs, Message{
			Hash:         r.Hash,
			InternalDate: date,
			Body:         rc,
		})
	}
	return msgs, nil
}

// CloseAll closes all open readers in the export messages slice.
func CloseAll(msgs []Message) {
	for _, m := range msgs {
		if c, ok := m.Body.(io.Closer); ok {
			_ = c.Close()
		}
	}
}

// dedup removes duplicate messages by hash.
func dedup(results []search.MessageResult) []search.MessageResult {
	seen := make(map[string]bool)
	out := make([]search.MessageResult, 0, len(results))
	for _, r := range results {
		key := hex.EncodeToString(r.Hash)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, r)
	}
	return out
}
