package main

import (
	"net/http"
	"sort"
	"strings"
	"time"
)

type userRef struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
}

type videoCard struct {
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	ThumbnailURL    string    `json:"thumbnailUrl"`
	Creator         userRef   `json:"creator"`
	DurationSeconds int       `json:"durationSeconds"`
	ViewCount       int64     `json:"viewCount"`
	PublishedAt     time.Time `json:"publishedAt"`
}

type videoDetail struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	Description     string     `json:"description"`
	Creator         userRef    `json:"creator"`
	ThumbnailURL    *string    `json:"thumbnailUrl"`
	DurationSeconds *int       `json:"durationSeconds"`
	Renditions      []string   `json:"renditions"`
	ViewCount       int64      `json:"viewCount"`
	PublishedAt     *time.Time `json:"publishedAt"`
	CreatedAt       time.Time  `json:"createdAt"`
}

// ownerDetail adds the owner-only fields; failureReason is sent as null when unset.
type ownerDetail struct {
	videoDetail
	Status        string  `json:"status"`
	FailureReason *string `json:"failureReason"`
}

type ownVideo struct {
	ID              string     `json:"id"`
	Title           string     `json:"title"`
	Status          string     `json:"status"`
	FailureReason   *string    `json:"failureReason"`
	ThumbnailURL    *string    `json:"thumbnailUrl"`
	DurationSeconds *int       `json:"durationSeconds"`
	CreatedAt       time.Time  `json:"createdAt"`
	PublishedAt     *time.Time `json:"publishedAt"`
	ViewCount       int64      `json:"viewCount"`
	LikeCount       int64      `json:"likeCount"`
	CommentCount    int64      `json:"commentCount"`
}

func (s *server) thumbURL(r *http.Request, id string) string {
	return s.baseURL(r) + "/media/thumbnails/" + id + ".jpg"
}

// mediaFields returns thumbnail, duration and renditions, which are null or empty until
// the video has been published (READY, or HIDDEN after being READY).
func (s *server) mediaFields(r *http.Request, v *Video) (*string, *int, []string) {
	if v.PublishedAt == nil {
		return nil, nil, []string{}
	}
	t := s.thumbURL(r, v.ID)
	d := s.media.clip(v.ID).DurationSeconds
	return &t, &d, []string{"720p"}
}

// Caller holds s.store.mu.
func (s *server) card(r *http.Request, v *Video) videoCard {
	u := s.store.user(v.OwnerID)
	return videoCard{
		ID: v.ID, Title: v.Title, ThumbnailURL: s.thumbURL(r, v.ID),
		Creator:         userRef{ID: u.ID, DisplayName: u.DisplayName},
		DurationSeconds: s.media.clip(v.ID).DurationSeconds,
		ViewCount:       v.BaseViews, PublishedAt: *v.PublishedAt,
	}
}

// Caller holds s.store.mu.
func (s *server) detail(r *http.Request, v *Video, owner bool) any {
	u := s.store.user(v.OwnerID)
	thumb, dur, rend := s.mediaFields(r, v)
	d := videoDetail{
		ID: v.ID, Title: v.Title, Description: v.Description,
		Creator:      userRef{ID: u.ID, DisplayName: u.DisplayName},
		ThumbnailURL: thumb, DurationSeconds: dur, Renditions: rend,
		ViewCount: v.BaseViews, PublishedAt: v.PublishedAt, CreatedAt: v.CreatedAt,
	}
	if !owner {
		return d
	}
	return ownerDetail{videoDetail: d, Status: v.Status, FailureReason: v.FailureReason}
}

func (s *server) cardPage(w http.ResponseWriter, r *http.Request, vs []*Video) {
	items, next, ok := paginate(w, r, vs)
	if !ok {
		return
	}
	cards := make([]videoCard, len(items))
	for i, v := range items {
		cards[i] = s.card(r, v)
	}
	writeJSON(w, http.StatusOK, pageBody[videoCard]{Items: cards, NextCursor: next})
}

func (s *server) getFeed(w http.ResponseWriter, r *http.Request) {
	less := byRecent
	switch r.URL.Query().Get("sort") {
	case "", "recent":
	case "most_viewed":
		less = func(a, b *Video) bool {
			if a.BaseViews != b.BaseViews {
				return a.BaseViews > b.BaseViews
			}
			return a.ID > b.ID
		}
	default:
		badRequest(w, r, "sort", "enum", "sort must be recent or most_viewed")
		return
	}
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	s.cardPage(w, r, s.store.readyVideos(nil, less))
}

func (s *server) searchVideos(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if !checkLength(w, r, "q", q, 1, 200) {
		return
	}
	terms := searchTerms(q)

	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	type hit struct {
		v    *Video
		rank int
	}
	var hits []hit
	if len(terms) > 0 {
		for _, v := range s.store.readyVideos(nil, byRecent) {
			title, desc := normalize(v.Title), normalize(v.Description)
			rank, all := 0, true
			for _, t := range terms {
				inTitle, inDesc := strings.Contains(title, t), strings.Contains(desc, t)
				if !inTitle && !inDesc {
					all = false
					break
				}
				if inTitle {
					rank += 2
				}
				if inDesc {
					rank++
				}
			}
			if all {
				hits = append(hits, hit{v, rank})
			}
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].rank > hits[j].rank })
	vs := make([]*Video, len(hits))
	for i, h := range hits {
		vs[i] = h.v
	}
	s.cardPage(w, r, vs)
}

func (s *server) listCreatorVideos(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("userId")
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	if _, ok := s.store.users[id]; !ok {
		notFound(w, r)
		return
	}
	s.cardPage(w, r, s.store.readyVideos(func(v *Video) bool { return v.OwnerID == id }, byRecent))
}

