package jobs

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/hjiang/mnemosyne/internal/db"
)

func newTestQueue(t *testing.T) *Queue {
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

	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return NewQueue(database, func() time.Time { return clock })
}

// Test 25: Enqueue persists a row with state=pending.
func TestEnqueue(t *testing.T) {
	q := newTestQueue(t)
	j, err := q.Enqueue("extract", `{"attachment_id":1}`)
	if err != nil {
		t.Fatal(err)
	}
	if j.Kind != "extract" {
		t.Errorf("Kind = %q, want extract", j.Kind)
	}
	if j.State != "pending" {
		t.Errorf("State = %q, want pending", j.State)
	}
	if j.Payload != `{"attachment_id":1}` {
		t.Errorf("Payload = %q", j.Payload)
	}

	// Verify in DB.
	got, err := q.GetByID(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "pending" {
		t.Errorf("DB State = %q, want pending", got.State)
	}
}

// Test 26: Sequential claims return distinct jobs.
func TestClaim_Distinct(t *testing.T) {
	q := newTestQueue(t)
	for i := 0; i < 5; i++ {
		if _, err := q.Enqueue("extract", ""); err != nil {
			t.Fatal(err)
		}
	}

	claimed := make(map[int64]bool)
	for i := 0; i < 5; i++ {
		j, err := q.Claim()
		if err != nil {
			t.Fatal(err)
		}
		if j == nil {
			t.Fatalf("claim %d returned nil", i)
		}
		if claimed[j.ID] {
			t.Errorf("job %d claimed twice", j.ID)
		}
		claimed[j.ID] = true
	}

	if len(claimed) != 5 {
		t.Errorf("claimed %d distinct jobs, want 5", len(claimed))
	}

	// No more pending jobs.
	j, err := q.Claim()
	if err != nil {
		t.Fatal(err)
	}
	if j != nil {
		t.Error("expected nil after all jobs claimed")
	}
}

// Test 27: Complete transitions running → done.
func TestComplete(t *testing.T) {
	q := newTestQueue(t)
	if _, err := q.Enqueue("extract", ""); err != nil {
		t.Fatal(err)
	}

	j, err := q.Claim()
	if err != nil {
		t.Fatal(err)
	}
	if j.State != "running" {
		t.Fatalf("State = %q, want running", j.State)
	}

	if err := q.Complete(j.ID); err != nil {
		t.Fatal(err)
	}

	got, _ := q.GetByID(j.ID)
	if got.State != "done" {
		t.Errorf("State = %q, want done", got.State)
	}
	if got.FinishedAt == nil {
		t.Error("FinishedAt should be set")
	}
}

// Test 28: Fail transitions to failed, increments attempts, records error.
func TestFail(t *testing.T) {
	q := newTestQueue(t)
	if _, err := q.Enqueue("extract", ""); err != nil {
		t.Fatal(err)
	}

	j, _ := q.Claim()
	if err := q.Fail(j.ID, "pdftotext crashed"); err != nil {
		t.Fatal(err)
	}

	got, _ := q.GetByID(j.ID)
	if got.State != "failed" {
		t.Errorf("State = %q, want failed", got.State)
	}
	if got.Error != "pdftotext crashed" {
		t.Errorf("Error = %q, want 'pdftotext crashed'", got.Error)
	}
	if got.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", got.Attempts)
	}
}

