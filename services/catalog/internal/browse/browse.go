// Package browse implements the public lists: the feed (FR-BR-01, FR-BR-02), search
// (FR-BR-03), and creator pages (FR-BR-07). Every list shows READY videos only and uses
// keyset pagination (SCAL-06).
package browse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/cursor"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

// Page limits (BR-LIM, openapi parameters.Limit).
const (
	DefaultLimit = 20
	MaxLimit     = 50
	MaxQueryLen  = 200
)

// Cache TTLs (Data Design §3.2).
const (
	FeedTTL   = 30 * time.Second
	SearchTTL = 30 * time.Second
)

// Repository reads the lists. Each method returns at most limit rows after the cursor
// (nil for the first page). *store.Videos implements it.
type Repository interface {
	ListRecent(ctx context.Context, after *cursor.Cursor, limit int) ([]video.Video, error)
	ListMostViewed(ctx context.Context, after *cursor.Cursor, limit int) ([]video.Video, error)
	ListByCreator(ctx context.Context, ownerID string, after *cursor.Cursor, limit int) ([]video.Video, error)
	CreatorExists(ctx context.Context, userID string) (bool, error)
	Search(ctx context.Context, text string, after *cursor.Cursor, limit int) ([]video.Ranked, error)
}

// PageCache stores whole pages under a key (cache-aside). load runs on a miss, and when
// the cache is unavailable. A nil PageCache disables caching.
type PageCache interface {
	Page(ctx context.Context, key string, ttl time.Duration, load func(context.Context) (Page, error)) (Page, error)
}

// Page is one page of a list. NextCursor is "" on the last page.
type Page struct {
	Items      []video.Video `json:"items"`
	NextCursor string        `json:"nextCursor"`
}

// Sort is the feed order.
type Sort string

const (
	SortRecent     Sort = "recent"
	SortMostViewed Sort = "most_viewed"
)

// Service implements the public lists.
type Service struct {
	repo  Repository
	cache PageCache
}

// NewService returns a Service. cache may be nil.
func NewService(repo Repository, cache PageCache) *Service {
	return &Service{repo: repo, cache: cache}
}

// Feed returns a page of the feed. Invalid parameters return a *video.ValidationError.
func (s *Service) Feed(ctx context.Context, sort Sort, rawCursor string, limit int) (Page, error) {
	var cs cursor.Sort
	switch sort {
	case "", SortRecent:
		sort, cs = SortRecent, cursor.Recent
	case SortMostViewed:
		cs = cursor.MostViewed
	default:
		return Page{}, &video.ValidationError{Field: "sort", Rule: "enum", Message: "sort must be recent or most_viewed"}
	}
	after, err := decodeCursor(rawCursor, cs, "")
	if err != nil {
		return Page{}, err
	}
	if err := checkLimit(limit); err != nil {
		return Page{}, err
	}

	load := func(ctx context.Context) (Page, error) {
		var rows []video.Video
		var err error
		if sort == SortMostViewed {
			rows, err = s.repo.ListMostViewed(ctx, after, limit+1)
		} else {
			rows, err = s.repo.ListRecent(ctx, after, limit+1)
		}
		if err != nil {
			return Page{}, fmt.Errorf("%w: %w", video.ErrUnavailable, err)
		}
		return page(rows, limit, func(v video.Video) cursor.Cursor {
			if sort == SortMostViewed {
				return cursor.AfterInt(cs, "", v.ViewCount, v.ID)
			}
			return cursor.AfterTime(cs, "", *v.PublishedAt, v.ID)
		}), nil
	}
	return s.cached(ctx, fmt.Sprintf("feed:%s:%s:%d", sort, keyPart(rawCursor), limit), FeedTTL, load)
}

// Creator returns a page of one creator's videos, or video.ErrNotFound if the user has no
// public profile (or the id is malformed).
func (s *Service) Creator(ctx context.Context, userID, rawCursor string, limit int) (Page, error) {
	if !video.ValidID(userID) {
		return Page{}, video.ErrNotFound
	}
	userID = strings.ToLower(userID)
	after, err := decodeCursor(rawCursor, cursor.Creator, userID)
	if err != nil {
		return Page{}, err
	}
	if err := checkLimit(limit); err != nil {
		return Page{}, err
	}
	ok, err := s.repo.CreatorExists(ctx, userID)
	if err != nil {
		return Page{}, fmt.Errorf("%w: %w", video.ErrUnavailable, err)
	}
	if !ok {
		return Page{}, video.ErrNotFound
	}
	rows, err := s.repo.ListByCreator(ctx, userID, after, limit+1)
	if err != nil {
		return Page{}, fmt.Errorf("%w: %w", video.ErrUnavailable, err)
	}
	return page(rows, limit, func(v video.Video) cursor.Cursor {
		return cursor.AfterTime(cursor.Creator, userID, *v.PublishedAt, v.ID)
	}), nil
}

