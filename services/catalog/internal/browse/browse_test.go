package browse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/cursor"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

type fakeRepo struct {
	rows      []video.Video
	ranked    []video.Ranked
	exists    bool
	err       error
	gotLimit  int
	gotAfter  *cursor.Cursor
	gotText   string
	gotCreate string
}

func (f *fakeRepo) ListRecent(_ context.Context, after *cursor.Cursor, limit int) ([]video.Video, error) {
	f.gotAfter, f.gotLimit = after, limit
	return f.take(limit), f.err
}

func (f *fakeRepo) ListMostViewed(_ context.Context, after *cursor.Cursor, limit int) ([]video.Video, error) {
	f.gotAfter, f.gotLimit = after, limit
	return f.take(limit), f.err
}

func (f *fakeRepo) ListByCreator(_ context.Context, owner string, after *cursor.Cursor, limit int) ([]video.Video, error) {
	f.gotCreate, f.gotAfter, f.gotLimit = owner, after, limit
	return f.take(limit), f.err
}

func (f *fakeRepo) CreatorExists(context.Context, string) (bool, error) { return f.exists, f.err }

func (f *fakeRepo) Search(_ context.Context, text string, after *cursor.Cursor, limit int) ([]video.Ranked, error) {
	f.gotText, f.gotAfter, f.gotLimit = text, after, limit
	if len(f.ranked) > limit {
		return f.ranked[:limit], f.err
	}
	return f.ranked, f.err
}

func (f *fakeRepo) take(n int) []video.Video {
	if len(f.rows) > n {
		return f.rows[:n]
	}
	return f.rows
}

func vids(n int) []video.Video {
	out := make([]video.Video, n)
	for i := range out {
		pub := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC).Add(-time.Duration(i) * time.Hour)
		out[i] = video.Video{ID: fmt.Sprintf("00000000-0000-4000-8000-%012d", i), PublishedAt: &pub, ViewCount: int64(100 - i)}
	}
	return out
}

func TestFeedPaging(t *testing.T) {
	repo := &fakeRepo{rows: vids(3)}
	svc := NewService(repo, nil)
	ctx := context.Background()

	p, err := svc.Feed(ctx, "", "", 2)
	if err != nil || len(p.Items) != 2 || p.NextCursor == "" || repo.gotLimit != 3 {
		t.Fatalf("first page: %d items next=%q limit=%d err=%v", len(p.Items), p.NextCursor, repo.gotLimit, err)
	}
	c, err := cursor.Decode(p.NextCursor, cursor.Recent, "")
	if err != nil || c.ID != repo.rows[1].ID || !c.Time.Equal(*repo.rows[1].PublishedAt) {
		t.Fatalf("cursor %+v err %v, want last item of page", c, err)
	}

	// The cursor is passed to the repository.
	if _, err := svc.Feed(ctx, SortRecent, p.NextCursor, 2); err != nil || repo.gotAfter == nil || repo.gotAfter.ID != c.ID {
		t.Fatalf("second page: after=%+v err=%v", repo.gotAfter, err)
	}

	// Exactly limit rows: no next page.
	repo.rows = vids(2)
	if p, _ := svc.Feed(ctx, "", "", 2); p.NextCursor != "" {
		t.Errorf("next cursor on last page: %q", p.NextCursor)
	}

	// most_viewed cursors carry the view count, and recent cursors are rejected.
	repo.rows = vids(3)
	p, _ = svc.Feed(ctx, SortMostViewed, "", 1)
	if c, err := cursor.Decode(p.NextCursor, cursor.MostViewed, ""); err != nil || c.Int != 100 {
		t.Errorf("most_viewed cursor %+v %v", c, err)
	}
	recent, _ := svc.Feed(ctx, SortRecent, "", 1)
	_, err = svc.Feed(ctx, SortMostViewed, recent.NextCursor, 1)
	assertRule(t, err, "cursor", "format")
}

func TestValidation(t *testing.T) {
	svc := NewService(&fakeRepo{exists: true}, nil)
	ctx := context.Background()
	_, err := svc.Feed(ctx, "popular", "", 20)
	assertRule(t, err, "sort", "enum")
	_, err = svc.Feed(ctx, "", "garbage", 20)
	assertRule(t, err, "cursor", "format")
	for _, n := range []int{0, -1, 51} {
		_, err = svc.Feed(ctx, "", "", n)
		assertRule(t, err, "limit", "range")
	}
	_, err = svc.Search(ctx, "   ", "", 20)
	assertRule(t, err, "q", "required")
	_, err = svc.Search(ctx, strings.Repeat("ñ", 201), "", 20)
	assertRule(t, err, "q", "length")
	if _, err = svc.Search(ctx, strings.Repeat("ñ", 200), "", 20); err != nil {
		t.Errorf("200 characters rejected: %v", err)
	}
}

