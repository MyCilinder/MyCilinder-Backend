package profile

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/cognito"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/user"
)

type fakeCognito struct {
	info cognito.UserInfo
	err  error
}

func (f fakeCognito) GetUser(context.Context, string) (cognito.UserInfo, error) { return f.info, f.err }

type memUsers struct {
	rows map[string]user.User
	err  error
}

func (m *memUsers) EnsureUser(_ context.Context, id, name string) (bool, error) {
	if m.err != nil {
		return false, m.err
	}
	if _, ok := m.rows[id]; ok {
		return false, nil
	}
	m.rows[id] = user.User{ID: id, DisplayName: name, CreatedAt: time.Unix(100, 0)}
	return true, nil
}
func (m *memUsers) Get(_ context.Context, id string) (user.User, error) {
	if m.err != nil {
		return user.User{}, m.err
	}
	u, ok := m.rows[id]
	if !ok {
		return user.User{}, user.ErrNotFound
	}
	return u, nil
}
func (m *memUsers) UpdateDisplayName(_ context.Context, id, name string) (user.User, error) {
	if m.err != nil {
		return user.User{}, m.err
	}
	u, ok := m.rows[id]
	if !ok {
		return user.User{}, user.ErrNotFound
	}
	u.DisplayName = name
	m.rows[id] = u
	return u, nil
}

const sub = "6f1c2a3b-0000-4000-8000-000000000001"

var info = cognito.UserInfo{Sub: sub, Email: "diego@example.com", DisplayName: "Diego"}

func TestGet(t *testing.T) {
	users := &memUsers{rows: map[string]user.User{sub: {ID: sub, DisplayName: "Local Name", CreatedAt: time.Unix(5, 0)}}}
	p, err := New(fakeCognito{info: info}, users).Get(context.Background(), sub, "at")
	if err != nil {
		t.Fatal(err)
	}
	// The local display name wins over the Cognito attribute.
	if p != (Profile{ID: sub, Email: "diego@example.com", DisplayName: "Local Name", CreatedAt: time.Unix(5, 0)}) {
		t.Errorf("profile = %+v", p)
	}
}

func TestGetCreatesMissingProfile(t *testing.T) {
	users := &memUsers{rows: map[string]user.User{}}
	p, err := New(fakeCognito{info: info}, users).Get(context.Background(), sub, "at")
	if err != nil || p.DisplayName != "Diego" || users.rows[sub].DisplayName != "Diego" {
		t.Fatalf("p=%+v err=%v rows=%v", p, err, users.rows)
	}
}

func TestErrors(t *testing.T) {
	ok := &memUsers{rows: map[string]user.User{sub: {ID: sub, DisplayName: "A"}}}
	tests := []struct {
		name  string
		cog   fakeCognito
		users *memUsers
		want  error
	}{
		{"revoked token", fakeCognito{err: errors.Join(cognito.ErrNotAuthorized)}, ok, ErrUnauthenticated},
		{"sub mismatch", fakeCognito{info: cognito.UserInfo{Sub: "someone-else"}}, ok, ErrUnauthenticated},
		{"cognito down", fakeCognito{err: cognito.ErrUnavailable}, ok, ErrUnavailable},
		{"db down", fakeCognito{info: info}, &memUsers{err: errors.New("db")}, ErrUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.cog, tt.users).Get(context.Background(), sub, "at")
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}

func TestUpdateDisplayName(t *testing.T) {
	users := &memUsers{rows: map[string]user.User{sub: {ID: sub, DisplayName: "Old"}}}
	svc := New(fakeCognito{info: info}, users)

	p, err := svc.UpdateDisplayName(context.Background(), sub, "at", "  New Name  ")
	if err != nil || p.DisplayName != "New Name" || p.Email != "diego@example.com" {
		t.Fatalf("p=%+v err=%v", p, err)
	}

	var ve *user.ValidationError
	if _, err := svc.UpdateDisplayName(context.Background(), sub, "at", ""); !errors.As(err, &ve) || ve.Rule != user.RuleRequired {
		t.Errorf("empty name: %v", err)
	}
	if users.rows[sub].DisplayName != "New Name" {
		t.Error("invalid update changed the row")
	}
}
