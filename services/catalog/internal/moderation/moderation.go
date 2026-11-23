// Package moderation implements operator hide and restore of videos (FR-MOD-04). The
// status change and its audit row commit in one transaction (Data Design §2.1).
package moderation

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/lifecycle"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/store"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

// AuditWriter appends an audit row inside the given transaction. *store.Audit implements
// it.
type AuditWriter interface {
	Write(ctx context.Context, db store.DBTX, e store.AuditEntry) error
}

// Transitioner is implemented by *lifecycle.Machine.
type Transitioner interface {
	Transition(ctx context.Context, id string, a lifecycle.Action, ch lifecycle.Changes,
		within func(ctx context.Context, tx pgx.Tx) error) error
}

// Service implements the moderation use cases.
type Service struct {
	machine Transitioner
	audit   AuditWriter
}

// NewService returns a Service.
func NewService(machine Transitioner, audit AuditWriter) *Service {
	return &Service{machine: machine, audit: audit}
}

// Hide moves a READY video to HIDDEN.
func (s *Service) Hide(ctx context.Context, operatorID, id string) error {
	return s.apply(ctx, operatorID, id, lifecycle.Hide, store.ActionVideoHidden)
}

// Restore moves a HIDDEN video back to READY.
func (s *Service) Restore(ctx context.Context, operatorID, id string) error {
	return s.apply(ctx, operatorID, id, lifecycle.Restore, store.ActionVideoRestored)
}

// apply returns video.ErrNotFound for an unknown or malformed id and *video.ConflictError
// for any status the action does not accept, including a repeated hide or restore.
func (s *Service) apply(ctx context.Context, operatorID, id string, a lifecycle.Action, auditAction string) error {
	if !video.ValidID(id) {
		return video.ErrNotFound
	}
	id = strings.ToLower(id)
	rule, _ := lifecycle.RuleFor(a)

	err := s.machine.Transition(ctx, id, a, lifecycle.Changes{}, func(ctx context.Context, tx pgx.Tx) error {
		return s.audit.Write(ctx, tx, store.AuditEntry{
			Action:     auditAction,
			ActorID:    operatorID,
			TargetType: "video",
			TargetID:   id,
			Details:    map[string]any{"from": string(rule.From[0]), "to": string(rule.To)},
		})
	})
	var conflict *video.ConflictError
	switch {
	case err == nil:
		return nil
	case errors.Is(err, lifecycle.ErrNoop):
		return &video.ConflictError{Current: rule.To}
	case errors.Is(err, video.ErrNotFound), errors.As(err, &conflict):
		return err
	default:
		return fmt.Errorf("%w: %w", video.ErrUnavailable, err)
	}
}