// ListByUser returns only the matching user's jobs in reverse order.
func TestListByUser(t *testing.T) {
	q := newTestQueue(t)

	// Enqueue jobs for two different users.
	for i := 0; i < 3; i++ {
		if _, err := q.Enqueue("backup", `{"account_id":1,"user_id":10}`); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if _, err := q.Enqueue("backup", `{"account_id":2,"user_id":20}`); err != nil {
			t.Fatal(err)
		}
	}

	// User 10 should see 3 jobs.
	jobs10, err := q.ListByUser(10, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs10) != 3 {
		t.Errorf("user 10: got %d jobs, want 3", len(jobs10))
	}

	// Verify newest-first ordering.
	for i := 1; i < len(jobs10); i++ {
		if jobs10[i].ID >= jobs10[i-1].ID {
			t.Errorf("jobs not in descending ID order: %d >= %d", jobs10[i].ID, jobs10[i-1].ID)
		}
	}

	// User 20 should see 2 jobs.
	jobs20, err := q.ListByUser(20, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs20) != 2 {
		t.Errorf("user 20: got %d jobs, want 2", len(jobs20))
	}

	// Limit parameter works.
	limited, err := q.ListByUser(10, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 1 {
		t.Errorf("limited: got %d jobs, want 1", len(limited))
	}

	// Non-existent user gets empty list.
	empty, err := q.ListByUser(999, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Errorf("non-existent user: got %d jobs, want 0", len(empty))
	}
}

// Claim on empty queue returns nil.
func TestClaim_Empty(t *testing.T) {
	q := newTestQueue(t)
	j, err := q.Claim()
	if err != nil {
		t.Fatal(err)
	}
	if j != nil {
		t.Error("expected nil for empty queue")
	}
}

// EnqueueIfNotActive creates a job when none exists for the account.
func TestEnqueueIfNotActive_Success(t *testing.T) {
	q := newTestQueue(t)
	j, err := q.EnqueueIfNotActive("backup", `{"account_id":1,"user_id":10}`, 1)
	if err != nil {
		t.Fatal(err)
	}
	if j == nil {
		t.Fatal("expected a job")
	}
	if j.State != "pending" {
		t.Errorf("State = %q, want pending", j.State)
	}
}

// EnqueueIfNotActive rejects when a pending job exists for the same account.
func TestEnqueueIfNotActive_RejectsPending(t *testing.T) {
	q := newTestQueue(t)
	if _, err := q.EnqueueIfNotActive("backup", `{"account_id":1,"user_id":10}`, 1); err != nil {
		t.Fatal(err)
	}

	_, err := q.EnqueueIfNotActive("backup", `{"account_id":1,"user_id":10}`, 1)
	if err != ErrJobActive {
		t.Errorf("err = %v, want ErrJobActive", err)
	}
}

// EnqueueIfNotActive rejects when a running job exists for the same account.
func TestEnqueueIfNotActive_RejectsRunning(t *testing.T) {
	q := newTestQueue(t)
	if _, err := q.EnqueueIfNotActive("backup", `{"account_id":1,"user_id":10}`, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(); err != nil {
		t.Fatal(err)
	}

	_, err := q.EnqueueIfNotActive("backup", `{"account_id":1,"user_id":10}`, 1)
	if err != ErrJobActive {
		t.Errorf("err = %v, want ErrJobActive", err)
	}
}

// EnqueueIfNotActive allows different accounts.
func TestEnqueueIfNotActive_DifferentAccounts(t *testing.T) {
	q := newTestQueue(t)
	if _, err := q.EnqueueIfNotActive("backup", `{"account_id":1,"user_id":10}`, 1); err != nil {
		t.Fatal(err)
	}

	j, err := q.EnqueueIfNotActive("backup", `{"account_id":2,"user_id":10}`, 2)
	if err != nil {
		t.Fatal(err)
	}
	if j == nil {
		t.Fatal("expected a job for different account")
	}
}

// EnqueueIfNotActive allows re-enqueue after previous job completes.
func TestEnqueueIfNotActive_AfterComplete(t *testing.T) {
	q := newTestQueue(t)
	j1, err := q.EnqueueIfNotActive("backup", `{"account_id":1,"user_id":10}`, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(); err != nil {
		t.Fatal(err)
	}
	if err := q.Complete(j1.ID); err != nil {
		t.Fatal(err)
	}

	j2, err := q.EnqueueIfNotActive("backup", `{"account_id":1,"user_id":10}`, 1)
	if err != nil {
		t.Fatal(err)
	}
	if j2 == nil {
		t.Fatal("expected a new job after previous completed")
	}
}

// UpdateProgress stores progress for a running job.
func TestUpdateProgress(t *testing.T) {
	q := newTestQueue(t)
	if _, err := q.Enqueue("backup", `{"account_id":1,"user_id":10}`); err != nil {
		t.Fatal(err)
	}
	j, err := q.Claim()
	if err != nil {
		t.Fatal(err)
	}

	progress := `{"folder":"INBOX","folder_index":1,"folder_total":3,"new_messages":5}`
	if err := q.UpdateProgress(j.ID, progress); err != nil {
		t.Fatal(err)
	}

	got, err := q.GetByID(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Progress != progress {
		t.Errorf("Progress = %q, want %q", got.Progress, progress)
	}
}

func TestGetByIDForUser(t *testing.T) {
	q := newTestQueue(t)

	const userA int64 = 42
	const userB int64 = 99

	j, err := q.Enqueue("backup", `{"account_id":1,"user_id":42}`)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.Claim(); err != nil {
		t.Fatal(err)
	}
	wantErr := "folder INBOX: timeout\nfolder Sent: timeout"
	if err := q.Fail(j.ID, wantErr); err != nil {
		t.Fatal(err)
	}

	t.Run("correct user retrieves job", func(t *testing.T) {
		got, err := q.GetByIDForUser(j.ID, userA)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.ID != j.ID {
			t.Errorf("ID = %d, want %d", got.ID, j.ID)
		}
		if got.Error != wantErr {
			t.Errorf("Error = %q, want %q", got.Error, wantErr)
		}
	})

	t.Run("wrong user gets ErrJobNotFound", func(t *testing.T) {
		_, err := q.GetByIDForUser(j.ID, userB)
		if !errors.Is(err, ErrJobNotFound) {
			t.Errorf("err = %v, want ErrJobNotFound", err)
		}
	})

	t.Run("non-existent id gets ErrJobNotFound", func(t *testing.T) {
		_, err := q.GetByIDForUser(99999, userA)
		if !errors.Is(err, ErrJobNotFound) {
			t.Errorf("err = %v, want ErrJobNotFound", err)
		}
	})
}
