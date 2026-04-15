package scheduler

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/hjiang/mnemosyne/internal/accounts"
	"github.com/hjiang/mnemosyne/internal/db"
	"github.com/hjiang/mnemosyne/internal/jobs"
)

type schedTestEnv struct {
	accounts *accounts.Repo
	queue    *jobs.Queue
}

func newSchedTestEnv(t *testing.T) *schedTestEnv {
	t.Helper()
	dir := t.TempDir()
	database, err := db.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := db.Migrate(database); err != nil {
		t.Fatal(err)
	}

	km, err := accounts.NewKeyManager(dir)
	if err != nil {
		t.Fatal(err)
	}

	database.Exec("INSERT INTO users (email, password_hash, created_at) VALUES (?, 'h', 0)", "user@test.com") //nolint:errcheck,gosec

	return &schedTestEnv{
		accounts: accounts.NewRepo(database, km),
		queue:    jobs.NewQueue(database, time.Now),
	}
}

// Test 17: Cron parser accepts standard 5-field expressions.
func TestNew_ValidSchedule(t *testing.T) {
	env := newSchedTestEnv(t)
	s, err := New("0 3 * * *", env.accounts, env.queue)
	if err != nil {
		t.Fatal(err)
	}
	s.Stop()
}

func TestNew_InvalidSchedule(t *testing.T) {
	env := newSchedTestEnv(t)
	_, err := New("not a cron", env.accounts, env.queue)
	if err == nil {
		t.Fatal("expected error for invalid schedule")
	}
}

// Test 18: EnqueueAll creates one backup job per enabled account.
func TestEnqueueAll(t *testing.T) {
	env := newSchedTestEnv(t)

	acct1, _ := env.accounts.Create(1, "A", "host", 993, "u", "p", true, "", 0, "", "")
	folder1, _ := env.accounts.CreateFolder(acct1.ID, "INBOX")
	_ = env.accounts.SetFolderEnabled(folder1.ID, true)

	acct2, _ := env.accounts.Create(1, "B", "host2", 993, "u", "p", true, "", 0, "", "")
	_, _ = env.accounts.CreateFolder(acct2.ID, "INBOX") // not enabled

	s, err := New("0 3 * * *", env.accounts, env.queue)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	if err := s.EnqueueAll(); err != nil {
		t.Fatal(err)
	}

	j, err := env.queue.Claim()
	if err != nil {
		t.Fatal(err)
	}
	if j == nil {
		t.Fatal("expected at least one job")
	}
	if j.Kind != "backup" {
		t.Errorf("Kind = %q, want backup", j.Kind)
	}

	j2, _ := env.queue.Claim()
	if j2 != nil {
		t.Error("expected only one job (second account has no enabled folders)")
	}
}

// Test 19: Disabled accounts are not enqueued.
func TestEnqueueAll_DisabledSkipped(t *testing.T) {
	env := newSchedTestEnv(t)

	acct, _ := env.accounts.Create(1, "A", "host", 993, "u", "p", true, "", 0, "", "")
	_, _ = env.accounts.CreateFolder(acct.ID, "INBOX") // not enabled

	s, err := New("0 3 * * *", env.accounts, env.queue)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	if err := s.EnqueueAll(); err != nil {
		t.Fatal(err)
	}

	j, _ := env.queue.Claim()
	if j != nil {
		t.Error("no jobs should be enqueued for disabled folders")
	}
}

// Test 20: Payload shape is consistent.
func TestPayloadShape(t *testing.T) {
	env := newSchedTestEnv(t)

	acct, _ := env.accounts.Create(1, "A", "host", 993, "u", "p", true, "", 0, "", "")
	folder, _ := env.accounts.CreateFolder(acct.ID, "INBOX")
	_ = env.accounts.SetFolderEnabled(folder.ID, true)

	s, err := New("0 3 * * *", env.accounts, env.queue)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Stop()

	if err := s.EnqueueAll(); err != nil {
		t.Fatal(err)
	}

	j, _ := env.queue.Claim()
	if j == nil {
		t.Fatal("expected a job")
	}

	var payload BackupPayload
	if err := json.Unmarshal([]byte(j.Payload), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.AccountID != acct.ID {
		t.Errorf("AccountID = %d, want %d", payload.AccountID, acct.ID)
	}
	if payload.UserID != 1 {
		t.Errorf("UserID = %d, want 1", payload.UserID)
	}
}
