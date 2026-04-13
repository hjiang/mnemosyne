// Package jobs implements a minimal persistent job queue backed by SQLite.
package jobs

import (
	"database/sql"
	"fmt"
	"time"
)

// ErrJobActive indicates that an active job already exists for the given account.
var ErrJobActive = fmt.Errorf("active job already exists for this account")

// ErrJobNotFound indicates that a job does not exist or is not accessible to the requesting user.
var ErrJobNotFound = fmt.Errorf("job not found")

// Job represents a queued job.
type Job struct {
	ID         int64
	Kind       string
	Payload    string
	State      string
	Attempts   int
	Error      string
	Progress   string
	CreatedAt  int64
	StartedAt  *int64
	FinishedAt *int64
}

// Queue provides job queue operations.
type Queue struct {
	db  *sql.DB
	now func() time.Time
}

// NewQueue creates a job queue.
func NewQueue(db *sql.DB, now func() time.Time) *Queue {
	return &Queue{db: db, now: now}
}

// Enqueue adds a new job to the queue.
func (q *Queue) Enqueue(kind, payload string) (*Job, error) {
	now := q.now().Unix()
	res, err := q.db.Exec(
		"INSERT INTO jobs (kind, payload, state, created_at) VALUES (?, ?, 'pending', ?)",
		kind, payload, now)
	if err != nil {
		return nil, fmt.Errorf("enqueuing job: %w", err)
	}
	id, _ := res.LastInsertId()
	return &Job{
		ID:        id,
		Kind:      kind,
		Payload:   payload,
		State:     "pending",
		CreatedAt: now,
	}, nil
}

// Claim atomically transitions a pending job to running and returns it.
// Returns nil if no pending jobs exist.
func (q *Queue) Claim() (*Job, error) {
	now := q.now().Unix()
	tx, err := q.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var j Job
	err = tx.QueryRow(
		`SELECT id, kind, payload, state, attempts, COALESCE(error, ''), COALESCE(progress, ''), created_at
		 FROM jobs WHERE state = 'pending' ORDER BY id LIMIT 1`,
	).Scan(&j.ID, &j.Kind, &j.Payload, &j.State, &j.Attempts, &j.Error, &j.Progress, &j.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("selecting pending job: %w", err)
	}

	_, err = tx.Exec(
		"UPDATE jobs SET state = 'running', started_at = ?, attempts = attempts + 1 WHERE id = ?",
		now, j.ID)
	if err != nil {
		return nil, fmt.Errorf("claiming job: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing claim: %w", err)
	}

	j.State = "running"
	j.Attempts++
	j.StartedAt = &now
	return &j, nil
}

