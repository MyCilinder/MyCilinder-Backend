//go:build integration

package store

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/db"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/testdb"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

// Run with the local stack up and migrations applied:
//
//	TEST_DATABASE_URL=postgres://catalog_svc:catalog_svc_local@127.0.0.1:5432/mycilinder?sslmode=disable \
//	TEST_ADMIN_DATABASE_URL=postgres://postgres:postgres@127.0.0.1:5432/mycilinder?sslmode=disable \
//	  go test -tags integration ./services/catalog/...
//
// The repository connects as catalog_svc, so the tests also exercise its grants.

func TestGetByIDReadyWithProfileAndStats(t *testing.T) {
	f := testdb.New(t)
	repo := NewVideos(testdb.Service(t), nil)

	owner := f.User("Diego Gómez")
	id := f.Insert(testdb.Video{Owner: owner, Description: "Un tutorial", Duration: 42})
	f.Exec(`UPDATE catalog.videos SET renditions = '{360p,720p}' WHERE id = $1`, id)
	f.Stats(id, 1500, 12, 3)

	got, err := repo.GetByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.OwnerID != owner || got.OwnerName != "Diego Gómez" {
		t.Errorf("ids or owner wrong: %+v", got)
	}
	if got.Status != video.StatusReady || got.Title != "Recorrido por el campus" || got.Description != "Un tutorial" {
		t.Errorf("fields wrong: %+v", got)
	}
	if got.DurationSeconds == nil || *got.DurationSeconds != 42 {
		t.Errorf("duration = %v", got.DurationSeconds)
	}
	if len(got.Renditions) != 2 || got.Renditions[0] != "360p" || got.Renditions[1] != "720p" {
		t.Errorf("renditions = %v", got.Renditions)
	}
	if got.PublishedAt == nil || got.CreatedAt.IsZero() {
		t.Errorf("timestamps: published=%v created=%v", got.PublishedAt, got.CreatedAt)
	}
	if got.ViewCount != 1500 || got.LikeCount != 12 || got.CommentCount != 3 {
		t.Errorf("counts = %d/%d/%d", got.ViewCount, got.LikeCount, got.CommentCount)
	}
}

func TestGetByIDMissingProfileAndStats(t *testing.T) {
	f := testdb.New(t)
	repo := NewVideos(testdb.Service(t), nil)

	id := f.Insert(testdb.Video{Status: "FAILED"})
	f.Exec(`UPDATE catalog.videos SET failure_reason = 'unsupported codec' WHERE id = $1`, id)

	got, err := repo.GetByID(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if got.OwnerName != video.UnknownCreatorName {
		t.Errorf("owner name = %q, want placeholder", got.OwnerName)
	}
	if got.ViewCount != 0 || got.LikeCount != 0 || got.CommentCount != 0 {
		t.Errorf("counts = %d/%d/%d, want zeros", got.ViewCount, got.LikeCount, got.CommentCount)
	}
	if got.FailureReason == nil || *got.FailureReason != "unsupported codec" {
		t.Errorf("failure reason = %v", got.FailureReason)
	}
	if got.DurationSeconds != nil || got.PublishedAt != nil || got.Renditions == nil || len(got.Renditions) != 0 {
		t.Errorf("media fields should be empty: %+v", got)
	}
}

func TestGetByIDUnknown(t *testing.T) {
	repo := NewVideos(testdb.Service(t), nil)
	if _, err := repo.GetByID(context.Background(), testdb.UUID()); !errors.Is(err, video.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// When the reader cannot be reached, the read goes to the writer (Data Design §2.2).
func TestGetByIDFallsBackToWriter(t *testing.T) {
	f := testdb.New(t)
	writer := testdb.Service(t)
	deadReader, err := db.NewPool(context.Background(), db.Options{
		URL: "postgres://catalog_svc:x@127.0.0.1:1/mycilinder?sslmode=disable&connect_timeout=1",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(deadReader.Close)

	id := f.Insert(testdb.Video{})
	repo := NewVideos(writer, deadReader)
	got, err := repo.GetByID(context.Background(), id)
	if err != nil || got.ID != id {
		t.Fatalf("got %q err %v, want the video from the writer", got.ID, err)
	}
	rows, err := repo.ListRecent(context.Background(), nil, 1)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list on dead reader: %d rows, err %v", len(rows), err)
	}
}

// catalog_svc changes rows but never creates or removes them (BR-05).
func TestCatalogRoleCannotInsertOrDelete(t *testing.T) {
	p := testdb.Service(t)
	ctx := context.Background()

	_, err := p.Exec(ctx, `INSERT INTO catalog.videos (owner_id, title, content_type, size_bytes)
	                      VALUES ($1, 'x', 'video/mp4', 1)`, testdb.UUID())
	assertInsufficientPrivilege(t, "INSERT", err)

	_, err = p.Exec(ctx, `DELETE FROM catalog.videos WHERE id = $1`, testdb.UUID())
	assertInsufficientPrivilege(t, "DELETE", err)

	// Cross-schema reads are allowed only through the views.
	_, err = p.Exec(ctx, `SELECT 1 FROM interactions.video_stats LIMIT 1`)
	assertInsufficientPrivilege(t, "SELECT interactions.video_stats", err)
	_, err = p.Exec(ctx, `SELECT 1 FROM auth.users LIMIT 1`)
	assertInsufficientPrivilege(t, "SELECT auth.users", err)

	// The audit trail is append-only for Catalog.
	_, err = p.Exec(ctx, `SELECT 1 FROM audit.audit_log LIMIT 1`)
	assertInsufficientPrivilege(t, "SELECT audit.audit_log", err)
}

func assertInsufficientPrivilege(t *testing.T, what string, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Errorf("%s: err = %v, want insufficient_privilege (42501)", what, err)
	}
}
