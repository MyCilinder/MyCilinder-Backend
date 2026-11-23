//go:build integration

package jobs_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/jobs"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/testdb"
)

// The thresholds below are years, so the tests only touch their own fixtures and never
// the seed data or rows from other tests.

func ago(years int) *time.Time {
	t := time.Now().AddDate(-years, 0, 0)
	return &t
}

func TestExpiryAndSweep(t *testing.T) {
	f := testdb.New(t)
	r := jobs.NewRunner(testdb.Service(t), nil)
	ctx := context.Background()
	const threshold = 5 * 365 * 24 * time.Hour

	stalePending := f.Insert(testdb.Video{Status: "PENDING_UPLOAD", CreatedAt: ago(10)})
	youngPending := f.Insert(testdb.Video{Status: "PENDING_UPLOAD", CreatedAt: ago(1)})
	staleProcessing := f.Insert(testdb.Video{Status: "PROCESSING", CreatedAt: ago(10)})
	youngProcessing := f.Insert(testdb.Video{Status: "PROCESSING", CreatedAt: ago(1)})
	oldReady := f.Insert(testdb.Video{Status: "READY", CreatedAt: ago(10)})

	if n, ran, err := r.RunOnce(ctx, jobs.Expiry(time.Minute, threshold, nil)); err != nil || !ran || n != 1 {
		t.Fatalf("expiry: n=%d ran=%v err=%v", n, ran, err)
	}
	if n, ran, err := r.RunOnce(ctx, jobs.Sweep(time.Minute, threshold, nil)); err != nil || !ran || n != 1 {
		t.Fatalf("sweep: n=%d ran=%v err=%v", n, ran, err)
	}

	want := map[string]string{
		stalePending: "EXPIRED", youngPending: "PENDING_UPLOAD",
		staleProcessing: "FAILED", youngProcessing: "PROCESSING", oldReady: "READY",
	}
	for id, st := range want {
		if got := f.Status(id); got != st {
			t.Errorf("%s: status %s, want %s", id, got, st)
		}
	}
	var reason string
	f.Scalar(&reason, `SELECT failure_reason FROM catalog.videos WHERE id = $1`, staleProcessing)
	if reason != jobs.TimeoutReason {
		t.Errorf("reason = %q", reason)
	}

	// A second run finds nothing more.
	if n, _, err := r.RunOnce(ctx, jobs.Expiry(time.Minute, threshold, nil)); err != nil || n != 0 {
		t.Errorf("second expiry: n=%d err=%v", n, err)
	}
}

func TestBatchLimit(t *testing.T) {
	f := testdb.New(t)
	r := jobs.NewRunner(testdb.Service(t), nil)
	ids := make([]string, jobs.BatchSize+5)
	for i := range ids {
		ids[i] = f.Insert(testdb.Video{Status: "PENDING_UPLOAD", CreatedAt: ago(10)})
	}
	n, _, err := r.RunOnce(context.Background(), jobs.Expiry(time.Minute, 5*365*24*time.Hour, nil))
	if err != nil || n != jobs.BatchSize {
		t.Fatalf("n=%d err=%v, want %d", n, err, jobs.BatchSize)
	}
	expired := 0
	for _, id := range ids {
		if f.Status(id) == "EXPIRED" {
			expired++
		}
	}
	if expired != jobs.BatchSize {
		t.Errorf("expired %d of our rows, want %d", expired, jobs.BatchSize)
	}
}

