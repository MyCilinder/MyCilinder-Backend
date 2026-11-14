package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Audit actions written by the auth facade (Data Design §1.3).
const (
	ActionRegister       = "auth.register"
	ActionLoginSucceeded = "auth.login_succeeded"
	ActionLoginFailed    = "auth.login_failed"
	ActionLogout         = "auth.logout"
)

// AuditEntry is one audit_log row. ActorID is empty for unauthenticated events such as
// a failed login (the row then stores NULL). Details must never contain tokens,
// passwords, or email addresses (SEC-12).
type AuditEntry struct {
	Action     string
	ActorID    string
	TargetType string
	TargetID   string
	Details    map[string]any
}

// Audit appends to audit.audit_log. The auth_svc role has INSERT only, so the statement
// has no RETURNING clause.
type Audit struct {
	pool    *pgxpool.Pool
	service string
}

// NewAudit returns an audit writer that stamps every row with service.
func NewAudit(pool *pgxpool.Pool, service string) *Audit {
	return &Audit{pool: pool, service: service}
}

// Write inserts one entry.
//
// details is sent as a string with an explicit ::jsonb cast. With QueryExecModeExec pgx
// picks the parameter type from the Go type, so a []byte would be sent as bytea and fail.
func (a *Audit) Write(ctx context.Context, e AuditEntry) error {
	details := "{}"
	if len(e.Details) > 0 {
		b, err := json.Marshal(e.Details)
		if err != nil {
			return fmt.Errorf("audit details: %w", err)
		}
		details = string(b)
	}
	_, err := a.pool.Exec(ctx,
		`INSERT INTO audit.audit_log (service, actor_id, action, target_type, target_id, details)
		 VALUES ($1, $2, $3, $4, $5, $6::jsonb)`,
		a.service, nullIfEmpty(e.ActorID), e.Action, nullIfEmpty(e.TargetType), nullIfEmpty(e.TargetID), details)
	if err != nil {
		return fmt.Errorf("audit write: %w", err)
	}
	return nil
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
