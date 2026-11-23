// Package video holds the Catalog domain types (Data Design §1.3, Functional
// Requirements §1.0), the title and description rules, and the visibility rules for
// video detail (FR-PB-06, BR-09).
package video

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

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

// UnknownCreatorName is shown when the owner has no row in auth.v_user_public (for
// example, the profile was never created because the database was down at first login).
const UnknownCreatorName = "Usuario"

var (
	// ErrNotFound: the video does not exist or the caller may not see it. The two cases
	// are deliberately indistinguishable (BR-09).
	ErrNotFound = errors.New("video: not found")
	// ErrUnavailable: the database failed.
	ErrUnavailable = errors.New("video: dependency unavailable")
	// ErrForbidden: the caller can see the video but does not own it.
	ErrForbidden = errors.New("video: not the owner")
)

// ConflictError: the requested status change is not allowed from the current status.
type ConflictError struct {
	Current Status
}

func (e *ConflictError) Error() string { return "video: not allowed while " + string(e.Current) }

// Ranked is a search hit with its ts_rank value.
type Ranked struct {
	Video Video
	Rank  float32
}

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

// IsOwner reports whether viewerID owns the video. An empty viewerID is a guest. Ownership
// comes from the token sub only (SEC-02).
func (v Video) IsOwner(viewerID string) bool {
	return viewerID != "" && strings.EqualFold(viewerID, v.OwnerID)
}

// VisibleTo reports whether viewerID may see the video: everyone sees READY videos, and
// the owner also sees every other status except DELETED.
func (v Video) VisibleTo(viewerID string) bool {
	if v.Status == StatusReady {
		return true
	}
	return v.IsOwner(viewerID) && v.Status != StatusDeleted
}

// HasMedia reports whether the HLS outputs and thumbnail may be exposed. Media fields are
// null or empty until the video is READY (openapi VideoDetail).
func (v Video) HasMedia() bool { return v.Status == StatusReady }

// ValidID reports whether s is a UUID in the canonical 8-4-4-4-12 hex form. Callers treat
// a malformed id as not found, so a bad path parameter never reaches the database.
func ValidID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
				return false
			}
		}
	}
	return true
}

// Rule ids used in problem `errors[].rule` (openapi FieldError).
const (
	RuleRequired = "required"
	RuleLength   = "length"
	RuleFormat   = "format"
)

// ValidationError reports which rule a field broke.
type ValidationError struct {
	Field   string
	Rule    string
	Message string
}

func (e *ValidationError) Error() string { return e.Field + ": " + e.Message }

// NormalizeTitle trims surrounding whitespace and checks the 1-100 character limit.
// Control characters are rejected so titles cannot break logs or client layouts.
func NormalizeTitle(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", &ValidationError{Field: "title", Rule: RuleRequired, Message: "title is required"}
	}
	if !utf8.ValidString(s) {
		return "", &ValidationError{Field: "title", Rule: RuleFormat, Message: "title must be valid UTF-8"}
	}
	if utf8.RuneCountInString(s) > MaxTitleLength {
		return "", &ValidationError{Field: "title", Rule: RuleLength, Message: "title must be 1-100 characters"}
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", &ValidationError{Field: "title", Rule: RuleFormat, Message: "title must not contain control characters"}
		}
	}
	return s, nil
}

// ValidateDescription checks the 0-5,000 character limit. The description is stored as
// sent: line breaks and tabs are allowed, other control characters are not.
func ValidateDescription(s string) error {
	if !utf8.ValidString(s) {
		return &ValidationError{Field: "description", Rule: RuleFormat, Message: "description must be valid UTF-8"}
	}
	if utf8.RuneCountInString(s) > MaxDescriptionLength {
		return &ValidationError{Field: "description", Rule: RuleLength, Message: "description must be at most 5000 characters"}
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' {
			return &ValidationError{Field: "description", Rule: RuleFormat, Message: "description must not contain control characters"}
		}
	}
	return nil
}