// Two tasks run the same job at once: only one gets the lock and does the work.
func TestAdvisoryLockOneRunAtATime(t *testing.T) {
	pool := testdb.Service(t)
	a, b := jobs.NewRunner(pool, nil), jobs.NewRunner(pool, nil)

	var mu sync.Mutex
	work := 0
	entered := make(chan struct{})
	release := make(chan struct{})
	job := jobs.Job{Name: "test", LockKey: 0x7E57_0001, Interval: time.Minute,
		Run: func(context.Context, pgx.Tx) (int, error) {
			mu.Lock()
			work++
			mu.Unlock()
			close(entered)
			<-release
			return 1, nil
		}}

	type result struct {
		ran bool
		err error
	}
	first := make(chan result, 1)
	go func() {
		_, ran, err := a.RunOnce(context.Background(), job)
		first <- result{ran, err}
	}()
	<-entered // a holds the lock

	_, ran, err := b.RunOnce(context.Background(), job)
	if err != nil || ran {
		t.Fatalf("second runner: ran=%v err=%v, want skipped", ran, err)
	}
	close(release)
	if r := <-first; r.err != nil || !r.ran {
		t.Fatalf("first runner: %+v", r)
	}
	if work != 1 {
		t.Errorf("work done %d times", work)
	}

	// The lock ends with the transaction, so the next run gets it.
	job.Run = func(context.Context, pgx.Tx) (int, error) { return 0, nil }
	if _, ran, err := b.RunOnce(context.Background(), job); err != nil || !ran {
		t.Errorf("after release: ran=%v err=%v", ran, err)
	}
}

// Run executes jobs on their interval and stops on cancel.
func TestRunLoop(t *testing.T) {
	var mu sync.Mutex
	runs := 0
	job := jobs.Job{Name: "tick", LockKey: 0x7E57_0002, Interval: 50 * time.Millisecond,
		Run: func(context.Context, pgx.Tx) (int, error) {
			mu.Lock()
			runs++
			mu.Unlock()
			return 0, nil
		}}
	r := jobs.NewRunner(testdb.Service(t), nil, job)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.Run(ctx); close(done) }()
	time.Sleep(400 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop")
	}
	mu.Lock()
	defer mu.Unlock()
	if runs < 3 {
		t.Errorf("runs = %d, want several", runs)
	}
}

type fakeDeleter struct {
	mu   sync.Mutex
	ids  []string
	fail map[string]bool
}

func (d *fakeDeleter) DeleteOutputs(_ context.Context, id string) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.ids = append(d.ids, id)
	if d.fail[id] {
		return 0, errors.New("AccessDenied")
	}
	return 3, nil
}

func TestCleanup(t *testing.T) {
	f := testdb.New(t)
	r := jobs.NewRunner(testdb.Service(t), nil)
	ctx := context.Background()
	const retention = 5 * 365 * 24 * time.Hour

	deletedAt := func(id string, years int) {
		f.Exec(`UPDATE catalog.videos SET deleted_at = now() - make_interval(years => $2) WHERE id = $1`, id, years)
	}
	old := f.Insert(testdb.Video{Status: "DELETED"})
	deletedAt(old, 10)
	failing := f.Insert(testdb.Video{Status: "DELETED"})
	deletedAt(failing, 10)
	recent := f.Insert(testdb.Video{Status: "DELETED"})
	deletedAt(recent, 1)
	hidden := f.Insert(testdb.Video{Status: "HIDDEN"}) // kept while HIDDEN (Data Design §2.3)
	deletedAt(hidden, 10)

	d := &fakeDeleter{fail: map[string]bool{failing: true}}
	job := jobs.Cleanup(time.Minute, retention, d, nil)
	n, ran, err := r.RunOnce(ctx, job)
	if err != nil || !ran || n != 1 {
		t.Fatalf("n=%d ran=%v err=%v", n, ran, err)
	}
	purged := func(id string) bool {
		var p *time.Time
		f.Scalar(&p, `SELECT outputs_purged_at FROM catalog.videos WHERE id = $1`, id)
		return p != nil
	}
	if !purged(old) || purged(failing) || purged(recent) || purged(hidden) {
		t.Errorf("purged: old=%v failing=%v recent=%v hidden=%v", purged(old), purged(failing), purged(recent), purged(hidden))
	}
	for _, id := range d.ids {
		if id == recent || id == hidden {
			t.Errorf("cleanup touched %s", id)
		}
	}

	// Next run: the purged video is not listed again; the failed one is retried.
	d.ids = nil
	delete(d.fail, failing)
	if n, _, err := r.RunOnce(ctx, job); err != nil || n != 1 {
		t.Fatalf("second run: n=%d err=%v", n, err)
	}
	if len(d.ids) != 1 || d.ids[0] != failing || !purged(failing) {
		t.Errorf("second run deleted %v", d.ids)
	}
}
