package jobs

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Handler processes a single job. Return an error to mark the job as failed.
type Handler func(ctx context.Context, job *Job) error

// WorkerPool manages a pool of job workers.
type WorkerPool struct {
	queue       *Queue
	handler     Handler
	maxWorkers  int
	maxAttempts int
	pollInterval time.Duration

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewWorkerPool creates a worker pool.
func NewWorkerPool(queue *Queue, handler Handler, maxWorkers, maxAttempts int) *WorkerPool {
	return &WorkerPool{
		queue:        queue,
		handler:      handler,
		maxWorkers:   maxWorkers,
		maxAttempts:  maxAttempts,
		pollInterval: 1 * time.Second,
	}
}

// Start launches the worker goroutines.
func (p *WorkerPool) Start() {
	ctx, cancel := context.WithCancel(context.Background()) //nolint:gosec // cancel is stored and called in Shutdown
	p.cancel = cancel

	for i := 0; i < p.maxWorkers; i++ {
		p.wg.Add(1)
		go p.run(ctx)
	}
}

// Shutdown signals workers to stop and waits up to the deadline.
func (p *WorkerPool) Shutdown(timeout time.Duration) {
	p.cancel()

	done := make(chan struct{})
	go func() {
		p.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		log.Printf("worker pool: shutdown timed out, some jobs may be left running")
	}
}

func (p *WorkerPool) run(ctx context.Context) {
	defer p.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		job, err := p.queue.Claim()
		if err != nil {
			log.Printf("worker: claim error: %v", err)
			p.sleep(ctx)
			continue
		}
		if job == nil {
			p.sleep(ctx)
			continue
		}

		p.execute(ctx, job)
	}
}

func (p *WorkerPool) execute(ctx context.Context, job *Job) {
	err := p.safeExecute(ctx, job)
	if err != nil {
		errMsg := err.Error()
		if job.Attempts >= p.maxAttempts {
			errMsg = fmt.Sprintf("max attempts (%d) reached: %s", p.maxAttempts, errMsg)
		}
		_ = p.queue.Fail(job.ID, errMsg)
	} else {
		_ = p.queue.Complete(job.ID)
	}
}

func (p *WorkerPool) safeExecute(ctx context.Context, job *Job) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return p.handler(ctx, job)
}

func (p *WorkerPool) sleep(ctx context.Context) {
	select {
	case <-ctx.Done():
	case <-time.After(p.pollInterval):
	}
}

// ReclaimStuck resets any jobs stuck in 'running' state back to 'pending'.
// Call this on startup before starting workers.
func (q *Queue) ReclaimStuck() (int64, error) {
	res, err := q.db.Exec("UPDATE jobs SET state = 'pending', started_at = NULL WHERE state = 'running'")
	if err != nil {
		return 0, fmt.Errorf("reclaiming stuck jobs: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
