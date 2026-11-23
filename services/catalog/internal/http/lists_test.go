package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/browse"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

func send(t *testing.T, h http.Handler, method, path, token, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var out map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("body is not JSON: %s", rec.Body.String())
		}
	}
	return rec, out
}

func readyVideo(id string, views int64) video.Video {
	pub := time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC)
	d := 30
	return video.Video{
		ID: id, OwnerID: userSub, OwnerName: "Diego", Title: "T", Status: video.StatusReady,
		DurationSeconds: &d, PublishedAt: &pub, CreatedAt: pub, ViewCount: views, LikeCount: 4, CommentCount: 2,
	}
}

func TestFeedCardsAndParams(t *testing.T) {
	b := &fakeBrowse{page: browse.Page{Items: []video.Video{readyVideo(readyID, 7)}, NextCursor: "abc"}}
	opts := testOptions()
	opts.Browse = b
	opts.MediaDomain = "d1.cloudfront.net"
	h := NewRouter(opts)

	rec, body := send(t, h, "GET", "/api/v1/feed?sort=most_viewed&cursor=abc&limit=7", "", "")
	if rec.Code != 200 {
		t.Fatalf("status = %d", rec.Code)
	}
	if b.sort != "most_viewed" || b.cursor != "abc" || b.limit != 7 {
		t.Errorf("params passed: %+v", b)
	}
	if body["nextCursor"] != "abc" {
		t.Errorf("nextCursor = %v", body["nextCursor"])
	}
	items := body["items"].([]any)
	card := items[0].(map[string]any)
	want := map[string]any{
		"id": readyID, "title": "T", "durationSeconds": float64(30), "viewCount": float64(7),
		"publishedAt": "2026-11-02T00:00:00Z", "thumbnailUrl": "https://d1.cloudfront.net/thumbnails/" + readyID + ".jpg",
	}
	for k, v := range want {
		if card[k] != v {
			t.Errorf("card %s = %v, want %v", k, card[k], v)
		}
	}
	for _, k := range []string{"description", "status", "likeCount"} {
		if _, ok := card[k]; ok {
			t.Errorf("card has %q", k)
		}
	}

	// Last page: nextCursor is null, items is [] (not null), and limit defaults to 20.
	b.page = browse.Page{}
	rec, body = send(t, h, "GET", "/api/v1/feed", "", "")
	if rec.Code != 200 || b.limit != 20 {
		t.Fatalf("status %d limit %d", rec.Code, b.limit)
	}
	if v, ok := body["nextCursor"]; !ok || v != nil {
		t.Errorf("nextCursor = %v, want null", v)
	}
	if items, ok := body["items"].([]any); !ok || len(items) != 0 {
		t.Errorf("items = %v, want []", body["items"])
	}
}

func TestListErrors(t *testing.T) {
	tests := []struct {
		name string
		path string
		err  error
		want int
		rule string
	}{
		{"non-integer limit", "/api/v1/feed?limit=ten", nil, 400, "range"},
		{"validation", "/api/v1/feed?sort=popular", &video.ValidationError{Field: "sort", Rule: "enum", Message: "m"}, 400, "enum"},
		{"bad cursor", "/api/v1/search?q=x&cursor=zz", &video.ValidationError{Field: "cursor", Rule: "format", Message: "m"}, 400, "format"},
		{"unknown creator", "/api/v1/creators/" + readyID + "/videos", video.ErrNotFound, 404, ""},
		{"database down", "/api/v1/search?q=x", fmt.Errorf("%w: boom", video.ErrUnavailable), 503, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := testOptions()
			opts.Browse = &fakeBrowse{err: tt.err}
			rec, body := send(t, NewRouter(opts), "GET", tt.path, "", "")
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.rule != "" {
				errs, _ := body["errors"].([]any)
				if len(errs) != 1 || errs[0].(map[string]any)["rule"] != tt.rule {
					t.Errorf("errors = %v, want rule %s", body["errors"], tt.rule)
				}
			}
		})
	}
}

