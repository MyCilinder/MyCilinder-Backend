// Package store implements the auth facade repositories on PostgreSQL (pgx).
//
// The service packages (internal/auth, internal/profile) define the interfaces they need;
// the types here satisfy them. Queries are always parameterized (SEC-07).
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/user"
)

// Users reads and writes auth.users. The auth_svc role has SELECT, INSERT, and UPDATE on
// the table, and no DELETE (profile deletion is out of scope, FR-AUTH-09).
type Users struct {
	pool *pgxpool.Pool
}

// NewUsers returns a Users repository.
func NewUsers(pool *pgxpool.Pool) *Users { return &Users{pool: pool} }

// EnsureUser creates the profile on first login (FR-AUTH-05). It is idempotent: if the
// profile exists it is left untouched, so a later display-name change is not overwritten
// by the name stored in Cognito. created reports whether a row was inserted.
func (u *Users) EnsureUser(ctx context.Context, id, displayName string) (created bool, err error) {
	tag, err := u.pool.Exec(ctx,
		`INSERT INTO auth.users (id, display_name) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`,
		id, displayName)
	if err != nil {
		return false, fmt.Errorf("ensure user: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// Get returns the profile, or user.ErrNotFound.
func (u *Users) Get(ctx context.Context, id string) (user.User, error) {
	row := u.pool.QueryRow(ctx,
		`SELECT id, display_name, created_at, updated_at FROM auth.users WHERE id = $1`, id)
	return scanUser(row)
}

// UpdateDisplayName changes the display name and returns the updated profile, or
// user.ErrNotFound. The caller validates the name (user.NormalizeDisplayName); the CHECK
// constraint is the last line of defense.
func (u *Users) UpdateDisplayName(ctx context.Context, id, displayName string) (user.User, error) {
	row := u.pool.QueryRow(ctx,
		`UPDATE auth.users SET display_name = $2, updated_at = now()
		 WHERE id = $1
		 RETURNING id, display_name, created_at, updated_at`, id, displayName)
	return scanUser(row)
}

func scanUser(row pgx.Row) (user.User, error) {
	var out user.User
	err := row.Scan(&out.ID, &out.DisplayName, &out.CreatedAt, &out.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return user.User{}, user.ErrNotFound
	}
	if err != nil {
		return user.User{}, fmt.Errorf("scan user: %w", err)
	}
	return out, nil
}
