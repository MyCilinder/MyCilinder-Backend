package store

import (
	"context"
	"encoding/json"
	"fmt"
)

// Audit actions written by Catalog (Data Design §1.3).
const (
	ActionVideoHidden   = "video.hidden"
	ActionVideoRestored = "video.restored"
)

// AuditEntry is one audit_log row. Details must never contain tokens, passwords, or
// email addresses (SEC-12).
type AuditEntry struct {
	Action     string
	ActorID    string
	TargetType string
	TargetID   string
	Details    map[string]any
}

// Audit appends to audit.audit_log. The catalog_svc role has INSERT only, so the
// statement has no RETURNING clause.
type Audit struct {
	service string
}

// NewAudit returns an audit writer that stamps every row with service.
func NewAudit(service string) *Audit { return &Audit{service: service} }

// Write inserts one entry through db, normally the transaction that made the change, so
// the change and its audit row commit or roll back together (Data Design §2.1).
//
// details is sent as a string with an explicit ::jsonb cast. With QueryExecModeExec pgx
// picks the parameter type from the Go type, so a []byte would be sent as bytea and fail.
func (a *Audit) Write(ctx context.Context, db DBTX, e AuditEntry) error {
	details := "{}"
	if len(e.Details) > 0 {
		b, err := json.Marshal(e.Details)
		if err != nil {
			return fmt.Errorf("audit details: %w", err)
		}
		details = string(b)
	}
	_, err := db.Exec(ctx,
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
