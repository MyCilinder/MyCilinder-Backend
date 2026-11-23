// Package store implements the Catalog repositories on PostgreSQL (pgx).
//
// The service packages (internal/video, browse, studio, moderation) define the interfaces
// they need; the types here satisfy them. Queries are always parameterized (SEC-07).
//
// Reads use the reader pool (RDS Proxy read-only endpoint). If the reader cannot be
// reached, the read is retried once on the writer (Data Design §2.2). Writes, and reads
// that must see the caller's own write, use the writer or the caller's transaction.
package store

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/cursor"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

// DBTX is satisfied by *pgxpool.Pool and pgx.Tx.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Videos reads and edits catalog.videos. The catalog_svc role has SELECT and UPDATE on
// the table, and SELECT on auth.v_user_public and interactions.v_video_stats.
type Videos struct {
	writer *pgxpool.Pool
	reader *pgxpool.Pool
}

// NewVideos returns a Videos repository. reader may be nil or the same pool as writer.
func NewVideos(writer, reader *pgxpool.Pool) *Videos {
	if reader == nil {
		reader = writer
	}
	return &Videos{writer: writer, reader: reader}
}

// query collects positional arguments so a statement can be assembled from parts.
type query struct {
	args []any
}

func (q *query) arg(v any) string {
	q.args = append(q.args, v)
	return "$" + strconv.Itoa(len(q.args))
}

// columns selects one video with its creator name and counters. A missing profile gets
// the placeholder name and a missing stats row counts as zero.
func (q *query) columns() string {
	return `v.id::text, v.owner_id::text, COALESCE(u.display_name, ` + q.arg(video.UnknownCreatorName) + `),
		v.title, v.description, v.status::text, v.failure_reason,
		v.duration_seconds, v.renditions,
		v.created_at, v.updated_at, v.published_at, v.deleted_at,
		COALESCE(s.view_count, 0), COALESCE(s.like_count, 0), COALESCE(s.comment_count, 0)`
}

const joins = `
	FROM catalog.videos v
	LEFT JOIN auth.v_user_public u ON u.id = v.owner_id
	LEFT JOIN interactions.v_video_stats s ON s.video_id = v.id`

// GetByID returns the video in any status, or video.ErrNotFound.
func (r *Videos) GetByID(ctx context.Context, id string) (video.Video, error) {
	var out video.Video
	err := r.read(ctx, func(db DBTX) error {
		var err error
		out, err = GetByIDTx(ctx, db, id)
		return err
	})
	return out, err
}

// GetByIDTx reads the video through db (a transaction or the writer), so it sees writes
// the caller just made.
func GetByIDTx(ctx context.Context, db DBTX, id string) (video.Video, error) {
	var q query
	sql := `SELECT ` + q.columns() + joins + ` WHERE v.id = ` + q.arg(id) + `::uuid`
	v, err := scanVideo(db.QueryRow(ctx, sql, q.args...))
	if errors.Is(err, pgx.ErrNoRows) {
		return video.Video{}, video.ErrNotFound
	}
	if err != nil {
		return video.Video{}, fmt.Errorf("get video: %w", err)
	}
	return v, nil
}

// ListRecent returns READY videos by (published_at, id) descending (FR-BR-01).
func (r *Videos) ListRecent(ctx context.Context, after *cursor.Cursor, limit int) ([]video.Video, error) {
	var q query
	sql := `SELECT ` + q.columns() + joins + `
		WHERE v.status = 'READY' AND v.published_at IS NOT NULL`
	if after != nil {
		sql += ` AND (v.published_at, v.id) < (` + q.arg(after.Time) + `::timestamptz, ` + q.arg(after.ID) + `::uuid)`
	}
	sql += ` ORDER BY v.published_at DESC, v.id DESC LIMIT ` + q.arg(limit)
	return r.list(ctx, sql, q.args)
}

// ListMostViewed returns READY videos by (view count, id) descending (FR-BR-02).
func (r *Videos) ListMostViewed(ctx context.Context, after *cursor.Cursor, limit int) ([]video.Video, error) {
	var q query
	sql := `SELECT ` + q.columns() + joins + `
		WHERE v.status = 'READY' AND v.published_at IS NOT NULL`
	if after != nil {
		sql += ` AND (COALESCE(s.view_count, 0), v.id) < (` + q.arg(after.Int) + `::bigint, ` + q.arg(after.ID) + `::uuid)`
	}
	sql += ` ORDER BY COALESCE(s.view_count, 0) DESC, v.id DESC LIMIT ` + q.arg(limit)
	return r.list(ctx, sql, q.args)
}

// ListByCreator returns one creator's READY videos, newest first (FR-BR-07).
func (r *Videos) ListByCreator(ctx context.Context, ownerID string, after *cursor.Cursor, limit int) ([]video.Video, error) {
	var q query
	sql := `SELECT ` + q.columns() + joins + `
		WHERE v.owner_id = ` + q.arg(ownerID) + `::uuid AND v.status = 'READY' AND v.published_at IS NOT NULL`
	if after != nil {
		sql += ` AND (v.published_at, v.id) < (` + q.arg(after.Time) + `::timestamptz, ` + q.arg(after.ID) + `::uuid)`
	}
	sql += ` ORDER BY v.published_at DESC, v.id DESC LIMIT ` + q.arg(limit)
	return r.list(ctx, sql, q.args)
}