func (s *server) getVideo(w http.ResponseWriter, r *http.Request) {
	me := viewer(r)
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	v, ok := s.store.videos[r.PathValue("videoId")]
	if !ok || !visibleTo(v, me) {
		notFound(w, r)
		return
	}
	writeJSON(w, http.StatusOK, s.detail(r, v, me == v.OwnerID))
}

func (s *server) getPlayback(w http.ResponseWriter, r *http.Request) {
	me := viewer(r)
	s.store.mu.Lock()
	v, ok := s.store.videos[r.PathValue("videoId")]
	if !ok || !visibleTo(v, me) {
		s.store.mu.Unlock()
		notFound(w, r)
		return
	}
	status := v.Status
	s.store.mu.Unlock()
	if status != StatusReady {
		conflict(w, r, "video is not ready for playback")
		return
	}

	// Fake CloudFront cookies with the contract's shape. The mock media server does not
	// check them; the URL is a plain MP4 at one fixed resolution, not an HLS playlist.
	expires := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	cookies := map[string]string{
		"CloudFront-Policy":      "bW9jay1wb2xpY3k",
		"CloudFront-Signature":   "bW9jay1zaWduYXR1cmU",
		"CloudFront-Key-Pair-Id": "KMOCKKEYPAIR",
	}
	for _, name := range []string{"CloudFront-Policy", "CloudFront-Signature", "CloudFront-Key-Pair-Id"} {
		http.SetCookie(w, &http.Cookie{
			Name: name, Value: cookies[name], Path: "/videos/" + v.ID + "/", MaxAge: 7200,
			Secure: true, HttpOnly: true, SameSite: http.SameSiteNoneMode, Partitioned: true,
		})
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"masterPlaylistUrl": s.baseURL(r) + "/media/videos/" + v.ID + "/video.mp4",
		"expiresAt":         expires,
		"cookies":           cookies,
	})
}

func (s *server) listMyVideos(w http.ResponseWriter, r *http.Request) {
	me := viewer(r)
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	var vs []*Video
	for _, v := range s.store.videos {
		if v.OwnerID == me && v.Status != StatusDeleted {
			vs = append(vs, v)
		}
	}
	sort.Slice(vs, func(i, j int) bool { return byCreatedDesc(vs[i], vs[j]) })
	items, next, ok := paginate(w, r, vs)
	if !ok {
		return
	}
	out := make([]ownVideo, len(items))
	for i, v := range items {
		thumb, dur, _ := s.mediaFields(r, v)
		out[i] = ownVideo{
			ID: v.ID, Title: v.Title, Status: v.Status, FailureReason: v.FailureReason,
			ThumbnailURL: thumb, DurationSeconds: dur, CreatedAt: v.CreatedAt, PublishedAt: v.PublishedAt,
			ViewCount: v.BaseViews, LikeCount: s.store.likeCount(v.ID), CommentCount: s.store.commentCount(v.ID),
		}
	}
	writeJSON(w, http.StatusOK, pageBody[ownVideo]{Items: out, NextCursor: next})
}

// ownedVideo loads a video for an owner-only route. Non-owners get 403 when they can see
// the video and 404 otherwise. Caller holds s.store.mu.
func (s *server) ownedVideo(w http.ResponseWriter, r *http.Request) (*Video, bool) {
	me := viewer(r)
	v, ok := s.store.videos[r.PathValue("videoId")]
	switch {
	case !ok || v.Status == StatusDeleted:
		notFound(w, r)
		return nil, false
	case v.OwnerID != me && v.Status == StatusReady:
		forbidden(w, r)
		return nil, false
	case v.OwnerID != me:
		notFound(w, r)
		return nil, false
	}
	return v, true
}

func (s *server) updateVideo(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title       *string `json:"title"`
		Description *string `json:"description"`
	}
	if !decodeStrict(w, r, &body) {
		return
	}
	if body.Title == nil && body.Description == nil {
		badRequest(w, r, "", "required", "title or description is required")
		return
	}
	if body.Title != nil {
		t := strings.TrimSpace(*body.Title)
		if !checkLength(w, r, "title", t, 1, 100) {
			return
		}
		body.Title = &t
	}
	if body.Description != nil && !checkLength(w, r, "description", *body.Description, 0, 5000) {
		return
	}

	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	v, ok := s.ownedVideo(w, r)
	if !ok {
		return
	}
	if body.Title != nil {
		v.Title = *body.Title
	}
	if body.Description != nil {
		v.Description = *body.Description
	}
	writeJSON(w, http.StatusOK, s.detail(r, v, true))
}

func (s *server) deleteVideo(w http.ResponseWriter, r *http.Request) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	v, ok := s.ownedVideo(w, r)
	if !ok {
		return
	}
	if v.Status == StatusPendingUpload || v.Status == StatusProcessing {
		conflict(w, r, "video cannot be deleted while it is "+v.Status)
		return
	}
	v.Status = StatusDeleted
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) moderate(w http.ResponseWriter, r *http.Request, from, to string) {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	v, ok := s.store.videos[r.PathValue("videoId")]
	if !ok {
		notFound(w, r)
		return
	}
	if v.Status != from {
		conflict(w, r, "video status is "+v.Status+", expected "+from)
		return
	}
	v.Status = to
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) hideVideo(w http.ResponseWriter, r *http.Request) {
	s.moderate(w, r, StatusReady, StatusHidden)
}

func (s *server) restoreVideo(w http.ResponseWriter, r *http.Request) {
	s.moderate(w, r, StatusHidden, StatusReady)
}
