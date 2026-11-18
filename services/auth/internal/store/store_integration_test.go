//go:build integration

package store

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/db"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/user"
)

// Run with the local stack up and migrations applied:
//
//	TEST_DATABASE_URL=postgres://auth_svc:auth_svc_local@127.0.0.1:5432/mycilinder?sslmode=disable \
//	  go test -tags integration ./services/auth/internal/store/...
//
// The tests connect as auth_svc, so they also exercise the grants. Rows are not deleted
// (auth_svc has no DELETE); every test uses fresh random ids.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	pool, err := db.NewPool(context.Background(), db.Options{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func TestEnsureUserIsIdempotent(t *testing.T) {
	ctx := context.Background()
	users := NewUsers(testPool(t))
	id := randomUUID()

	created, err := users.EnsureUser(ctx, id, "First Name")
	if err != nil || !created {
		t.Fatalf("first EnsureUser: created=%v err=%v", created, err)
	}
	created, err = users.EnsureUser(ctx, id, "Other Name")
	if err != nil || created {
		t.Fatalf("second EnsureUser: created=%v err=%v", created, err)
	}

	got, err := users.Get(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.DisplayName != "First Name" {
		t.Errorf("profile overwritten or wrong: %+v", got)
	}
}

func TestGetUnknownUser(t *testing.T) {
	_, err := NewUsers(testPool(t)).Get(context.Background(), randomUUID())
	if !errors.Is(err, user.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestUpdateDisplayName(t *testing.T) {
	ctx := context.Background()
	users := NewUsers(testPool(t))
	id := randomUUID()
	if _, err := users.EnsureUser(ctx, id, "Before"); err != nil {
		t.Fatal(err)
	}
	before, _ := users.Get(ctx, id)
	time.Sleep(5 * time.Millisecond)

	after, err := users.UpdateDisplayName(ctx, id, "After")
	if err != nil {
		t.Fatal(err)
	}
	if after.DisplayName != "After" {
		t.Errorf("display name = %q", after.DisplayName)
	}
	if !after.UpdatedAt.After(before.UpdatedAt) {
		t.Errorf("updated_at did not change: before %v after %v", before.UpdatedAt, after.UpdatedAt)
	}
	if !after.CreatedAt.Equal(before.CreatedAt) {
		t.Errorf("created_at changed")
	}

	if _, err := users.UpdateDisplayName(ctx, randomUUID(), "Nobody"); !errors.Is(err, user.ErrNotFound) {
		t.Errorf("update unknown: err = %v, want ErrNotFound", err)
	}
}

func TestUpdateDisplayNameCheckConstraint(t *testing.T) {
	ctx := context.Background()
	users := NewUsers(testPool(t))
	id := randomUUID()
	if _, err := users.EnsureUser(ctx, id, "Valid"); err != nil {
		t.Fatal(err)
	}
	if _, err := users.UpdateDisplayName(ctx, id, strings.Repeat("a", 51)); err == nil {
		t.Error("expected CHECK violation for 51 characters")
	}
}

func TestAuditWrite(t *testing.T) {
	ctx := context.Background()
	audit := NewAudit(testPool(t), "auth")

	// Authenticated event with details.
	if err := audit.Write(ctx, AuditEntry{
		Action:     ActionLoginSucceeded,
		ActorID:    randomUUID(),
		TargetType: "user",
		TargetID:   randomUUID(),
		Details:    map[string]any{"firstLogin": true},
	}); err != nil {
		t.Fatal(err)
	}
	// Unauthenticated event: no actor, no details.
	if err := audit.Write(ctx, AuditEntry{Action: ActionLoginFailed}); err != nil {
		t.Fatal(err)
	}
}
