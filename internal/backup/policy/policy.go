// Package policy implements retention policy logic as pure functions.
package policy

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"
)

// Message is the minimal info needed for retention decisions.
type Message struct {
	UID          uint32
	InternalDate int64 // Unix timestamp
}

// Config describes a retention policy parsed from JSON.
type Config struct {
	LeaveOnServer string `json:"leave_on_server"` // "all", "newest_n", "younger_than"
	N             int    `json:"n,omitempty"`      // for newest_n
	Days          int    `json:"days,omitempty"`   // for younger_than
}

// ParseConfig parses a JSON policy configuration.
func ParseConfig(raw string) (*Config, error) {
	var c Config
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return nil, fmt.Errorf("parsing policy: %w", err)
	}
	switch c.LeaveOnServer {
	case "all", "newest_n", "younger_than":
		// valid
	default:
		return nil, fmt.Errorf("unknown policy: %q", c.LeaveOnServer)
	}
	return &c, nil
}

// Apply returns the UIDs that should be expunged according to the policy.
// now is injected for testability.
func Apply(cfg *Config, msgs []Message, now time.Time) []uint32 {
	if len(msgs) == 0 {
		return nil
	}

	switch cfg.LeaveOnServer {
	case "all":
		return nil
	case "newest_n":
		return applyNewestN(cfg.N, msgs)
	case "younger_than":
		return applyYoungerThan(cfg.Days, msgs, now)
	default:
		return nil
	}
}

func applyNewestN(n int, msgs []Message) []uint32 {
	if n <= 0 {
		// Keep nothing on server — expunge everything.
		uids := make([]uint32, len(msgs))
		for i, m := range msgs {
			uids[i] = m.UID
		}
		return uids
	}

	if len(msgs) <= n {
		return nil
	}

	// Sort by date descending; keep the newest N, expunge the rest.
	sorted := make([]Message, len(msgs))
	copy(sorted, msgs)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].InternalDate > sorted[j].InternalDate
	})

	var expunge []uint32
	for _, m := range sorted[n:] {
		expunge = append(expunge, m.UID)
	}
	return expunge
}

func applyYoungerThan(days int, msgs []Message, now time.Time) []uint32 {
	cutoff := now.AddDate(0, 0, -days).Unix()

	var expunge []uint32
	for _, m := range msgs {
		if m.InternalDate < cutoff {
			expunge = append(expunge, m.UID)
		}
	}
	return expunge
}
