package policy

import (
	"testing"
	"time"
)

var now = time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)

func msgs(dates ...int64) []Message {
	m := make([]Message, len(dates))
	for i, d := range dates {
		m[i] = Message{UID: uint32(i + 1), InternalDate: d}
	}
	return m
}

func day(daysAgo int) int64 {
	return now.AddDate(0, 0, -daysAgo).Unix()
}

// Test 1: "all" policy returns no UIDs regardless of input.
func TestAll_NeverExpunges(t *testing.T) {
	cfg := &Config{LeaveOnServer: "all"}
	result := Apply(cfg, msgs(1000, 2000, 3000), now)
	if len(result) != 0 {
		t.Errorf("all policy returned %v, want empty", result)
	}
}

// Test 2: newest_n with n=1000, 500 messages → expunges nothing.
func TestNewestN_UnderLimit(t *testing.T) {
	cfg := &Config{LeaveOnServer: "newest_n", N: 1000}
	result := Apply(cfg, msgs(1, 2, 3, 4, 5), now)
	if len(result) != 0 {
		t.Errorf("newest_n(1000) with 5 msgs returned %v, want empty", result)
	}
}

// Test 3: newest_n with n=100, 150 messages → expunges 50 oldest.
func TestNewestN_OverLimit(t *testing.T) {
	dates := make([]int64, 150)
	for i := range dates {
		dates[i] = int64(i + 1) // 1,2,...,150 — oldest first
	}
	cfg := &Config{LeaveOnServer: "newest_n", N: 100}
	result := Apply(cfg, msgs(dates...), now)
	if len(result) != 50 {
		t.Fatalf("len(expunge) = %d, want 50", len(result))
	}
	// The 50 oldest (dates 1-50) should be expunged.
	for _, uid := range result {
		if uid > 50 {
			t.Errorf("UID %d should not be expunged (it's in the newest 100)", uid)
		}
	}
}

// Test 4: newest_n with n=0 → expunges everything.
func TestNewestN_Zero(t *testing.T) {
	cfg := &Config{LeaveOnServer: "newest_n", N: 0}
	result := Apply(cfg, msgs(1, 2, 3), now)
	if len(result) != 3 {
		t.Errorf("newest_n(0) returned %d UIDs, want 3", len(result))
	}
}

// Test 5: younger_than(90 days) expunges messages older than 90 days.
func TestYoungerThan(t *testing.T) {
	cfg := &Config{LeaveOnServer: "younger_than", Days: 90}
	messages := []Message{
		{UID: 1, InternalDate: day(100)}, // 100 days ago → expunge
		{UID: 2, InternalDate: day(91)},  // 91 days ago → expunge
		{UID: 3, InternalDate: day(89)},  // 89 days ago → keep
		{UID: 4, InternalDate: day(1)},   // 1 day ago → keep
	}
	result := Apply(cfg, messages, now)
	if len(result) != 2 {
		t.Fatalf("len(expunge) = %d, want 2", len(result))
	}
	uids := map[uint32]bool{}
	for _, u := range result {
		uids[u] = true
	}
	if !uids[1] || !uids[2] {
		t.Errorf("expected UIDs 1,2 to be expunged, got %v", result)
	}
}

// Test 6: Ties — messages with identical internal_date are handled consistently.
func TestNewestN_Ties(t *testing.T) {
	// 5 messages, all same date. newest_n=3 must expunge exactly 2.
	messages := []Message{
		{UID: 1, InternalDate: 1000},
		{UID: 2, InternalDate: 1000},
		{UID: 3, InternalDate: 1000},
		{UID: 4, InternalDate: 1000},
		{UID: 5, InternalDate: 1000},
	}
	cfg := &Config{LeaveOnServer: "newest_n", N: 3}
	result := Apply(cfg, messages, now)
	if len(result) != 2 {
		t.Errorf("with ties, expected 2 expunged, got %d", len(result))
	}
}

// Test 7: Empty input → empty output for every policy.
func TestAllPolicies_EmptyInput(t *testing.T) {
	policies := []*Config{
		{LeaveOnServer: "all"},
		{LeaveOnServer: "newest_n", N: 5},
		{LeaveOnServer: "younger_than", Days: 90},
	}
	for _, cfg := range policies {
		result := Apply(cfg, nil, now)
		if len(result) != 0 {
			t.Errorf("%s with empty input returned %v", cfg.LeaveOnServer, result)
		}
	}
}

// Test 8: Invalid policy JSON → typed error at parse time.
func TestParseConfig_Invalid(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{"bad json", `not json`},
		{"unknown policy", `{"leave_on_server":"delete_all"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseConfig(tt.input)
			if err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestParseConfig_Valid(t *testing.T) {
	cfg, err := ParseConfig(`{"leave_on_server":"newest_n","n":100}`)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LeaveOnServer != "newest_n" || cfg.N != 100 {
		t.Errorf("cfg = %+v", cfg)
	}
}
