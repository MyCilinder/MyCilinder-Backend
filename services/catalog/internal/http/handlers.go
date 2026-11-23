package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/jwtauth"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/problem"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/browse"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/cfsign"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/playback"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

// VideoService is implemented by *video.Service.
type VideoService interface {
	Get(ctx context.Context, id, viewerID string) (video.Detail, error)
}

// BrowseService is implemented by *browse.Service.
type BrowseService interface {
	Feed(ctx context.Context, sort browse.Sort, rawCursor string, limit int) (browse.Page, error)
	Search(ctx context.Context, q, rawCursor string, limit int) (browse.Page, error)
	Creator(ctx context.Context, userID, rawCursor string, limit int) (browse.Page, error)
}

// StudioService is implemented by *studio.Service.
type StudioService interface {
	List(ctx context.Context, sub, rawCursor string, limit int) (browse.Page, error)
	Update(ctx context.Context, sub, id string, title, description *string) (video.Detail, error)
	Delete(ctx context.Context, sub, id string) error
}

// ModerationService is implemented by *moderation.Service.
type ModerationService interface {
	Hide(ctx context.Context, operatorID, id string) error
	Restore(ctx context.Context, operatorID, id string) error
}

// PlaybackService is implemented by *playback.Service.
type PlaybackService interface {
	Grant(ctx context.Context, id, viewerID string) (cfsign.Grant, error)
}

// handlers holds the route handlers.
type handlers struct {
	videos     VideoService
	playback   PlaybackService
	browse     BrowseService
	studio     StudioService
	moderation ModerationService
	media      mediaURLs
}

// --- public lists ---

// GET /feed?sort=recent|most_viewed&cursor=&limit=
func (h *handlers) getFeed(w http.ResponseWriter, r *http.Request) {
	c, limit, ok := pageParams(w, r)
	if !ok {
		return
	}
	p, err := h.browse.Feed(r.Context(), browse.Sort(r.URL.Query().Get("sort")), c, limit)
	h.writeCards(w, r, p, err)
}

// GET /search?q=&cursor=&limit=
func (h *handlers) searchVideos(w http.ResponseWriter, r *http.Request) {
	c, limit, ok := pageParams(w, r)
	if !ok {
		return
	}
	p, err := h.browse.Search(r.Context(), r.URL.Query().Get("q"), c, limit)
	h.writeCards(w, r, p, err)
}

// GET /creators/{userId}/videos
func (h *handlers) listCreatorVideos(w http.ResponseWriter, r *http.Request) {
	c, limit, ok := pageParams(w, r)
	if !ok {
		return
	}
	p, err := h.browse.Creator(r.Context(), chi.URLParam(r, "userId"), c, limit)
	h.writeCards(w, r, p, err)
}

func (h *handlers) writeCards(w http.ResponseWriter, r *http.Request, p browse.Page, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := videoCardPage{Items: make([]videoCard, 0, len(p.Items)), NextCursor: nullable(p.NextCursor)}
	for _, v := range p.Items {
		out.Items = append(out.Items, h.toVideoCard(v))
	}
	writeJSON(w, http.StatusOK, out)
}

// --- video detail ---

// GET /videos/{videoId} (optional token). Guests and other users see READY videos only;
// the owner sees every status except DELETED, plus status and failureReason.
func (h *handlers) getVideo(w http.ResponseWriter, r *http.Request) {
	claims, _ := jwtauth.FromContext(r.Context()) // zero Claims for a guest
	d, err := h.videos.Get(r.Context(), chi.URLParam(r, "videoId"), claims.Subject)
	if err != nil {
		writeError(w, r, err)
		return
	}
	h.writeDetail(w, d)
}

func (h *handlers) writeDetail(w http.ResponseWriter, d video.Detail) {
	if d.Owner {
		// The owner view includes non-public fields; keep it out of shared caches.
		w.Header().Set("Cache-Control", "private, no-store")
		writeJSON(w, http.StatusOK, h.toOwnerVideoDetail(d))
		return
	}
	writeJSON(w, http.StatusOK, h.toVideoDetail(d))
}

// playbackAccess is the PlaybackAccess body.
type playbackAccess struct {
	MasterPlaylistURL string            `json:"masterPlaylistUrl"`
	ExpiresAt         string            `json:"expiresAt"`
	Cookies           map[string]string `json:"cookies"`
}

