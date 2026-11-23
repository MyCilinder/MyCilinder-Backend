package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/store"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

// ErrNoop: the video is already in the target status (a duplicate request). The status
// consumer acknowledges the message; HTTP callers decide what it means for them.
var ErrNoop = errors.New("lifecycle: already in target status")

// Changes are the column values an action sets besides status and updated_at.
type Changes struct {
	DurationSeconds *int     // ready
	Renditions      []string // ready
	FailureReason   string   // fail, timeout
}

// Invalidator drops cached copies of a video. *cache.Videos implements it.
type Invalidator interface {
	Invalidate(ctx context.Context, id string)
}

// Machine applies transitions on the writer.
type Machine struct {
	writer *pgxpool.Pool
	inv    Invalidator
}

// NewMachine returns a Machine. inv may be nil when no cache is used.
func NewMachine(writer *pgxpool.Pool, inv Invalidator) *Machine {
	return &Machine{writer: writer, inv: inv}
}

// Transition applies a in its own transaction. within, if not nil, runs in the same
// transaction after the status change (for example, the audit insert); if it fails,
// the whole change rolls back. The cache entry is dropped after commit.
//
// Errors: video.ErrNotFound, ErrNoop, *video.ConflictError, or a database error.
func (m *Machine) Transition(ctx context.Context, id string, a Action, ch Changes,
	within func(ctx context.Context, tx pgx.Tx) error) error {

	err := pgx.BeginFunc(ctx, m.writer, func(tx pgx.Tx) error {
		if err := Apply(ctx, tx, id, a, ch); err != nil {
			return err
		}
		if within != nil {
			return within(ctx, tx)
		}
		return nil
	})
	if err != nil {
		return err
	}
	m.Invalidate(ctx, id)
	return nil
}

// Invalidate drops the cached copies of a video. Callers that use Apply inside their own
// transaction call it after commit.
func (m *Machine) Invalidate(ctx context.Context, id string) {
	if m.inv != nil {
		m.inv.Invalidate(ctx, strings.ToLower(id))
	}
}

// Apply runs the conditional UPDATE for a on db (normally a transaction). When no row
// changes it reads the current status to tell a missing video, a duplicate, and a
// disallowed transition apart.
func Apply(ctx context.Context, db store.DBTX, id string, a Action, ch Changes) error {
	rule, ok := table[a]
	if !ok {
		return fmt.Errorf("lifecycle: unknown action %q", a)
	}
	from := make([]string, len(rule.From))
	for i, s := range rule.From {
		from[i] = string(s)
	}

	args := []any{id, string(rule.To), from}
	set := `status = $2::catalog.video_status, updated_at = now()`
	switch a {
	case Ready:
		renditions := ch.Renditions
		if renditions == nil {
			renditions = []string{}
		}
		args = append(args, ch.DurationSeconds, renditions)
		set += `, duration_seconds = $4, renditions = $5::text[],
			published_at = COALESCE(published_at, now()), failure_reason = NULL`
	case Fail, Timeout:
		args = append(args, ch.FailureReason)
		set += `, failure_reason = $4`
	case Delete:
		set += `, deleted_at = now()`
	}

	tag, err := db.Exec(ctx,
		`UPDATE catalog.videos SET `+set+`
		 WHERE id = $1::uuid AND status::text = ANY($3::text[])`, args...)
	if err != nil {
		return fmt.Errorf("lifecycle %s: %w", a, err)
	}
	if tag.RowsAffected() == 1 {
		return nil
	}

	var current string
	err = db.QueryRow(ctx, `SELECT status::text FROM catalog.videos WHERE id = $1::uuid`, id).Scan(&current)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return video.ErrNotFound
	case err != nil:
		return fmt.Errorf("lifecycle %s: read status: %w", a, err)
	case video.Status(current) == rule.To:
		return ErrNoop
	default:
		return &video.ConflictError{Current: video.Status(current)}
	}
}
