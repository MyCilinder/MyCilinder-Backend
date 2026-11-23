package video

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Repository reads videos. *store.Videos implements it.
type Repository interface {
	// GetByID returns the video in any status, or ErrNotFound if no row exists.
	GetByID(ctx context.Context, id string) (Video, error)
}

// Detail is a video as one caller may see it.
type Detail struct {
	Video
	// Owner is true when the caller owns the video; only then are status and
	// failureReason returned.
	Owner bool
}

// Service implements the video detail use case.
type Service struct {
	repo Repository
}

// NewService returns a Service.
func NewService(repo Repository) *Service { return &Service{repo: repo} }

// Get returns the video for viewerID ("" for a guest). It returns ErrNotFound for a
// malformed id, an unknown id, and a video the caller may not see, and ErrUnavailable
// when the database fails.
func (s *Service) Get(ctx context.Context, id, viewerID string) (Detail, error) {
	if !ValidID(id) {
		return Detail{}, ErrNotFound
	}
	// Ids are stored lower-case. Normalizing keeps one cache key per video, so
	// invalidation always hits the entry.
	id = strings.ToLower(id)
	v, err := s.repo.GetByID(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return Detail{}, ErrNotFound
	}
	if err != nil {
		return Detail{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if !v.VisibleTo(viewerID) {
		return Detail{}, ErrNotFound
	}
	return Detail{Video: v, Owner: v.IsOwner(viewerID)}, nil
}