// GET /videos/{videoId}/playback (optional token). Three Set-Cookie lines for web
// clients, and the same values in the body for native players (Interface Design §1.3).
func (h *handlers) getPlayback(w http.ResponseWriter, r *http.Request) {
	claims, _ := jwtauth.FromContext(r.Context())
	g, err := h.playback.Grant(r.Context(), chi.URLParam(r, "videoId"), claims.Subject)
	if errors.Is(err, playback.ErrNotConfigured) {
		middleware.Logger(r.Context()).Error("playback requested but CLOUDFRONT_* is not set")
		problem.Write(w, r, http.StatusServiceUnavailable, problem.Unavailable, "")
		return
	}
	if err != nil {
		writeError(w, r, err)
		return
	}
	body := playbackAccess{
		MasterPlaylistURL: g.MasterPlaylistURL,
		ExpiresAt:         formatTime(g.ExpiresAt),
		Cookies:           make(map[string]string, len(g.Cookies)),
	}
	for _, c := range g.Cookies {
		http.SetCookie(w, c)
		body.Cookies[c.Name] = c.Value
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, body)
}

// --- studio ---

// GET /me/videos
func (h *handlers) listMyVideos(w http.ResponseWriter, r *http.Request) {
	claims, _ := jwtauth.FromContext(r.Context())
	c, limit, ok := pageParams(w, r)
	if !ok {
		return
	}
	p, err := h.studio.List(r.Context(), claims.Subject, c, limit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	out := ownVideoPage{Items: make([]ownVideo, 0, len(p.Items)), NextCursor: nullable(p.NextCursor)}
	for _, v := range p.Items {
		out.Items = append(out.Items, h.toOwnVideo(v))
	}
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, http.StatusOK, out)
}

type updateVideoRequest struct {
	Title       *string `json:"title"`
	Description *string `json:"description"`
}

// PATCH /videos/{videoId}
func (h *handlers) updateVideo(w http.ResponseWriter, r *http.Request) {
	claims, _ := jwtauth.FromContext(r.Context())
	var req updateVideoRequest
	if !decode(w, r, &req) {
		return
	}
	d, err := h.studio.Update(r.Context(), claims.Subject, chi.URLParam(r, "videoId"), req.Title, req.Description)
	if err != nil {
		writeError(w, r, err)
		return
	}
	h.writeDetail(w, d)
}

