package cache

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/browse"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

// memStore is an in-memory Store. down makes every call fail like an unreachable Redis.
type memStore struct {
	mu   sync.Mutex
	m    map[string][]byte
	ttl  map[string]time.Duration
	down bool
}

func newMem() *memStore { return &memStore{m: map[string][]byte{}, ttl: map[string]time.Duration{}} }

var errDown = errors.New("dial tcp: connection refused")

func (s *memStore) Get(_ context.Context, k string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down {
		return nil, errDown
	}
	b, ok := s.m[k]
	if !ok {
		return nil, ErrMiss
	}
	return b, nil
}

func (s *memStore) Set(_ context.Context, k string, v []byte, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down {
		return errDown
	}
	s.m[k], s.ttl[k] = v, ttl
	return nil
}

func (s *memStore) Del(_ context.Context, keys ...string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.down {
		return errDown
	}
	for _, k := range keys {
		delete(s.m, k)
	}
	return nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestPagesHitAndMiss(t *testing.T) {
	mem := newMem()
	pages := NewPages(New(mem, quiet()))
	var loads int
	load := func(context.Context) (browse.Page, error) {
		loads++
		return browse.Page{Items: []video.Video{{ID: "a", Title: "Recorrido"}}, NextCursor: "n"}, nil
	}
	for i := 0; i < 3; i++ {
		p, err := pages.Page(context.Background(), "feed:recent:-:20", 30*time.Second, load)
		if err != nil || len(p.Items) != 1 || p.Items[0].Title != "Recorrido" || p.NextCursor != "n" {
			t.Fatalf("call %d: %+v %v", i, p, err)
		}
	}
	if loads != 1 {
		t.Errorf("loads = %d, want 1", loads)
	}
	if mem.ttl["feed:recent:-:20"] != 30*time.Second {
		t.Errorf("ttl = %v", mem.ttl["feed:recent:-:20"])
	}
}

func TestSingleflightCollapsesMisses(t *testing.T) {
	pages := NewPages(New(newMem(), quiet()))
	var loads atomic.Int32
	release := make(chan struct{})
	load := func(context.Context) (browse.Page, error) {
		loads.Add(1)
		<-release
		return browse.Page{NextCursor: "x"}, nil
	}

	const n = 50
	var started, done sync.WaitGroup
	started.Add(n)
	done.Add(n)
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		go func() {
			defer done.Done()
			started.Done()
			p, err := pages.Page(context.Background(), "feed:most_viewed:-:20", time.Minute, load)
			if err == nil && p.NextCursor != "x" {
				err = errors.New("wrong page")
			}
			errs <- err
		}()
	}
	started.Wait()
	time.Sleep(50 * time.Millisecond) // let every goroutine reach the singleflight call
	close(release)
	done.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if got := loads.Load(); got != 1 {
		t.Errorf("repository called %d times, want 1", got)
	}
}

func TestRedisDownFallsBackToLoad(t *testing.T) {
	mem := newMem()
	mem.down = true
	pages := NewPages(New(mem, quiet()))
	var loads int
	for i := 0; i < 2; i++ {
		p, err := pages.Page(context.Background(), "k", time.Minute, func(context.Context) (browse.Page, error) {
			loads++
			return browse.Page{NextCursor: "ok"}, nil
		})
		if err != nil || p.NextCursor != "ok" {
			t.Fatalf("call %d: %+v %v", i, p, err)
		}
	}
	if loads != 2 {
		t.Errorf("loads = %d, want 2 (nothing is cached while Redis is down)", loads)
	}
	// Invalidate must not fail or panic either.
	NewVideos(New(mem, quiet()), nil).Invalidate(context.Background(), "x")
}

func TestLoadErrorNotCached(t *testing.T) {
	mem := newMem()
	pages := NewPages(New(mem, quiet()))
	_, err := pages.Page(context.Background(), "k", time.Minute, func(context.Context) (browse.Page, error) {
		return browse.Page{}, video.ErrUnavailable
	})
	if !errors.Is(err, video.ErrUnavailable) {
		t.Fatalf("err = %v", err)
	}
	if _, ok := mem.m["k"]; ok {
		t.Error("failed load was cached")
	}
}

func TestCorruptEntryReloads(t *testing.T) {
	mem := newMem()
	mem.m["k"] = []byte("{not json")
	pages := NewPages(New(mem, quiet()))
	p, err := pages.Page(context.Background(), "k", time.Minute, func(context.Context) (browse.Page, error) {
		return browse.Page{NextCursor: "fresh"}, nil
	})
	if err != nil || p.NextCursor != "fresh" {
		t.Fatalf("%+v %v", p, err)
	}
}

type countingRepo struct {
	v     video.Video
	err   error
	calls int
}

func (r *countingRepo) GetByID(context.Context, string) (video.Video, error) {
	r.calls++
	return r.v, r.err
}

func TestVideosCacheOnlyReady(t *testing.T) {
	mem := newMem()
	repo := &countingRepo{v: video.Video{ID: "v1", Status: video.StatusProcessing}}
	cv := NewVideos(New(mem, quiet()), repo)
	ctx := context.Background()

	// Not READY: never cached, so the owner always sees the current status.
	cv.GetByID(ctx, "v1")
	cv.GetByID(ctx, "v1")
	if repo.calls != 2 {
		t.Errorf("PROCESSING: repo calls = %d, want 2", repo.calls)
	}

	repo.v.Status = video.StatusReady
	cv.GetByID(ctx, "v1")
	cv.GetByID(ctx, "v1")
	if repo.calls != 3 {
		t.Errorf("READY: repo calls = %d, want 3 (second read cached)", repo.calls)
	}
	var cached video.Video
	if err := json.Unmarshal(mem.m["video:v1"], &cached); err != nil || cached.Status != video.StatusReady {
		t.Errorf("cached entry = %s", mem.m["video:v1"])
	}
	if mem.ttl["video:v1"] != VideoTTL {
		t.Errorf("ttl = %v", mem.ttl["video:v1"])
	}

	// After a write, Invalidate drops the entry and the next read sees the new status.
	repo.v.Status = video.StatusHidden
	cv.Invalidate(ctx, "v1")
	got, _ := cv.GetByID(ctx, "v1")
	if got.Status != video.StatusHidden || repo.calls != 4 {
		t.Errorf("after invalidate: status %s calls %d", got.Status, repo.calls)
	}

	// Not found is passed through and not cached.
	repo.err = video.ErrNotFound
	if _, err := cv.GetByID(ctx, "v2"); !errors.Is(err, video.ErrNotFound) {
		t.Errorf("err = %v", err)
	}
	if _, ok := mem.m["video:v2"]; ok {
		t.Error("not-found result cached")
	}
}
