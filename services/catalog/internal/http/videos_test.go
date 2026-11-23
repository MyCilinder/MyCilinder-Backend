package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

const (
	userSub  = "11111111-1111-1111-1111-111111111111" // fakeVerifier "user"
	readyID  = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	failedID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	brokenID = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
)

// fakeVideos runs the real visibility rules over an in-memory set of videos.
type fakeVideos struct {
	videos map[string]video.Video
}

func (f *fakeVideos) Get(ctx context.Context, id, viewer string) (video.Detail, error) {
	return video.NewService(f).Get(ctx, id, viewer)
}

func (f *fakeVideos) GetByID(_ context.Context, id string) (video.Video, error) {
	if id == brokenID {
		return video.Video{}, errors.New("connection refused")
	}
	v, ok := f.videos[id]
	if !ok {
		return video.Video{}, video.ErrNotFound
	}
	return v, nil
}

func detailFixtures() *fakeVideos {
	created := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	published := created.Add(5 * time.Minute)
	dur := 312
	reason := "unsupported codec"
	return &fakeVideos{videos: map[string]video.Video{
		readyID: {
			ID: readyID, OwnerID: userSub, OwnerName: "Diego", Title: "Recorrido", Description: "Campus",
			Status: video.StatusReady, DurationSeconds: &dur, Renditions: []string{"360p", "720p"},
			CreatedAt: created, PublishedAt: &published, ViewCount: 99, LikeCount: 5,
		},
		failedID: {
			ID: failedID, OwnerID: userSub, OwnerName: "Diego", Title: "Roto", Status: video.StatusFailed,
			FailureReason: &reason, DurationSeconds: &dur, Renditions: []string{"360p"},
			CreatedAt: created, PublishedAt: &published,
		},
	}}
}

func detailRouter(domain string) http.Handler {
	opts := testOptions()
	opts.Videos = detailFixtures()
	opts.MediaDomain = domain
	return NewRouter(opts)
}

func getJSON(t *testing.T, h http.Handler, path, token string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v: %s", err, rec.Body.String())
	}
	return rec, body
}

func TestGetVideoGuestReady(t *testing.T) {
	h := detailRouter("d123.cloudfront.net")
	rec, body := getJSON(t, h, "/api/v1/videos/"+readyID, "")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	want := map[string]any{
		"id": readyID, "title": "Recorrido", "description": "Campus",
		"thumbnailUrl":    "https://d123.cloudfront.net/thumbnails/" + readyID + ".jpg",
		"durationSeconds": float64(312), "viewCount": float64(99),
		"publishedAt": "2026-11-01T10:05:00Z", "createdAt": "2026-11-01T10:00:00Z",
	}
	for k, v := range want {
		if body[k] != v {
			t.Errorf("%s = %v, want %v", k, body[k], v)
		}
	}
	if c, _ := body["creator"].(map[string]any); c["id"] != userSub || c["displayName"] != "Diego" {
		t.Errorf("creator = %v", body["creator"])
	}
	if r, _ := body["renditions"].([]any); len(r) != 2 {
		t.Errorf("renditions = %v", body["renditions"])
	}
	// Owner-only fields and counts that belong to Interactions are absent.
	for _, k := range []string{"status", "failureReason", "likeCount", "commentCount"} {
		if _, ok := body[k]; ok {
			t.Errorf("guest response has %q", k)
		}
	}
	if rec.Header().Get("Cache-Control") != "" {
		t.Errorf("Cache-Control = %q, want unset for the public view", rec.Header().Get("Cache-Control"))
	}
}

func TestGetVideoOwnerFailed(t *testing.T) {
	h := detailRouter("d123.cloudfront.net")

	// Guests and other users cannot see a FAILED video.
	if rec, _ := getJSON(t, h, "/api/v1/videos/"+failedID, ""); rec.Code != 404 {
		t.Errorf("guest status = %d, want 404", rec.Code)
	}
	if rec, _ := getJSON(t, h, "/api/v1/videos/"+failedID, "operator"); rec.Code != 404 {
		t.Errorf("other user status = %d, want 404", rec.Code)
	}

	rec, body := getJSON(t, h, "/api/v1/videos/"+failedID, "user")
	if rec.Code != 200 {
		t.Fatalf("owner status = %d", rec.Code)
	}
	if body["status"] != "FAILED" || body["failureReason"] != "unsupported codec" {
		t.Errorf("status/failureReason = %v / %v", body["status"], body["failureReason"])
	}
	// Media fields stay empty until READY, even if the row has values.
	for _, k := range []string{"thumbnailUrl", "durationSeconds", "publishedAt"} {
		if v, ok := body[k]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want null", k, v, ok)
		}
	}
	if r, ok := body["renditions"].([]any); !ok || len(r) != 0 {
		t.Errorf("renditions = %v, want []", body["renditions"])
	}
	if rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
}

func TestGetVideoOwnerReadyHasNullFailureReason(t *testing.T) {
	h := detailRouter("")
	rec, body := getJSON(t, h, "/api/v1/videos/"+readyID, "user")
	if rec.Code != 200 || body["status"] != "READY" {
		t.Fatalf("status = %d body = %v", rec.Code, body)
	}
	if v, ok := body["failureReason"]; !ok || v != nil {
		t.Errorf("failureReason = %v (present %v), want null", v, ok)
	}
	// No MediaDomain configured: the thumbnail URL is relative.
	if v := body["thumbnailUrl"]; v != "/thumbnails/"+readyID+".jpg" {
		t.Errorf("thumbnailUrl = %v, want relative path", v)
	}
}

func TestGetVideoErrors(t *testing.T) {
	h := detailRouter("")
	tests := []struct {
		path string
		want int
		typ  string
	}{
		{"/api/v1/videos/not-a-uuid", 404, "/problems/not-found"},
		{"/api/v1/videos/dddddddd-dddd-4ddd-8ddd-dddddddddddd", 404, "/problems/not-found"},
		{"/api/v1/videos/" + brokenID, 503, "/problems/unavailable"},
	}
	for _, tt := range tests {
		rec, body := getJSON(t, h, tt.path, "")
		if rec.Code != tt.want || body["type"] != tt.typ {
			t.Errorf("%s: %d %v, want %d %s", tt.path, rec.Code, body["type"], tt.want, tt.typ)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("%s: Content-Type = %q", tt.path, ct)
		}
	}
}