// DELETE /videos/{videoId}
func (h *handlers) deleteVideo(w http.ResponseWriter, r *http.Request) {
	claims, _ := jwtauth.FromContext(r.Context())
	if err := h.studio.Delete(r.Context(), claims.Subject, chi.URLParam(r, "videoId")); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- moderation ---

// POST /ops/videos/{videoId}/hide
func (h *handlers) hideVideo(w http.ResponseWriter, r *http.Request) {
	claims, _ := jwtauth.FromContext(r.Context())
	h.writeNoContent(w, r, h.moderation.Hide(r.Context(), claims.Subject, chi.URLParam(r, "videoId")))
}

// POST /ops/videos/{videoId}/restore
func (h *handlers) restoreVideo(w http.ResponseWriter, r *http.Request) {
	claims, _ := jwtauth.FromContext(r.Context())
	h.writeNoContent(w, r, h.moderation.Restore(r.Context(), claims.Subject, chi.URLParam(r, "videoId")))
}

func (h *handlers) writeNoContent(w http.ResponseWriter, r *http.Request, err error) {
	if err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- response bodies (openapi.yaml components.schemas) ---

type userRef struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

// videoCard is a list item. Lists contain READY videos only, so media fields are set.
type videoCard struct {
	ID              string  `json:"id"`
	Title           string  `json:"title"`
	ThumbnailURL    string  `json:"thumbnailUrl"`
	Creator         userRef `json:"creator"`
	DurationSeconds int     `json:"durationSeconds"`
	ViewCount       int64   `json:"viewCount"`
	PublishedAt     string  `json:"publishedAt"`
}

type videoCardPage struct {
	Items      []videoCard `json:"items"`
	NextCursor *string     `json:"nextCursor"`
}

// videoDetail is the public VideoDetail. Media fields are null or empty until READY.
type videoDetail struct {
	ID              string   `json:"id"`
	Title           string   `json:"title"`
	Description     string   `json:"description"`
	Creator         userRef  `json:"creator"`
	ThumbnailURL    *string  `json:"thumbnailUrl"`
	DurationSeconds *int     `json:"durationSeconds"`
	Renditions      []string `json:"renditions"`
	ViewCount       int64    `json:"viewCount"`
	PublishedAt     *string  `json:"publishedAt"`
	CreatedAt       string   `json:"createdAt"`
}

// ownerVideoDetail adds the owner-only fields.
type ownerVideoDetail struct {
	videoDetail
	Status        video.Status `json:"status"`
	FailureReason *string      `json:"failureReason"`
}

// ownVideo is a creator studio item.
type ownVideo struct {
	ID              string       `json:"id"`
	Title           string       `json:"title"`
	Status          video.Status `json:"status"`
	FailureReason   *string      `json:"failureReason"`
	ThumbnailURL    *string      `json:"thumbnailUrl"`
	DurationSeconds *int         `json:"durationSeconds"`
	CreatedAt       string       `json:"createdAt"`
	PublishedAt     *string      `json:"publishedAt"`
	ViewCount       int64        `json:"viewCount"`
	LikeCount       int64        `json:"likeCount"`
	CommentCount    int64        `json:"commentCount"`
}

type ownVideoPage struct {
	Items      []ownVideo `json:"items"`
	NextCursor *string    `json:"nextCursor"`
}

func (h *handlers) toVideoCard(v video.Video) videoCard {
	out := videoCard{
		ID:           v.ID,
		Title:        v.Title,
		ThumbnailURL: h.media.thumbnail(v.ID),
		Creator:      userRef{ID: v.OwnerID, DisplayName: v.OwnerName},
		ViewCount:    v.ViewCount,
	}
	if v.DurationSeconds != nil {
		out.DurationSeconds = *v.DurationSeconds
	}
	if v.PublishedAt != nil {
		out.PublishedAt = formatTime(*v.PublishedAt)
	}
	return out
}

func (h *handlers) toVideoDetail(d video.Detail) videoDetail {
	v := d.Video
	out := videoDetail{
		ID:          v.ID,
		Title:       v.Title,
		Description: v.Description,
		Creator:     userRef{ID: v.OwnerID, DisplayName: v.OwnerName},
		Renditions:  []string{},
		ViewCount:   v.ViewCount,
		CreatedAt:   formatTime(v.CreatedAt),
	}
	if v.HasMedia() {
		thumb := h.media.thumbnail(v.ID)
		out.ThumbnailURL = &thumb
		out.DurationSeconds = v.DurationSeconds
		if v.Renditions != nil {
			out.Renditions = v.Renditions
		}
		out.PublishedAt = formatTimePtr(v.PublishedAt)
	}
	return out
}

func (h *handlers) toOwnerVideoDetail(d video.Detail) ownerVideoDetail {
	out := ownerVideoDetail{videoDetail: h.toVideoDetail(d), Status: d.Status}
	if d.Status == video.StatusFailed {
		out.FailureReason = d.FailureReason
	}
	return out
}

func (h *handlers) toOwnVideo(v video.Video) ownVideo {
	out := ownVideo{
		ID:           v.ID,
		Title:        v.Title,
		Status:       v.Status,
		CreatedAt:    formatTime(v.CreatedAt),
		ViewCount:    v.ViewCount,
		LikeCount:    v.LikeCount,
		CommentCount: v.CommentCount,
	}
	if v.Status == video.StatusFailed {
		out.FailureReason = v.FailureReason
	}
	if v.HasMedia() {
		thumb := h.media.thumbnail(v.ID)
		out.ThumbnailURL = &thumb
		out.DurationSeconds = v.DurationSeconds
		out.PublishedAt = formatTimePtr(v.PublishedAt)
	}
	return out
}

// mediaURLs builds the unsigned CloudFront thumbnail URLs (BR-06). With no domain
// configured (local development without CloudFront) the URL is relative.
type mediaURLs struct {
	domain string
}

func (m mediaURLs) thumbnail(videoID string) string {
	path := "/thumbnails/" + videoID + ".jpg"
	if m.domain == "" {
		return path
	}
	return "https://" + m.domain + path
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func formatTimePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	s := formatTime(*t)
	return &s
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// --- responses ---

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// writeError maps service errors to problem responses (Interface Design §1.1).
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var (
		invalid  *video.ValidationError
		conflict *video.ConflictError
	)
	switch {
	case errors.As(err, &invalid):
		writeValidation(w, r, invalid.Message,
			problem.FieldError{Field: invalid.Field, Rule: invalid.Rule, Message: invalid.Message})
	case errors.Is(err, video.ErrNotFound):
		problem.Write(w, r, http.StatusNotFound, problem.NotFound, "")
	case errors.Is(err, video.ErrForbidden):
		problem.Write(w, r, http.StatusForbidden, problem.Forbidden, "Only the owner can modify this video.")
	case errors.As(err, &conflict):
		problem.Write(w, r, http.StatusConflict, problem.Conflict, "Video is "+string(conflict.Current)+".")
	case errors.Is(err, video.ErrUnavailable):
		middleware.Logger(r.Context()).Error("dependency unavailable", "err", err)
		problem.Write(w, r, http.StatusServiceUnavailable, problem.Unavailable, "")
	default:
		middleware.Logger(r.Context()).Error("unexpected error", "err", err)
		problem.Write(w, r, http.StatusInternalServerError, problem.Internal, "")
	}
}
