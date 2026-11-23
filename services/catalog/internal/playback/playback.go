// Package playback implements GET /videos/{videoId}/playback (FR-PB-02, FR-PB-06): the
// visibility check, then signed cookies for the video's HLS prefix.
package playback

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/cfsign"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

// ErrNotConfigured: the CloudFront settings are missing, so cookies cannot be signed.
var ErrNotConfigured = errors.New("playback: CloudFront signing is not configured")

// Signer is implemented by *cfsign.Signer.
type Signer interface {
	Sign(videoID string) (cfsign.Grant, error)
}

// Service grants playback.
type Service struct {
	repo   video.Repository
	signer Signer
}

// NewService returns a Service. signer may be nil when CloudFront is not configured;
// every request then returns ErrNotConfigured after the visibility check.
func NewService(repo video.Repository, signer Signer) *Service {
	return &Service{repo: repo, signer: signer}
}

// Grant returns signed cookies for a READY video. Non-owners get video.ErrNotFound for
// any other status; the owner of a video that is not READY (and not DELETED) gets
// *video.ConflictError, so the studio can tell the creator why it cannot play yet.
func (s *Service) Grant(ctx context.Context, id, viewerID string) (cfsign.Grant, error) {
	if !video.ValidID(id) {
		return cfsign.Grant{}, video.ErrNotFound
	}
	id = strings.ToLower(id)
	v, err := s.repo.GetByID(ctx, id)
	if errors.Is(err, video.ErrNotFound) {
		return cfsign.Grant{}, video.ErrNotFound
	}
	if err != nil {
		return cfsign.Grant{}, fmt.Errorf("%w: %w", video.ErrUnavailable, err)
	}
	if !v.VisibleTo(viewerID) {
		return cfsign.Grant{}, video.ErrNotFound
	}
	if v.Status != video.StatusReady {
		return cfsign.Grant{}, &video.ConflictError{Current: v.Status}
	}
	if s.signer == nil {
		return cfsign.Grant{}, ErrNotConfigured
	}
	return s.signer.Sign(v.ID)
}