// Search returns a ranked page of results. No match returns an empty page.
func (s *Service) Search(ctx context.Context, q, rawCursor string, limit int) (Page, error) {
	text, err := NormalizeQuery(q)
	if err != nil {
		return Page{}, err
	}
	scope := cursor.SearchScope(text)
	after, err := decodeCursor(rawCursor, cursor.Search, scope)
	if err != nil {
		return Page{}, err
	}
	if err := checkLimit(limit); err != nil {
		return Page{}, err
	}

	load := func(ctx context.Context) (Page, error) {
		hits, err := s.repo.Search(ctx, text, after, limit+1)
		if err != nil {
			return Page{}, fmt.Errorf("%w: %w", video.ErrUnavailable, err)
		}
		p := Page{Items: make([]video.Video, 0, min(len(hits), limit))}
		for i, h := range hits {
			if i == limit {
				last := hits[limit-1]
				p.NextCursor = cursor.AfterFloat(cursor.Search, scope, last.Rank, last.Video.ID).Encode()
				break
			}
			p.Items = append(p.Items, h.Video)
		}
		return p, nil
	}
	return s.cached(ctx, fmt.Sprintf("search:%s:%s:%d", scope, keyPart(rawCursor), limit), SearchTTL, load)
}

// NormalizeQuery trims the text, collapses runs of whitespace, and lower-cases it, so
// equivalent queries share cursors and cache entries. It must be 1-200 characters.
func NormalizeQuery(q string) (string, error) {
	if !utf8.ValidString(q) {
		return "", &video.ValidationError{Field: "q", Rule: video.RuleFormat, Message: "q must be valid UTF-8"}
	}
	text := strings.ToLower(strings.Join(strings.Fields(q), " "))
	if text == "" {
		return "", &video.ValidationError{Field: "q", Rule: video.RuleRequired, Message: "q is required"}
	}
	if utf8.RuneCountInString(text) > MaxQueryLen {
		return "", &video.ValidationError{Field: "q", Rule: video.RuleLength, Message: "q must be 1-200 characters"}
	}
	return text, nil
}

func (s *Service) cached(ctx context.Context, key string, ttl time.Duration, load func(context.Context) (Page, error)) (Page, error) {
	if s.cache == nil {
		return load(ctx)
	}
	return s.cache.Page(ctx, key, ttl, load)
}

// page trims the limit+1 rows to limit and sets the next cursor from the last kept row.
func page(rows []video.Video, limit int, next func(video.Video) cursor.Cursor) Page {
	if len(rows) <= limit {
		return Page{Items: rows}
	}
	rows = rows[:limit]
	return Page{Items: rows, NextCursor: next(rows[limit-1]).Encode()}
}

func decodeCursor(raw string, sort cursor.Sort, scope string) (*cursor.Cursor, error) {
	if raw == "" {
		return nil, nil
	}
	c, err := cursor.Decode(raw, sort, scope)
	if errors.Is(err, cursor.ErrInvalid) {
		return nil, &video.ValidationError{Field: "cursor", Rule: video.RuleFormat, Message: "cursor is invalid or belongs to another list"}
	}
	return &c, err
}

// CheckLimit validates a page size. The HTTP layer turns a missing limit into
// DefaultLimit before calling the services.
func CheckLimit(limit int) error { return checkLimit(limit) }

func checkLimit(limit int) error {
	if limit < 1 || limit > MaxLimit {
		return &video.ValidationError{Field: "limit", Rule: "range", Message: "limit must be between 1 and 50"}
	}
	return nil
}

// DecodeCursor exposes the cursor check to other list services (the creator studio).
func DecodeCursor(raw string, sort cursor.Sort, scope string) (*cursor.Cursor, error) {
	return decodeCursor(raw, sort, scope)
}

// NextPage exposes the limit+1 trimming to other list services.
func NextPage(rows []video.Video, limit int, next func(video.Video) cursor.Cursor) Page {
	return page(rows, limit, next)
}

func keyPart(rawCursor string) string {
	if rawCursor == "" {
		return "-"
	}
	return rawCursor
}
