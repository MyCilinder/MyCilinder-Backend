// Package testdb holds the fixtures for Catalog's integration tests (build tag
// integration). Nothing outside tests imports it.
//
//	TEST_DATABASE_URL        catalog_svc on the Compose Postgres (the code under test)
//	TEST_ADMIN_DATABASE_URL  postgres on the same database (fixtures and checks)
//
// catalog_svc cannot INSERT or DELETE videos, so fixtures are created and removed through
// the admin connection, with fresh random ids for every test.
package testdb

import (
	"context"
	"crypto/rand"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/db"
)

// Pool connects with the URL in env, or skips the test when it is unset.
func Pool(t testing.TB, env string) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv(env)
	if url == "" {
		t.Skip(env + " not set")
	}
	p, err := db.NewPool(context.Background(), db.Options{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.Close)
	return p
}

// Service is the catalog_svc pool.
func Service(t testing.TB) *pgxpool.Pool { return Pool(t, "TEST_DATABASE_URL") }

// UUID returns a random version-4 UUID.
func UUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Fixtures inserts rows as postgres and deletes them when the test ends.
type Fixtures struct {
	t     testing.TB
	Admin *pgxpool.Pool
}

// New returns Fixtures on the admin connection.
func New(t testing.TB) *Fixtures {
	return &Fixtures{t: t, Admin: Pool(t, "TEST_ADMIN_DATABASE_URL")}
}

// Exec runs a statement as postgres.
func (f *Fixtures) Exec(sql string, args ...any) {
	f.t.Helper()
	if _, err := f.Admin.Exec(context.Background(), sql, args...); err != nil {
		f.t.Fatalf("fixture: %v", err)
	}
}

// Scalar reads one value as postgres.
func (f *Fixtures) Scalar(dst any, sql string, args ...any) {
	f.t.Helper()
	if err := f.Admin.QueryRow(context.Background(), sql, args...).Scan(dst); err != nil {
		f.t.Fatalf("fixture query: %v", err)
	}
}

// User creates a profile.
func (f *Fixtures) User(name string) string {
	id := UUID()
	f.Exec(`INSERT INTO auth.users (id, display_name) VALUES ($1, $2)`, id, name)
	f.t.Cleanup(func() { f.Exec(`DELETE FROM auth.users WHERE id = $1`, id) })
	return id
}

// Video describes a fixture row. Zero values get defaults.
type Video struct {
	Owner       string
	Status      string // default READY
	Title       string // default "Recorrido por el campus"
	Description string
	PublishedAt *time.Time // default now() for READY and HIDDEN
	CreatedAt   *time.Time // default now()
	Duration    int        // default 30 for READY and HIDDEN
}

// Insert creates the video and returns its id.
func (f *Fixtures) Insert(v Video) string {
	f.t.Helper()
	if v.Owner == "" {
		v.Owner = UUID()
	}
	if v.Status == "" {
		v.Status = "READY"
	}
	if v.Title == "" {
		v.Title = "Recorrido por el campus"
	}
	media := v.Status == "READY" || v.Status == "HIDDEN" || v.Status == "DELETED"
	var dur any
	if media {
		if v.Duration == 0 {
			v.Duration = 30
		}
		dur = v.Duration
		if v.PublishedAt == nil {
			now := time.Now()
			v.PublishedAt = &now
		}
	}
	created := time.Now()
	if v.CreatedAt != nil {
		created = *v.CreatedAt
	}
	id := UUID()
	f.Exec(`INSERT INTO catalog.videos
	          (id, owner_id, title, description, status, content_type, size_bytes,
	           duration_seconds, published_at, created_at, updated_at)
	        VALUES ($1, $2, $3, $4, $5::catalog.video_status, 'video/mp4', 1000, $6, $7, $8, $8)`,
		id, v.Owner, v.Title, v.Description, v.Status, dur, v.PublishedAt, created)
	f.t.Cleanup(func() { f.Exec(`DELETE FROM catalog.videos WHERE id = $1`, id) })
	return id
}

// Stats sets the counters of a video.
func (f *Fixtures) Stats(videoID string, views, likes, comments int64) {
	f.Exec(`INSERT INTO interactions.video_stats (video_id, view_count, like_count, comment_count)
	        VALUES ($1, $2, $3, $4)
	        ON CONFLICT (video_id) DO UPDATE SET view_count = $2, like_count = $3, comment_count = $4`,
		videoID, views, likes, comments)
	f.t.Cleanup(func() { f.Exec(`DELETE FROM interactions.video_stats WHERE video_id = $1`, videoID) })
}

// Status returns the stored status of a video.
func (f *Fixtures) Status(id string) string {
	var s string
	f.Scalar(&s, `SELECT status::text FROM catalog.videos WHERE id = $1`, id)
	return s
}
