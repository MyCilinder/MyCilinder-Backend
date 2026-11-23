//go:build integration

package moderation_test

import (
	"context"
	"errors"
	"testing"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/lifecycle"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/moderation"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/store"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/testdb"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

func cleanupAudit(t *testing.T, f *testdb.Fixtures, id string) {
	t.Cleanup(func() { f.Exec(`DELETE FROM audit.audit_log WHERE target_id = $1`, id) })
}

func TestHideAndRestoreWriteAudit(t *testing.T) {
	f := testdb.New(t)
	svc := moderation.NewService(lifecycle.NewMachine(testdb.Service(t), nil), store.NewAudit("catalog"))
	ctx := context.Background()
	operator := testdb.UUID()
	id := f.Insert(testdb.Video{})
	cleanupAudit(t, f, id)

	if err := svc.Hide(ctx, operator, id); err != nil {
		t.Fatal(err)
	}
	if s := f.Status(id); s != "HIDDEN" {
		t.Fatalf("status = %s", s)
	}
	var action, actor, service, details string
	err := f.Admin.QueryRow(ctx,
		`SELECT action, actor_id::text, service, details::text FROM audit.audit_log
		 WHERE target_id = $1 AND target_type = 'video' ORDER BY id DESC LIMIT 1`, id).
		Scan(&action, &actor, &service, &details)
	if err != nil {
		t.Fatal(err)
	}
	if action != "video.hidden" || actor != operator || service != "catalog" || details != `{"to": "HIDDEN", "from": "READY"}` {
		t.Errorf("audit row: %s %s %s %s", action, actor, service, details)
	}

	// Repeating the hide is a conflict and writes nothing.
	var c *video.ConflictError
	if err := svc.Hide(ctx, operator, id); !errors.As(err, &c) || c.Current != video.StatusHidden {
		t.Errorf("second hide: %v", err)
	}

	if err := svc.Restore(ctx, operator, id); err != nil {
		t.Fatal(err)
	}
	if s := f.Status(id); s != "READY" {
		t.Fatalf("status = %s", s)
	}
	var rows int
	f.Scalar(&rows, `SELECT count(*) FROM audit.audit_log WHERE target_id = $1`, id)
	var last string
	f.Scalar(&last, `SELECT action FROM audit.audit_log WHERE target_id = $1 ORDER BY id DESC LIMIT 1`, id)
	if rows != 2 || last != "video.restored" {
		t.Errorf("audit rows = %d, last = %s", rows, last)
	}
}

func TestWrongStatuses(t *testing.T) {
	f := testdb.New(t)
	svc := moderation.NewService(lifecycle.NewMachine(testdb.Service(t), nil), store.NewAudit("catalog"))
	ctx := context.Background()

	for _, st := range []string{"PENDING_UPLOAD", "PROCESSING", "FAILED", "HIDDEN", "DELETED", "EXPIRED"} {
		id := f.Insert(testdb.Video{Status: st})
		var c *video.ConflictError
		if err := svc.Hide(ctx, testdb.UUID(), id); !errors.As(err, &c) {
			t.Errorf("hide %s: %v, want conflict", st, err)
		}
	}
	for _, st := range []string{"PENDING_UPLOAD", "PROCESSING", "READY", "FAILED", "DELETED", "EXPIRED"} {
		id := f.Insert(testdb.Video{Status: st})
		var c *video.ConflictError
		if err := svc.Restore(ctx, testdb.UUID(), id); !errors.As(err, &c) {
			t.Errorf("restore %s: %v, want conflict", st, err)
		}
	}
	if err := svc.Hide(ctx, testdb.UUID(), testdb.UUID()); !errors.Is(err, video.ErrNotFound) {
		t.Errorf("unknown id: %v", err)
	}
	if err := svc.Hide(ctx, testdb.UUID(), "not-a-uuid"); !errors.Is(err, video.ErrNotFound) {
		t.Errorf("malformed id: %v", err)
	}
}

type failingAudit struct{}

func (failingAudit) Write(context.Context, store.DBTX, store.AuditEntry) error {
	return errors.New("audit insert failed")
}

// If the audit insert fails, the status change rolls back (Data Design §2.1).
func TestAuditFailureRollsBack(t *testing.T) {
	f := testdb.New(t)
	svc := moderation.NewService(lifecycle.NewMachine(testdb.Service(t), nil), failingAudit{})
	id := f.Insert(testdb.Video{})

	if err := svc.Hide(context.Background(), testdb.UUID(), id); !errors.Is(err, video.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}
	if s := f.Status(id); s != "READY" {
		t.Errorf("status = %s, want READY (rolled back)", s)
	}
}

// A real database rejection of the audit row (here: a malformed actor id) also rolls back.
func TestAuditConstraintRollsBack(t *testing.T) {
	f := testdb.New(t)
	svc := moderation.NewService(lifecycle.NewMachine(testdb.Service(t), nil), store.NewAudit("catalog"))
	id := f.Insert(testdb.Video{})
	cleanupAudit(t, f, id)

	if err := svc.Hide(context.Background(), "not-a-uuid-actor", id); err == nil {
		t.Fatal("expected the audit insert to fail")
	}
	if s := f.Status(id); s != "READY" {
		t.Errorf("status = %s, want READY", s)
	}
}
