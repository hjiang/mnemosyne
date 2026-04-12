// Package scheduler provides cron-based backup scheduling.
package scheduler

import (
	"encoding/json"
	"fmt"
	"log"

	"github.com/robfig/cron/v3"

	"github.com/hjiang/mnemosyne/internal/accounts"
	"github.com/hjiang/mnemosyne/internal/jobs"
)

// BackupPayload is the JSON payload for a backup job.
type BackupPayload struct {
	AccountID int64 `json:"account_id"`
	UserID    int64 `json:"user_id"`
}

// Scheduler enqueues backup jobs on a cron schedule.
type Scheduler struct {
	cron     *cron.Cron
	accounts *accounts.Repo
	queue    *jobs.Queue
}

// New creates a scheduler. The schedule is a standard 5-field cron expression
// (e.g., "0 3 * * *" for daily at 3 AM).
func New(schedule string, accts *accounts.Repo, queue *jobs.Queue) (*Scheduler, error) {
	c := cron.New()
	s := &Scheduler{
		cron:     c,
		accounts: accts,
		queue:    queue,
	}

	_, err := c.AddFunc(schedule, s.tick)
	if err != nil {
		return nil, fmt.Errorf("invalid cron schedule %q: %w", schedule, err)
	}

	return s, nil
}

// Start begins the cron scheduler.
func (s *Scheduler) Start() {
	s.cron.Start()
}

// Stop gracefully stops the cron scheduler.
func (s *Scheduler) Stop() {
	s.cron.Stop()
}

func (s *Scheduler) tick() {
	if err := s.EnqueueAll(); err != nil {
		log.Printf("scheduler: %v", err)
	}
}

// EnqueueAll enqueues a backup job for every enabled account.
// This is exported so manual triggers can reuse the same logic.
func (s *Scheduler) EnqueueAll() error {
	// We need to iterate all users' accounts. Since the accounts repo
	// requires a user_id, we query the database directly for all accounts
	// that have at least one enabled folder.
	rows, err := s.accounts.ListAllEnabled()
	if err != nil {
		return fmt.Errorf("listing enabled accounts: %w", err)
	}

	for _, info := range rows {
		payload, _ := json.Marshal(BackupPayload{
			AccountID: info.AccountID,
			UserID:    info.UserID,
		})
		if _, err := s.queue.Enqueue("backup", string(payload)); err != nil {
			log.Printf("scheduler: failed to enqueue account %d: %v", info.AccountID, err)
		}
	}
	return nil
}