func TestMyVideos(t *testing.T) {
	failed := readyVideo(failedID, 0)
	failed.Status = video.StatusFailed
	reason := "unsupported codec"
	failed.FailureReason = &reason
	s := &fakeStudio{page: browse.Page{Items: []video.Video{readyVideo(readyID, 3), failed}}}
	opts := testOptions()
	opts.Studio = s
	rec, body := send(t, NewRouter(opts), "GET", "/api/v1/me/videos", "user", "")
	if rec.Code != 200 || s.sub != userSub {
		t.Fatalf("status %d sub %q", rec.Code, s.sub)
	}
	if rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	items := body["items"].([]any)
	ready, fail := items[0].(map[string]any), items[1].(map[string]any)
	if ready["status"] != "READY" || ready["likeCount"] != float64(4) || ready["commentCount"] != float64(2) || ready["thumbnailUrl"] == nil {
		t.Errorf("ready item = %v", ready)
	}
	if fail["status"] != "FAILED" || fail["failureReason"] != reason || fail["thumbnailUrl"] != nil || fail["publishedAt"] != nil {
		t.Errorf("failed item = %v", fail)
	}
}

func TestUpdateVideo(t *testing.T) {
	path := "/api/v1/videos/" + readyID
	tests := []struct {
		name string
		body string
		err  error
		want int
	}{
		{"ok", `{"title":"Nuevo"}`, nil, 200},
		{"unknown field", `{"title":"x","status":"READY"}`, nil, 400},
		{"not json", `{title`, nil, 400},
		{"empty body", ``, nil, 400},
		{"wrong type", `{"title":5}`, nil, 400},
		{"service validation", `{}`, &video.ValidationError{Rule: "required", Message: "send title, description, or both"}, 400},
		{"not owner, visible", `{"title":"x"}`, video.ErrForbidden, 403},
		{"not owner, hidden or missing", `{"title":"x"}`, video.ErrNotFound, 404},
		{"database down", `{"title":"x"}`, video.ErrUnavailable, 503},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &fakeStudio{err: tt.err, detail: video.Detail{Video: readyVideo(readyID, 1), Owner: true}}
			opts := testOptions()
			opts.Studio = s
			rec, body := send(t, NewRouter(opts), "PATCH", path, "user", tt.body)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (%v)", rec.Code, tt.want, body)
			}
			if tt.want == 200 {
				if s.title == nil || *s.title != "Nuevo" || s.description != nil || s.sub != userSub || s.id != readyID {
					t.Errorf("service got %+v", s)
				}
				if body["status"] != "READY" {
					t.Errorf("response is not the owner view: %v", body)
				}
			}
		})
	}
}

func TestDeleteVideo(t *testing.T) {
	tests := []struct {
		err  error
		want int
	}{
		{nil, 204},
		{video.ErrForbidden, 403},
		{video.ErrNotFound, 404},
		{&video.ConflictError{Current: video.StatusProcessing}, 409},
		{errors.New("surprise"), 500},
	}
	for _, tt := range tests {
		opts := testOptions()
		opts.Studio = &fakeStudio{err: tt.err}
		rec, body := send(t, NewRouter(opts), "DELETE", "/api/v1/videos/"+readyID, "user", "")
		if rec.Code != tt.want {
			t.Errorf("err %v: status = %d, want %d", tt.err, rec.Code, tt.want)
		}
		if tt.want == 409 && body["detail"] != "Video is PROCESSING." {
			t.Errorf("409 detail = %v", body["detail"])
		}
	}
}

func TestModeration(t *testing.T) {
	for _, action := range []string{"hide", "restore"} {
		for _, tt := range []struct {
			err  error
			want int
		}{
			{nil, 204},
			{video.ErrNotFound, 404},
			{&video.ConflictError{Current: video.StatusDeleted}, 409},
			{video.ErrUnavailable, 503},
		} {
			m := &fakeModeration{err: tt.err}
			opts := testOptions()
			opts.Moderation = m
			rec, _ := send(t, NewRouter(opts), "POST", "/api/v1/ops/videos/"+readyID+"/"+action, "operator", "")
			if rec.Code != tt.want {
				t.Errorf("%s err %v: status = %d, want %d", action, tt.err, rec.Code, tt.want)
			}
			if m.action != action || m.operator != "22222222-2222-2222-2222-222222222222" || m.id != readyID {
				t.Errorf("%s: service got %+v", action, m)
			}
		}
	}
}
