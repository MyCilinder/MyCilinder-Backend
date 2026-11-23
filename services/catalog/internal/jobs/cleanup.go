package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
)

// CleanupBatchSize bounds the videos one cleanup run handles.
const CleanupBatchSize = 20

// OutputDeleter is implemented by *storage.Cleaner.
type OutputDeleter interface {
	DeleteOutputs(ctx context.Context, videoID string) (int, error)
}

// Cleanup returns the job that removes the HLS outputs and thumbnail of videos DELETED
// more than retention ago, and marks them with outputs_purged_at so they are not listed
// again (Catalog plan, decision 2). A video whose deletion fails keeps a NULL marker and
// is retried on the next run; the others still complete.
func Cleanup(interval, retention time.Duration, store OutputDeleter, log *slog.Logger) Job {
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "jobs", "job", "cleanup")
	return Job{Name: "cleanup", LockKey: LockCleanup, Interval: interval, Run: func(ctx context.Context, tx pgx.Tx) (int, error) {
		rows, err := tx.Query(ctx,
			`SELECT id::text FROM catalog.videos
			 WHERE status = 'DELETED' AND outputs_purged_at IS NULL AND deleted_at < now() - $1::interval
			 ORDER BY deleted_at
			 LIMIT $2`, retention.String(), CleanupBatchSize)
		if err != nil {
			return 0, err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return 0, err
		}
		done := 0
		for _, id := range ids {
			keys, err := store.DeleteOutputs(ctx, id)
			if err != nil {
				log.Error("output cleanup failed; will retry", "videoId", id, "err", err)
				continue
			}
			if _, err := tx.Exec(ctx,
				`UPDATE catalog.videos SET outputs_purged_at = now() WHERE id = $1::uuid`, id); err != nil {
				return done, err
			}
			log.Info("outputs removed", "videoId", id, "keys", keys)
			done++
		}
		return done, nil
	}}
}