// EnqueueIfNotActive enqueues a job only if no pending or running job exists
// for the same account (identified by json_extract on the payload).
// Returns ErrJobActive if an active job already exists.
func (q *Queue) EnqueueIfNotActive(kind, payload string, accountID int64) (*Job, error) {
	now := q.now().Unix()
	tx, err := q.db.Begin()
	if err != nil {
		return nil, fmt.Errorf("beginning transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var count int
	err = tx.QueryRow(
		`SELECT COUNT(*) FROM jobs
		 WHERE kind = ? AND state IN ('pending', 'running')
		 AND json_extract(payload, '$.account_id') = ?`,
		kind, accountID,
	).Scan(&count)
	if err != nil {
		return nil, fmt.Errorf("checking active jobs: %w", err)
	}
	if count > 0 {
		return nil, ErrJobActive
	}

	res, err := tx.Exec(
		"INSERT INTO jobs (kind, payload, state, created_at) VALUES (?, ?, 'pending', ?)",
		kind, payload, now)
	if err != nil {
		return nil, fmt.Errorf("enqueuing job: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("committing enqueue: %w", err)
	}

	id, _ := res.LastInsertId()
	return &Job{
		ID:        id,
		Kind:      kind,
		Payload:   payload,
		State:     "pending",
		CreatedAt: now,
	}, nil
}

// UpdateProgress stores progress information for a running job.
func (q *Queue) UpdateProgress(jobID int64, progress string) error {
	_, err := q.db.Exec(
		"UPDATE jobs SET progress = ? WHERE id = ? AND state = 'running'",
		progress, jobID)
	if err != nil {
		return fmt.Errorf("updating progress: %w", err)
	}
	return nil
}

// Complete marks a running job as done.
func (q *Queue) Complete(jobID int64) error {
	now := q.now().Unix()
	_, err := q.db.Exec(
		"UPDATE jobs SET state = 'done', finished_at = ? WHERE id = ? AND state = 'running'",
		now, jobID)
	if err != nil {
		return fmt.Errorf("completing job: %w", err)
	}
	return nil
}

// Fail marks a running job as failed with an error message.
func (q *Queue) Fail(jobID int64, errMsg string) error {
	now := q.now().Unix()
	_, err := q.db.Exec(
		"UPDATE jobs SET state = 'failed', error = ?, finished_at = ? WHERE id = ? AND state = 'running'",
		errMsg, now, jobID)
	if err != nil {
		return fmt.Errorf("failing job: %w", err)
	}
	return nil
}

// ListByUser returns the most recent jobs for the given user, newest first.
// It filters by user_id inside the JSON payload column.
// enforces user isolation
func (q *Queue) ListByUser(userID int64, limit int) ([]*Job, error) {
	rows, err := q.db.Query(
		`SELECT id, kind, payload, state, attempts, COALESCE(error, ''), COALESCE(progress, ''), created_at, started_at, finished_at
		 FROM jobs
		 WHERE json_extract(payload, '$.user_id') = ?
		 ORDER BY id DESC
		 LIMIT ?`,
		userID, limit)
	if err != nil {
		return nil, fmt.Errorf("listing jobs for user: %w", err)
	}
	defer rows.Close() //nolint:errcheck

	var result []*Job
	for rows.Next() {
		var j Job
		if err := rows.Scan(&j.ID, &j.Kind, &j.Payload, &j.State, &j.Attempts, &j.Error, &j.Progress, &j.CreatedAt, &j.StartedAt, &j.FinishedAt); err != nil {
			return nil, fmt.Errorf("scanning job row: %w", err)
		}
		result = append(result, &j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating job rows: %w", err)
	}
	return result, nil
}

// GetByIDForUser retrieves a job by ID scoped to the given user.
// Returns ErrJobNotFound if not found or owned by a different user.
// enforces user isolation
func (q *Queue) GetByIDForUser(id, userID int64) (*Job, error) {
	var j Job
	err := q.db.QueryRow(
		`SELECT id, kind, payload, state, attempts, COALESCE(error, ''), COALESCE(progress, ''), created_at, started_at, finished_at
		 FROM jobs WHERE id = ? AND json_extract(payload, '$.user_id') = ?`,
		id, userID,
	).Scan(&j.ID, &j.Kind, &j.Payload, &j.State, &j.Attempts, &j.Error, &j.Progress, &j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	if err == sql.ErrNoRows {
		return nil, ErrJobNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("getting job for user: %w", err)
	}
	return &j, nil
}

// GetByID retrieves a job by ID.
func (q *Queue) GetByID(id int64) (*Job, error) {
	var j Job
	err := q.db.QueryRow(
		`SELECT id, kind, payload, state, attempts, COALESCE(error, ''), COALESCE(progress, ''), created_at, started_at, finished_at
		 FROM jobs WHERE id = ?`, id,
	).Scan(&j.ID, &j.Kind, &j.Payload, &j.State, &j.Attempts, &j.Error, &j.Progress, &j.CreatedAt, &j.StartedAt, &j.FinishedAt)
	if err != nil {
		return nil, fmt.Errorf("getting job: %w", err)
	}
	return &j, nil
}
