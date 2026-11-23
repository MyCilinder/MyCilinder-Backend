//go:build integration

package lifecycle_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/lifecycle"
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

func TestEventSequence(t *testing.T) {
	f := testdb.New(t)
	inv := &recorder{}
	m := lifecycle.NewMachine(testdb.Service(t), inv)
	ctx := context.Background()
	id := f.Insert(testdb.Video{Status: "PENDING_UPLOAD"})

	if err := m.Transition(ctx, id, lifecycle.Processing, lifecycle.Changes{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.Transition(ctx, id, lifecycle.Processing, lifecycle.Changes{}, nil); !errors.Is(err, lifecycle.ErrNoop) {
		t.Fatalf("duplicate processing: %v, want ErrNoop", err)
	}

	dur := 31
	ready := lifecycle.Changes{DurationSeconds: &dur, Renditions: []string{"360p", "720p"}}
	if err := m.Transition(ctx, id, lifecycle.Ready, ready, nil); err != nil {
		t.Fatal(err)
	}
	var published time.Time
	var gotDur int
	var renditions []string
	f.Scalar(&published, `SELECT published_at FROM catalog.videos WHERE id = $1`, id)
	f.Scalar(&gotDur, `SELECT duration_seconds FROM catalog.videos WHERE id = $1`, id)
	f.Scalar(&renditions, `SELECT renditions FROM catalog.videos WHERE id = $1`, id)
	if gotDur != 31 || len(renditions) != 2 || published.IsZero() {
		t.Fatalf("ready columns: dur=%d renditions=%v published=%v", gotDur, renditions, published)
	}

	// Duplicate ready: no-op, published_at untouched.
	if err := m.Transition(ctx, id, lifecycle.Ready, ready, nil); !errors.Is(err, lifecycle.ErrNoop) {
		t.Fatalf("duplicate ready: %v", err)
	}
	// Late or backwards events are conflicts and change nothing.
	if err := m.Transition(ctx, id, lifecycle.Fail, lifecycle.Changes{FailureReason: "x"}, nil); !isConflict(err, video.StatusReady) {
		t.Fatalf("fail after ready: %v", err)
	}
	if err := m.Transition(ctx, id, lifecycle.Processing, lifecycle.Changes{}, nil); !isConflict(err, video.StatusReady) {
		t.Fatalf("processing after ready: %v", err)
	}

	// hide then restore keeps the first published_at.
	time.Sleep(10 * time.Millisecond)
	if err := m.Transition(ctx, id, lifecycle.Hide, lifecycle.Changes{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := m.Transition(ctx, id, lifecycle.Restore, lifecycle.Changes{}, nil); err != nil {
		t.Fatal(err)
	}
	var after time.Time
	f.Scalar(&after, `SELECT published_at FROM catalog.videos WHERE id = $1`, id)
	if !after.Equal(published) {
		t.Errorf("published_at changed from %v to %v", published, after)
	}

	// Invalidate ran once per successful change (processing, ready, hide, restore).
	if len(inv.ids) != 4 {
		t.Errorf("invalidations = %v, want 4", inv.ids)
	}
}

func TestOutOfOrderReadyAndFailure(t *testing.T) {
	f := testdb.New(t)
	m := lifecycle.NewMachine(testdb.Service(t), nil)
	ctx := context.Background()

	// ready before processing is accepted (Catalog plan, decision 1).
	early := f.Insert(testdb.Video{Status: "PENDING_UPLOAD"})
	dur := 5
	if err := m.Transition(ctx, early, lifecycle.Ready, lifecycle.Changes{DurationSeconds: &dur, Renditions: []string{"360p"}}, nil); err != nil {
		t.Fatal(err)
	}
	// The late processing event is then ignored.
	if err := m.Transition(ctx, early, lifecycle.Processing, lifecycle.Changes{}, nil); !isConflict(err, video.StatusReady) {
		t.Fatalf("late processing: %v", err)
	}

	failed := f.Insert(testdb.Video{Status: "PROCESSING"})
	if err := m.Transition(ctx, failed, lifecycle.Fail, lifecycle.Changes{FailureReason: "unsupported codec"}, nil); err != nil {
		t.Fatal(err)
	}
	var reason string
	f.Scalar(&reason, `SELECT failure_reason FROM catalog.videos WHERE id = $1`, failed)
	if reason != "unsupported codec" || f.Status(failed) != "FAILED" {
		t.Errorf("status %s reason %q", f.Status(failed), reason)
	}

	// Final statuses accept nothing.
	expired := f.Insert(testdb.Video{Status: "EXPIRED"})
	if err := m.Transition(ctx, expired, lifecycle.Ready, lifecycle.Changes{DurationSeconds: &dur}, nil); !isConflict(err, video.StatusExpired) {
		t.Errorf("ready on EXPIRED: %v", err)
	}
	if err := m.Transition(ctx, testdb.UUID(), lifecycle.Hide, lifecycle.Changes{}, nil); !errors.Is(err, video.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
}

// A failure inside the transaction rolls the status change back and skips invalidation.
func TestTransitionRollsBack(t *testing.T) {
	f := testdb.New(t)
	inv := &recorder{}
	m := lifecycle.NewMachine(testdb.Service(t), inv)
	id := f.Insert(testdb.Video{})

	boom := errors.New("audit insert failed")
	err := m.Transition(context.Background(), id, lifecycle.Hide, lifecycle.Changes{},
		func(context.Context, pgx.Tx) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	if s := f.Status(id); s != "READY" {
		t.Errorf("status = %s after rollback, want READY", s)
	}
	if len(inv.ids) != 0 {
		t.Errorf("invalidated after a failed change: %v", inv.ids)
	}
}

// Concurrent duplicates: exactly one applies, the rest are no-ops.
func TestConcurrentDuplicates(t *testing.T) {
	f := testdb.New(t)
	m := lifecycle.NewMachine(testdb.Service(t), nil)
	id := f.Insert(testdb.Video{Status: "PENDING_UPLOAD"})

	const n = 8
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- m.Transition(context.Background(), id, lifecycle.Processing, lifecycle.Changes{}, nil)
		}()
	}
	wg.Wait()
	close(errs)
	var ok, noop int
	for err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, lifecycle.ErrNoop):
			noop++
		default:
			t.Errorf("unexpected: %v", err)
		}
	}
	if ok != 1 || noop != n-1 {
		t.Errorf("ok=%d noop=%d, want 1 and %d", ok, noop, n-1)
	}
}

func isConflict(err error, current video.Status) bool {
	var c *video.ConflictError
	return errors.As(err, &c) && c.Current == current
}
