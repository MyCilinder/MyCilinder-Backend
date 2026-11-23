// Package studio implements the creator studio: the caller's own videos (FR-CR-01,
// FR-CR-02), edits (FR-CR-03), and soft delete (FR-CR-04, BR-05).
//
// Ownership comes from the token sub only (SEC-02). A caller who does not own the video
// gets 403 only if they could see it anyway (it is READY); otherwise 404, so the
// existence of a non-public video is never revealed (Catalog plan, decision 4).
package studio

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/browse"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/cursor"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/lifecycle"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/store"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

// Lister reads the caller's videos. *store.Videos implements it.
type Lister interface {
	ListByOwner(ctx context.Context, ownerID string, after *cursor.Cursor, limit int) ([]video.Video, error)
}

// Service implements the studio use cases.
type Service struct {
	writer  *pgxpool.Pool
	lister  Lister
	machine *lifecycle.Machine
}

// NewService returns a Service. Edits and deletes run on writer.
func NewService(writer *pgxpool.Pool, lister Lister, machine *lifecycle.Machine) *Service {
	return &Service{writer: writer, lister: lister, machine: machine}
}

// List returns a page of the caller's videos in every status except DELETED, newest
// first.
func (s *Service) List(ctx context.Context, sub, rawCursor string, limit int) (browse.Page, error) {
	after, err := browse.DecodeCursor(rawCursor, cursor.Mine, "")
	if err != nil {
		return browse.Page{}, err
	}
	if err := browse.CheckLimit(limit); err != nil {
		return browse.Page{}, err
	}
	rows, err := s.lister.ListByOwner(ctx, strings.ToLower(sub), after, limit+1)
	if err != nil {
		return browse.Page{}, fmt.Errorf("%w: %w", video.ErrUnavailable, err)
	}
	return browse.NextPage(rows, limit, func(v video.Video) cursor.Cursor {
		return cursor.AfterTime(cursor.Mine, "", v.CreatedAt, v.ID)
	}), nil
}

// Update changes the title and/or description (nil leaves a field unchanged) and returns
// the owner's view of the video. Invalid values return a *video.ValidationError.
func (s *Service) Update(ctx context.Context, sub, id string, title, description *string) (video.Detail, error) {
	if title == nil && description == nil {
		return video.Detail{}, &video.ValidationError{Rule: video.RuleRequired, Message: "send title, description, or both"}
	}
	if title != nil {
		t, err := video.NormalizeTitle(*title)
		if err != nil {
			return video.Detail{}, err
		}
		title = &t
	}
	if description != nil {
		if err := video.ValidateDescription(*description); err != nil {
			return video.Detail{}, err
		}
	}
	if !video.ValidID(id) {
		return video.Detail{}, video.ErrNotFound
	}
	id = strings.ToLower(id)

	var out video.Video
	err := pgx.BeginFunc(ctx, s.writer, func(tx pgx.Tx) error {
		if err := checkOwner(ctx, tx, sub, id); err != nil {
			return err
		}
		if err := store.UpdateText(ctx, tx, id, title, description); err != nil {
			return err
		}
		var err error
		out, err = store.GetByIDTx(ctx, tx, id)
		return err
	})
	if err != nil {
		return video.Detail{}, mapErr(err)
	}
	s.machine.Invalidate(ctx, id)
	return video.Detail{Video: out, Owner: true}, nil
}

// Delete soft-deletes the video. Deleting a deleted video returns video.ErrNotFound;
// PENDING_UPLOAD, PROCESSING, and EXPIRED videos return *video.ConflictError (Catalog
// plan, decision 3).
func (s *Service) Delete(ctx context.Context, sub, id string) error {
	if !video.ValidID(id) {
		return video.ErrNotFound
	}
	id = strings.ToLower(id)
	err := pgx.BeginFunc(ctx, s.writer, func(tx pgx.Tx) error {
		if err := checkOwner(ctx, tx, sub, id); err != nil {
			return err
		}
		return lifecycle.Apply(ctx, tx, id, lifecycle.Delete, lifecycle.Changes{})
	})
	if errors.Is(err, lifecycle.ErrNoop) {
		// Unreachable after checkOwner (DELETED is 404 there), kept for safety.
		return video.ErrNotFound
	}
	if err != nil {
		return mapErr(err)
	}
	s.machine.Invalidate(ctx, id)
	return nil
}

// checkOwner locks the row and applies the owner rules. Concurrent edits and deletes of
// one video wait on the lock, so two deletes give one 204 and one 404.
func checkOwner(ctx context.Context, tx pgx.Tx, sub, id string) error {
	owner, status, err := store.LockForUpdate(ctx, tx, id)
	if err != nil {
		return err
	}
	if !strings.EqualFold(owner, sub) {
		if status == video.StatusReady {
			return video.ErrForbidden
		}
		return video.ErrNotFound
	}
	if status == video.StatusDeleted {
		return video.ErrNotFound
	}
	return nil
}

func mapErr(err error) error {
	var conflict *video.ConflictError
	switch {
	case errors.Is(err, video.ErrNotFound), errors.Is(err, video.ErrForbidden), errors.As(err, &conflict):
		return err
	default:
		return fmt.Errorf("%w: %w", video.ErrUnavailable, err)
	}
}