func TestCreator(t *testing.T) {
	repo := &fakeRepo{rows: vids(2)}
	svc := NewService(repo, nil)
	ctx := context.Background()
	const user = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"

	if _, err := svc.Creator(ctx, "nope", "", 20); !errors.Is(err, video.ErrNotFound) {
		t.Errorf("malformed id: %v", err)
	}
	if _, err := svc.Creator(ctx, user, "", 20); !errors.Is(err, video.ErrNotFound) {
		t.Errorf("unknown creator: %v", err)
	}
	repo.exists = true
	p, err := svc.Creator(ctx, user, "", 1)
	if err != nil || repo.gotCreate != strings.ToLower(user) || p.NextCursor == "" {
		t.Fatalf("p=%+v owner=%q err=%v", p, repo.gotCreate, err)
	}
	// A cursor from one creator is rejected for another.
	_, err = svc.Creator(ctx, "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", p.NextCursor, 1)
	assertRule(t, err, "cursor", "format")

	repo.err = errors.New("down")
	if _, err := svc.Creator(ctx, user, "", 1); !errors.Is(err, video.ErrUnavailable) {
		t.Errorf("db error: %v", err)
	}
}

func TestSearch(t *testing.T) {
	repo := &fakeRepo{}
	for i, v := range vids(3) {
		repo.ranked = append(repo.ranked, video.Ranked{Video: v, Rank: 0.5 - float32(i)/10})
	}
	svc := NewService(repo, nil)
	ctx := context.Background()

	p, err := svc.Search(ctx, "  Recorridos   CAMPUS ", "", 2)
	if err != nil || repo.gotText != "recorridos campus" || len(p.Items) != 2 {
		t.Fatalf("text=%q items=%d err=%v", repo.gotText, len(p.Items), err)
	}
	c, err := cursor.Decode(p.NextCursor, cursor.Search, cursor.SearchScope("recorridos campus"))
	if err != nil || c.Float != repo.ranked[1].Rank || c.ID != repo.ranked[1].Video.ID {
		t.Fatalf("cursor %+v err %v", c, err)
	}
	// The same query with different spacing or case shares the cursor.
	if _, err := svc.Search(ctx, "recorridos campus", p.NextCursor, 2); err != nil {
		t.Errorf("normalized query rejected the cursor: %v", err)
	}
	// Another query does not.
	_, err = svc.Search(ctx, "tutorial", p.NextCursor, 2)
	assertRule(t, err, "cursor", "format")

	// No results is an empty page, not an error.
	repo.ranked = nil
	p, err = svc.Search(ctx, "nada", "", 20)
	if err != nil || len(p.Items) != 0 || p.Items == nil || p.NextCursor != "" {
		t.Errorf("empty search: %+v %v", p, err)
	}
}

// recordingCache records keys and passes through to load.
type recordingCache struct{ keys []string }

func (c *recordingCache) Page(ctx context.Context, key string, _ time.Duration, load func(context.Context) (Page, error)) (Page, error) {
	c.keys = append(c.keys, key)
	return load(ctx)
}

func TestCacheKeys(t *testing.T) {
	rc := &recordingCache{}
	svc := NewService(&fakeRepo{rows: vids(1), exists: true}, rc)
	ctx := context.Background()
	svc.Feed(ctx, "", "", 20)
	svc.Feed(ctx, SortMostViewed, "", 5)
	svc.Search(ctx, "Campus", "", 20)
	svc.Creator(ctx, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "", 20) // not cached

	want := []string{
		"feed:recent:-:20",
		"feed:most_viewed:-:5",
		"search:" + cursor.SearchScope("campus") + ":-:20",
	}
	if strings.Join(rc.keys, ",") != strings.Join(want, ",") {
		t.Errorf("keys = %v, want %v", rc.keys, want)
	}
}

func assertRule(t *testing.T, err error, field, rule string) {
	t.Helper()
	var ve *video.ValidationError
	if !errors.As(err, &ve) || ve.Field != field || ve.Rule != rule {
		t.Errorf("err = %v, want %s/%s", err, field, rule)
	}
}
