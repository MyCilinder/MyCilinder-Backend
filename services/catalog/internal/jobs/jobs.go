// Package jobs runs Catalog's periodic jobs (Architecture §4.2):
//
//	expiry   every 15 min  PENDING_UPLOAD older than 24 h  -> EXPIRED
//	sweep    every 15 min  PROCESSING older than 2 h       -> FAILED ("processing timed out")
//	cleanup  daily         HLS outputs of videos DELETED more than 7 days ago are removed
//
// Every task may run the jobs. Each run opens a transaction and takes
// pg_try_advisory_xact_lock with a per-job key; if another task holds it the run is
// skipped. The lock is released when the transaction ends, also if the task dies.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/lifecycle"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

// Advisory lock keys, one per job. Arbitrary constants, unique in the database.
const (
	LockExpiry  int64 = 0x0CA7_0001
	LockSweep   int64 = 0x0CA7_0002
	LockCleanup int64 = 0x0CA7_0003
)

// BatchSize bounds the rows one run of the status jobs changes.
const BatchSize = 100

// TimeoutReason is the failure reason the sweep stores (shown to the creator).
const TimeoutReason = "processing timed out"

// Job is one unit of periodic work. Run is called inside a transaction that holds the
// job's advisory lock, and returns how many items it handled.
type Job struct {
	Name     string
	LockKey  int64
	Interval time.Duration
	Run      func(ctx context.Context, tx pgx.Tx) (int, error)
}

// Runner starts jobs on their intervals.
type Runner struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	jobs []Job
}

// NewRunner returns a Runner on pool (the writer).
func NewRunner(pool *pgxpool.Pool, log *slog.Logger, jobs ...Job) *Runner {
	if log == nil {
		log = slog.Default()
	}
	return &Runner{pool: pool, log: log.With("component", "jobs"), jobs: jobs}
}

// Run starts every job and blocks until ctx is cancelled. A run in progress finishes
// first (its transaction is not cancelled by shutdown).
func (r *Runner) Run(ctx context.Context) {
	done := make(chan struct{}, len(r.jobs))
	for _, j := range r.jobs {
		go func(j Job) {
			r.loop(ctx, j)
			done <- struct{}{}
		}(j)
	}
	for range r.jobs {
		<-done
	}
}

func (r *Runner) loop(ctx context.Context, j Job) {
	log := r.log.With("job", j.Name)
	log.Info("job scheduled", "interval", j.Interval.String())
	for {
		// Jitter (up to 10% of the interval) spreads the tasks' runs, so they do not all
		// try the lock at the same instant.
		wait := j.Interval + time.Duration(rand.Int64N(int64(j.Interval/10)+1))
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		n, ran, err := r.RunOnce(context.WithoutCancel(ctx), j)
		switch {
		case err != nil:
			log.Error("job failed", "err", err)
		case !ran:
			log.Debug("job skipped; another task holds the lock")
		case n > 0:
			log.Info("job done", "items", n)
		default:
			log.Debug("job done", "items", 0)
		}
	}
}

// RunOnce runs j once under its advisory lock. ran is false when the lock was taken.
func (r *Runner) RunOnce(ctx context.Context, j Job) (n int, ran bool, err error) {
	ctx, cancel := context.WithTimeout(ctx, max(j.Interval, time.Minute))
	defer cancel()
	err = pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock($1)`, j.LockKey).Scan(&ran); err != nil {
			return fmt.Errorf("advisory lock: %w", err)
		}
		if !ran {
			return nil
		}
		var err error
		n, err = j.Run(ctx, tx)
		return err
	})
	return n, ran, err
}

// Invalidator is implemented by *lifecycle.Machine.
type Invalidator interface {
	Invalidate(ctx context.Context, id string)
}

// Expiry returns the job that expires uploads never completed (FR-UP-08).
func Expiry(interval, olderThan time.Duration, inv Invalidator) Job {
	return statusJob("expiry", LockExpiry, interval, video.StatusPendingUpload, olderThan,
		lifecycle.Expire, lifecycle.Changes{}, inv)
}

// Sweep returns the job that fails videos stuck in PROCESSING (failure mode F5).
func Sweep(interval, olderThan time.Duration, inv Invalidator) Job {
	return statusJob("sweep", LockSweep, interval, video.StatusProcessing, olderThan,
		lifecycle.Timeout, lifecycle.Changes{FailureReason: TimeoutReason}, inv)
}

// statusJob selects up to BatchSize videos in status whose updated_at is older than
// olderThan and applies action to each through the lifecycle, in the job's transaction.
// The age is measured from updated_at, which is set when the video enters the status.
func statusJob(name string, key int64, interval time.Duration, status video.Status, olderThan time.Duration,
	action lifecycle.Action, ch lifecycle.Changes, inv Invalidator) Job {

	return Job{Name: name, LockKey: key, Interval: interval, Run: func(ctx context.Context, tx pgx.Tx) (int, error) {
		rows, err := tx.Query(ctx,
			`SELECT id::text FROM catalog.videos
			 WHERE status = $1::catalog.video_status AND updated_at < now() - $2::interval
			 ORDER BY updated_at
			 LIMIT $3`, string(status), olderThan.String(), BatchSize)
		if err != nil {
			return 0, err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return 0, err
		}
		n := 0
		var changed []string
		for _, id := range ids {
			err := lifecycle.Apply(ctx, tx, id, action, ch)
			var conflict *video.ConflictError
			switch {
			case err == nil:
				n++
				changed = append(changed, id)
			case errors.Is(err, lifecycle.ErrNoop), errors.As(err, &conflict), errors.Is(err, video.ErrNotFound):
				// Changed by an event between the SELECT and the UPDATE. Nothing to do.
			default:
				return 0, err
			}
		}
		// Invalidate after the caller commits would be exact; doing it here is safe
		// because these videos are never READY before or after, so no cache entry exists.
		if inv != nil {
			for _, id := range changed {
				inv.Invalidate(ctx, id)
			}
		}
		return n, nil
	}}
}