// ListByOwner returns the owner's videos in every status except DELETED, by
// (created_at, id) descending (FR-CR-01). It reads the writer so a video the owner just
// edited or deleted shows its new state.
func (r *Videos) ListByOwner(ctx context.Context, ownerID string, after *cursor.Cursor, limit int) ([]video.Video, error) {
	var q query
	sql := `SELECT ` + q.columns() + joins + `
		WHERE v.owner_id = ` + q.arg(ownerID) + `::uuid AND v.status <> 'DELETED'`
	if after != nil {
		sql += ` AND (v.created_at, v.id) < (` + q.arg(after.Time) + `::timestamptz, ` + q.arg(after.ID) + `::uuid)`
	}
	sql += ` ORDER BY v.created_at DESC, v.id DESC LIMIT ` + q.arg(limit)
	return scanAll(r.writer.Query(ctx, sql, q.args...))
}

// CreatorExists reports whether the user has a public profile.
func (r *Videos) CreatorExists(ctx context.Context, userID string) (bool, error) {
	var ok bool
	err := r.read(ctx, func(db DBTX) error {
		return db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM auth.v_user_public WHERE id = $1::uuid)`, userID).Scan(&ok)
	})
	if err != nil {
		return false, fmt.Errorf("creator exists: %w", err)
	}
	return ok, nil
}

// Search returns READY videos matching the plain-word query with the Spanish
// configuration, by (rank, id) descending (FR-BR-03). rank is ts_rank as float4 (real);
// the cursor carries that exact value, so the comparison matches the previous page.
func (r *Videos) Search(ctx context.Context, text string, after *cursor.Cursor, limit int) ([]video.Ranked, error) {
	var q query
	sql := `SELECT * FROM (
		SELECT ` + q.columns() + `, ts_rank(v.search_vector, tq)::real AS rank` + joins + `
		CROSS JOIN plainto_tsquery('spanish', ` + q.arg(text) + `) AS tq
		WHERE v.status = 'READY' AND v.published_at IS NOT NULL AND v.search_vector @@ tq
	) hit`
	// Column 1 is the id and the last column is the rank.
	if after != nil {
		sql += ` WHERE (hit.rank, hit.id::uuid) < (` + q.arg(after.Float) + `::real, ` + q.arg(after.ID) + `::uuid)`
	}
	sql += ` ORDER BY hit.rank DESC, hit.id::uuid DESC LIMIT ` + q.arg(limit)

	var out []video.Ranked
	err := r.read(ctx, func(db DBTX) error {
		out = out[:0]
		rows, err := db.Query(ctx, sql, q.args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h video.Ranked
			v, err := scanVideo(rows, &h.Rank)
			if err != nil {
				return err
			}
			h.Video = v
			out = append(out, h)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	return out, nil
}

// LockForUpdate locks the row inside tx and returns its owner and status, or
// video.ErrNotFound. Edits and deletes call it first so concurrent requests on the same
// video run one after the other.
func LockForUpdate(ctx context.Context, tx DBTX, id string) (ownerID string, status video.Status, err error) {
	var st string
	err = tx.QueryRow(ctx,
		`SELECT owner_id::text, status::text FROM catalog.videos WHERE id = $1::uuid FOR UPDATE`, id).
		Scan(&ownerID, &st)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", video.ErrNotFound
	}
	if err != nil {
		return "", "", fmt.Errorf("lock video: %w", err)
	}
	return ownerID, video.Status(st), nil
}

// UpdateText changes the title and/or description (nil leaves a field unchanged) and
// bumps updated_at. The caller validates the values; the CHECK constraints are the last
// line of defense.
func UpdateText(ctx context.Context, tx DBTX, id string, title, description *string) error {
	_, err := tx.Exec(ctx,
		`UPDATE catalog.videos
		 SET title = COALESCE($2, title), description = COALESCE($3, description), updated_at = now()
		 WHERE id = $1::uuid`, id, title, description)
	if err != nil {
		return fmt.Errorf("update video: %w", err)
	}
	return nil
}

func (r *Videos) list(ctx context.Context, sql string, args []any) ([]video.Video, error) {
	var out []video.Video
	err := r.read(ctx, func(db DBTX) error {
		var err error
		out, err = scanAll(db.Query(ctx, sql, args...))
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("list videos: %w", err)
	}
	return out, nil
}

// read runs fn on the reader, and once more on the writer if the reader could not be
// reached. Errors returned by the server (SQL errors) and "no rows" are not retried.
func (r *Videos) read(ctx context.Context, fn func(DBTX) error) error {
	err := fn(r.reader)
	if err == nil || r.reader == r.writer || !isConnectionError(ctx, err) {
		return err
	}
	return fn(r.writer)
}

func isConnectionError(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, pgx.ErrNoRows) || errors.Is(err, video.ErrNotFound) {
		return false
	}
	var pgErr *pgconn.PgError
	return !errors.As(err, &pgErr)
}

func scanAll(rows pgx.Rows, err error) ([]video.Video, error) {
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []video.Video{}
	for rows.Next() {
		v, err := scanVideo(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// scanVideo reads the columns() list, followed by any extra destinations.
func scanVideo(row pgx.Row, extra ...any) (video.Video, error) {
	var (
		v      video.Video
		status string
	)
	dest := append([]any{
		&v.ID, &v.OwnerID, &v.OwnerName,
		&v.Title, &v.Description, &status, &v.FailureReason,
		&v.DurationSeconds, &v.Renditions,
		&v.CreatedAt, &v.UpdatedAt, &v.PublishedAt, &v.DeletedAt,
		&v.ViewCount, &v.LikeCount, &v.CommentCount,
	}, extra...)
	if err := row.Scan(dest...); err != nil {
		return video.Video{}, err
	}
	v.Status = video.Status(status)
	if v.Renditions == nil {
		v.Renditions = []string{}
	}
	return v, nil
}
