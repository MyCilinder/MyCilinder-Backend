// Package video holds the Catalog domain types (Data Design §1.3, Functional
// Requirements §1.0).
package video

import "time"

// Status mirrors the catalog.video_status enum. UPLOADED is transient and never stored.
type Status string

const (
	StatusPendingUpload Status = "PENDING_UPLOAD"
	StatusProcessing    Status = "PROCESSING"
	StatusReady         Status = "READY"
	StatusFailed        Status = "FAILED"
	StatusHidden        Status = "HIDDEN"
	StatusDeleted       Status = "DELETED"
	StatusExpired       Status = "EXPIRED"
)

// Limits from BR-LIM, counted in characters (runes), like char_length in the database.
const (
	MaxTitleLength       = 100
	MaxDescriptionLength = 5000
)

// Video is one row of catalog.videos, joined with the creator's display name and the
// counters from interactions.v_video_stats.
type Video struct {
	ID              string
	OwnerID         string
	OwnerName       string
	Title           string
	Description     string
	Status          Status
	FailureReason   *string
	DurationSeconds *int
	Renditions      []string
	CreatedAt       time.Time
	UpdatedAt       time.Time
	PublishedAt     *time.Time
	DeletedAt       *time.Time

	ViewCount    int64
	LikeCount    int64
	CommentCount int64
}
