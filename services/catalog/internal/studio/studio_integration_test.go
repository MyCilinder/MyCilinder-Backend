//go:build integration

package studio_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/lifecycle"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/store"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/studio"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/testdb"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

type recorder struct {
	mu  sync.Mutex
	ids []string
}

func (r *recorder) Invalidate(_ context.Context, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = append(r.ids, id)
}

func newService(t *testing.T) (*studio.Service, *recorder) {
	pool := testdb.Service(t)
	inv := &recorder{}
	return studio.NewService(pool, store.NewVideos(pool, nil), lifecycle.NewMachine(pool, inv)), inv
}

func ptr(s string) *string { return &s }

func TestListMyVideos(t *testing.T) {
	f := testdb.New(t)
	svc, _ := newService(t)
	ctx := context.Background()
	owner := f.User("Dueña")

	statuses := []string{"PENDING_UPLOAD", "PROCESSING", "READY", "FAILED", "HIDDEN", "EXPIRED", "DELETED", "READY", "READY"}
	var want []string
	for i, st := range statuses {
		created := time.Now().Add(-time.Duration(i) * time.Minute)
		id := f.Insert(testdb.Video{Owner: owner, Status: st, CreatedAt: &created})
		if st != "DELETED" {
			want = append(want, id)
		}
		if i == 2 {
			f.Stats(id, 10, 4, 2)
		}
	}
	f.Insert(testdb.Video{}) // someone else's

	var got []video.Video
	cur := ""
	for {
		p, err := svc.List(ctx, owner, cur, 3)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, p.Items...)
		if p.NextCursor == "" {
			break
		}
		cur = p.NextCursor
	}
	ids := make([]string, len(got))
	for i, v := range got {
		ids[i] = v.ID
	}
	if strings.Join(ids, ",") != strings.Join(want, ",") {
		t.Fatalf("ids:\n got  %v\n want %v", ids, want)
	}
	if got[2].LikeCount != 4 || got[2].CommentCount != 2 || got[2].ViewCount != 10 {
		t.Errorf("counts = %d/%d/%d", got[2].ViewCount, got[2].LikeCount, got[2].CommentCount)
	}
}

func TestUpdate(t *testing.T) {
	f := testdb.New(t)
	svc, inv := newService(t)
	ctx := context.Background()
	owner := f.User("Dueño")
	id := f.Insert(testdb.Video{Owner: owner, Status: "FAILED", Description: "vieja"})
	var before time.Time
	f.Scalar(&before, `SELECT updated_at FROM catalog.videos WHERE id = $1`, id)

	d, err := svc.Update(ctx, owner, strings.ToUpper(id), ptr("  Título nuevo  "), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !d.Owner || d.Title != "Título nuevo" || d.Description != "vieja" || d.Status != video.StatusFailed {
		t.Errorf("detail = %+v", d)
	}
	if !d.UpdatedAt.After(before) {
		t.Errorf("updated_at not bumped")
	}
	if len(inv.ids) != 1 || inv.ids[0] != id {
		t.Errorf("invalidations = %v", inv.ids)
	}

	// Description only, including the empty string.
	if d, err = svc.Update(ctx, owner, id, nil, ptr("")); err != nil || d.Description != "" || d.Title != "Título nuevo" {
		t.Errorf("description update: %+v %v", d, err)
	}

	// Validation.
	var ve *video.ValidationError
	if _, err := svc.Update(ctx, owner, id, nil, nil); !errors.As(err, &ve) {
		t.Errorf("no fields: %v", err)
	}
	if _, err := svc.Update(ctx, owner, id, ptr(strings.Repeat("a", 101)), nil); !errors.As(err, &ve) || ve.Field != "title" {
		t.Errorf("long title: %v", err)
	}
}

func TestOwnerRules(t *testing.T) {
	f := testdb.New(t)
	svc, _ := newService(t)
	ctx := context.Background()
	owner, other := f.User("Dueño"), f.User("Otro")

	ready := f.Insert(testdb.Video{Owner: owner})
	failed := f.Insert(testdb.Video{Owner: owner, Status: "FAILED"})
	deleted := f.Insert(testdb.Video{Owner: owner, Status: "DELETED"})
	processing := f.Insert(testdb.Video{Owner: owner, Status: "PROCESSING"})
	pending := f.Insert(testdb.Video{Owner: owner, Status: "PENDING_UPLOAD"})

	tests := []struct {
		name string
		sub  string
		id   string
		want error
	}{
		{"other user, READY", other, ready, video.ErrForbidden},
		{"other user, FAILED", other, failed, video.ErrNotFound},
		{"owner, DELETED", owner, deleted, video.ErrNotFound},
		{"unknown id", owner, testdb.UUID(), video.ErrNotFound},
		{"malformed id", owner, "nope", video.ErrNotFound},
	}
	for _, tt := range tests {
		if _, err := svc.Update(ctx, tt.sub, tt.id, ptr("x"), nil); !errors.Is(err, tt.want) {
			t.Errorf("update %s: %v, want %v", tt.name, err, tt.want)
		}
		if err := svc.Delete(ctx, tt.sub, tt.id); !errors.Is(err, tt.want) {
			t.Errorf("delete %s: %v, want %v", tt.name, err, tt.want)
		}
	}
	// Nothing changed.
	var title string
	f.Scalar(&title, `SELECT title FROM catalog.videos WHERE id = $1`, ready)
	if title == "x" || f.Status(ready) != "READY" {
		t.Errorf("non-owner changed the video")
	}

	// Deleting a video that is still uploading or processing: 409 (decision 3).
	for _, id := range []string{processing, pending} {
		var c *video.ConflictError
		if err := svc.Delete(ctx, owner, id); !errors.As(err, &c) {
			t.Errorf("delete %s: %v, want conflict", f.Status(id), err)
		}
	}
	// FAILED can be deleted, and deleted_at is set.
	if err := svc.Delete(ctx, owner, failed); err != nil {
		t.Fatal(err)
	}
	var deletedAt *time.Time
	f.Scalar(&deletedAt, `SELECT deleted_at FROM catalog.videos WHERE id = $1`, failed)
	if f.Status(failed) != "DELETED" || deletedAt == nil {
		t.Errorf("status %s deleted_at %v", f.Status(failed), deletedAt)
	}
}

// Two deletes at once: one 204, one 404.
func TestConcurrentDeletes(t *testing.T) {
	f := testdb.New(t)
	svc, inv := newService(t)
	owner := f.User("Dueño")
	id := f.Insert(testdb.Video{Owner: owner})

	const n = 2
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- svc.Delete(context.Background(), owner, id)
		}()
	}
	wg.Wait()
	close(errs)
	var ok, notFound int
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, video.ErrNotFound):
			notFound++
		default:
			t.Errorf("unexpected: %v", err)
		}
	}
	if ok != 1 || notFound != 1 {
		t.Errorf("ok=%d notFound=%d", ok, notFound)
	}
	if len(inv.ids) != 1 {
		t.Errorf("invalidations = %v", inv.ids)
	}
}
