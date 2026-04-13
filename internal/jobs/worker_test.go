package jobs

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hjiang/mnemosyne/internal/db"
)

func newTestQueueForWorker(t *testing.T) *Queue {
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
	return NewQueue(database, time.Now)
}

// Test 12: Worker pool processes jobs.
func TestWorkerPool_ProcessesJobs(t *testing.T) {
	q := newTestQueueForWorker(t)
	for i := 0; i < 5; i++ {
		if _, err := q.Enqueue("test", fmt.Sprintf("%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	var processed atomic.Int32
	handler := func(_ context.Context, _ *Job) error {
		processed.Add(1)
		return nil
	}

	pool := NewWorkerPool(q, handler, 2, 3)
	pool.pollInterval = 50 * time.Millisecond
	pool.Start()

	// Wait for processing.
	deadline := time.After(5 * time.Second)
	for processed.Load() < 5 {
		select {
		case <-deadline:
			t.Fatalf("timed out; processed %d/5 jobs", processed.Load())
		case <-time.After(50 * time.Millisecond):
		}
	}

	pool.Shutdown(2 * time.Second)

	if got := processed.Load(); got != 5 {
		t.Errorf("processed %d, want 5", got)
	}
}

// Test 13: Failing job is marked failed after max attempts.
func TestWorkerPool_FailAfterMaxAttempts(t *testing.T) {
	q := newTestQueueForWorker(t)
	j, _ := q.Enqueue("test", "")

	handler := func(_ context.Context, _ *Job) error {
		return fmt.Errorf("always fails")
	}

	pool := NewWorkerPool(q, handler, 1, 1)
	pool.pollInterval = 50 * time.Millisecond
	pool.Start()

	time.Sleep(500 * time.Millisecond)
	pool.Shutdown(2 * time.Second)

	got, err := q.GetByID(j.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != "failed" {
		t.Errorf("state = %q, want failed", got.State)
	}
	if got.Attempts < 1 {
		t.Errorf("attempts = %d, want >= 1", got.Attempts)
	}
}

// Test 14: Graceful shutdown waits for in-flight jobs.
func TestWorkerPool_GracefulShutdown(t *testing.T) {
	q := newTestQueueForWorker(t)
	if _, err := q.Enqueue("test", ""); err != nil {
		t.Fatal(err)
	}

	started := make(chan struct{})
	handler := func(_ context.Context, _ *Job) error {
		close(started)
		// Simulate work that takes a bit but always completes.
		time.Sleep(200 * time.Millisecond)
		return nil
	}

	pool := NewWorkerPool(q, handler, 1, 3)
	pool.pollInterval = 50 * time.Millisecond
	pool.Start()

	<-started
	pool.Shutdown(5 * time.Second)

	// Job should be completed (worker finished within deadline).
	got, _ := q.GetByID(1)
	if got.State != "done" {
		t.Errorf("state = %q, want done", got.State)
	}
}

// Test 15: ReclaimStuck marks running jobs as failed.
func TestReclaimStuck(t *testing.T) {
	q := newTestQueueForWorker(t)
	j, _ := q.Enqueue("test", "")
	_, _ = q.Claim() // moves to running

	got, _ := q.GetByID(j.ID)
	if got.State != "running" {
		t.Fatalf("state = %q, want running", got.State)
	}

	n, err := q.ReclaimStuck()
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("reclaimed %d, want 1", n)
	}

	got, _ = q.GetByID(j.ID)
	if got.State != "failed" {
		t.Errorf("state = %q, want failed", got.State)
	}
	if got.Error != "interrupted: process stopped while job was running" {
		t.Errorf("error = %q, want interrupted message", got.Error)
	}
	if got.FinishedAt == nil {
		t.Error("finished_at should be set")
	}
}

// Test: Worker recovers from handler panic.
func TestWorkerPool_PanicRecovery(t *testing.T) {
	q := newTestQueueForWorker(t)
	if _, err := q.Enqueue("test", ""); err != nil {
		t.Fatal(err)
	}

	handler := func(_ context.Context, _ *Job) error {
		panic("test panic")
	}

	pool := NewWorkerPool(q, handler, 1, 1)
	pool.pollInterval = 50 * time.Millisecond
	pool.Start()

	time.Sleep(500 * time.Millisecond)
	pool.Shutdown(2 * time.Second)

	got, _ := q.GetByID(1)
	if got.State != "failed" {
		t.Errorf("state = %q, want failed", got.State)
	}
	if got.Error == "" {
		t.Error("expected error message from panic")
	}
}
