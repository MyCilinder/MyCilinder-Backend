package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/jwtauth"
)

const (
	userSub = "11111111-1111-4111-8111-111111111111"
	opSub   = "22222222-2222-4222-8222-222222222222"
)

type fakeVerifier struct{}

func (fakeVerifier) Verify(_ context.Context, raw string) (jwtauth.Claims, error) {
	switch raw {
	case "user":
		return jwtauth.Claims{Subject: userSub}, nil
	case "op":
		return jwtauth.Claims{Subject: opSub, Groups: []string{"operators"}}, nil
	}
	return jwtauth.Claims{}, jwtauth.ErrInvalidToken
}

type client struct {
	t *testing.T
	h http.Handler
}

func (c client) do(method, path, token, body string, hdr ...string) (int, map[string]any, http.Header) {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out, rec.Header()
}

func newTestClient(t *testing.T, delay time.Duration) client {
	cfg := config{ProcessingDelay: delay, SeedPerUser: true, MediaDir: t.TempDir()}
	return client{t, newServer(cfg, NewStore(), LoadMedia(cfg.MediaDir), fakeVerifier{}).routes()}
}

func items(body map[string]any) []any { a, _ := body["items"].([]any); return a }

func TestBrowseAndWatch(t *testing.T) {
	c := newTestClient(t, 0)

	code, feed, _ := c.do("GET", "/api/v1/feed?limit=5", "", "")
	if code != 200 || len(items(feed)) != 5 || feed["nextCursor"] == nil {
		t.Fatalf("feed: %d %v", code, feed)
	}
	code, page2, _ := c.do("GET", "/api/v1/feed?limit=5&cursor="+feed["nextCursor"].(string), "", "")
	if code != 200 || len(items(page2)) != 5 {
		t.Fatalf("feed page 2: %d", code)
	}
	if code, _, _ := c.do("GET", "/api/v1/feed?limit=99", "", ""); code != 400 {
		t.Fatalf("limit 99: want 400, got %d", code)
	}

	code, res, _ := c.do("GET", "/api/v1/search?q=recorridos%20campus", "", "")
	if code != 200 || len(items(res)) == 0 || !strings.Contains(items(res)[0].(map[string]any)["title"].(string), "campus") {
		t.Fatalf("search: %d %v", code, res)
	}

	card := items(feed)[0].(map[string]any)
	id := card["id"].(string)
	if code, d, _ := c.do("GET", "/api/v1/videos/"+id, "", ""); code != 200 || d["status"] != nil {
		t.Fatalf("detail as guest: %d %v", code, d)
	}
	code, pb, hdr := c.do("GET", "/api/v1/videos/"+id+"/playback", "", "")
	if code != 200 || len(hdr.Values("Set-Cookie")) != 3 || !strings.HasSuffix(pb["masterPlaylistUrl"].(string), "/video.mp4") {
		t.Fatalf("playback: %d %v", code, pb)
	}
	creator := card["creator"].(map[string]any)["id"].(string)
	if code, _, _ := c.do("GET", "/api/v1/creators/"+creator+"/videos", "", ""); code != 200 {
		t.Fatalf("creator: %d", code)
	}
	if code, _, _ := c.do("GET", "/api/v1/videos/"+id, "garbage", ""); code != 401 {
		t.Fatalf("invalid token: want 401, got %d", code)
	}
}

func TestInteractions(t *testing.T) {
	c := newTestClient(t, 0)
	_, feed, _ := c.do("GET", "/api/v1/feed", "", "")
	id := items(feed)[0].(map[string]any)["id"].(string)

	_, before, _ := c.do("GET", "/api/v1/videos/"+id+"/stats", "user", "")
	if code, like, _ := c.do("PUT", "/api/v1/videos/"+id+"/like", "user", ""); code != 200 || like["liked"] != true {
		t.Fatalf("like: %d %v", code, like)
	}
	c.do("PUT", "/api/v1/videos/"+id+"/like", "user", "") // idempotent
	_, after, _ := c.do("GET", "/api/v1/videos/"+id+"/stats", "user", "")
	if after["likeCount"].(float64) != before["likeCount"].(float64)+1 || after["likedByMe"] != true {
		t.Fatalf("stats after like: %v -> %v", before, after)
	}

	if code, cm, _ := c.do("POST", "/api/v1/videos/"+id+"/comments", "user", `{"body":"  ¡Hola!  "}`); code != 201 || cm["body"] != "¡Hola!" {
		t.Fatalf("comment: %d %v", code, cm)
	}
	if code, _, _ := c.do("POST", "/api/v1/videos/"+id+"/comments", "user", `{"body":"x","extra":1}`); code != 400 {
		t.Fatalf("unknown field: want 400, got %d", code)
	}
	_, list, _ := c.do("GET", "/api/v1/videos/"+id+"/comments", "", "")
	if items(list)[0].(map[string]any)["body"] != "¡Hola!" {
		t.Fatalf("newest comment first: %v", list)
	}

	if code, _, _ := c.do("POST", "/api/v1/videos/"+id+"/views", "", ""); code != 400 {
		t.Fatalf("guest view without session: want 400, got %d", code)
	}
	sid := "33333333-3333-4333-8333-333333333333"
	c.do("POST", "/api/v1/videos/"+id+"/views", "", "", "X-Session-Id", sid)
	c.do("POST", "/api/v1/videos/"+id+"/views", "", "", "X-Session-Id", sid)
	_, s2, _ := c.do("GET", "/api/v1/videos/"+id+"/stats", "", "")
	if s2["viewCount"].(float64) != after["viewCount"].(float64)+1 {
		t.Fatalf("views not deduplicated: %v -> %v", after["viewCount"], s2["viewCount"])
	}

	body := `{"targetType":"VIDEO","targetId":"` + id + `","reason":"SPAM"}`
	if code, _, _ := c.do("POST", "/api/v1/reports", "user", body); code != 201 {
		t.Fatalf("report: %d", code)
	}
	if code, _, _ := c.do("POST", "/api/v1/reports", "user", body); code != 409 {
		t.Fatalf("duplicate report: want 409, got %d", code)
	}
	if code, _, _ := c.do("GET", "/api/v1/ops/reports", "user", ""); code != 403 {
		t.Fatalf("ops as user: want 403, got %d", code)
	}
	if code, rl, _ := c.do("GET", "/api/v1/ops/reports", "op", ""); code != 200 || len(items(rl)) != 1 {
		t.Fatalf("ops reports: %d %v", code, rl)
	}
}

func TestStudioAndModeration(t *testing.T) {
	c := newTestClient(t, 0)
	code, mine, _ := c.do("GET", "/api/v1/me/videos", "user", "")
	if code != 200 || len(items(mine)) != 5 {
		t.Fatalf("me/videos: %d %v", code, mine)
	}
	statuses := map[string]string{}
	for _, it := range items(mine) {
		m := it.(map[string]any)
		statuses[m["status"].(string)] = m["id"].(string)
	}
	failed := statuses[StatusFailed]
	if code, d, _ := c.do("GET", "/api/v1/videos/"+failed, "", ""); code != 404 {
		t.Fatalf("failed as guest: want 404, got %d %v", code, d)
	}
	if code, d, _ := c.do("GET", "/api/v1/videos/"+failed, "user", ""); code != 200 || d["failureReason"] == nil {
		t.Fatalf("failed as owner: %d %v", code, d)
	}
	if code, _, _ := c.do("GET", "/api/v1/videos/"+failed+"/playback", "user", ""); code != 409 {
		t.Fatalf("playback of FAILED for owner: want 409, got %d", code)
	}

	ready := statuses[StatusReady]
	if code, d, _ := c.do("PATCH", "/api/v1/videos/"+ready, "user", `{"title":" Nuevo título "}`); code != 200 || d["title"] != "Nuevo título" {
		t.Fatalf("patch: %d %v", code, d)
	}
	if code, _, _ := c.do("PATCH", "/api/v1/videos/"+ready, "op", `{"title":"x"}`); code != 403 {
		t.Fatalf("patch by non-owner: want 403, got %d", code)
	}
	if code, _, _ := c.do("DELETE", "/api/v1/videos/"+statuses[StatusProcessing], "user", ""); code != 409 {
		t.Fatalf("delete PROCESSING: want 409, got %d", code)
	}

	if code, _, _ := c.do("POST", "/api/v1/ops/videos/"+ready+"/hide", "op", ""); code != 204 {
		t.Fatalf("hide: %d", code)
	}
	if code, _, _ := c.do("GET", "/api/v1/videos/"+ready, "", ""); code != 404 {
		t.Fatalf("hidden as guest: want 404, got %d", code)
	}
	if code, _, _ := c.do("POST", "/api/v1/ops/videos/"+ready+"/hide", "op", ""); code != 409 {
		t.Fatalf("hide twice: want 409, got %d", code)
	}
	c.do("POST", "/api/v1/ops/videos/"+ready+"/restore", "op", "")

	if code, _, _ := c.do("DELETE", "/api/v1/videos/"+ready, "user", ""); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	if code, _, _ := c.do("DELETE", "/api/v1/videos/"+ready, "user", ""); code != 404 {
		t.Fatalf("delete twice: want 404, got %d", code)
	}
}

func TestUploadFlow(t *testing.T) {
	c := newTestClient(t, 50*time.Millisecond)
	body := `{"title":"Mi video","fileName":"a.mp4","contentType":"video/mp4","sizeBytes":2048}`
	if code, _, _ := c.do("POST", "/api/v1/uploads", "user", body); code != 400 {
		t.Fatalf("missing Idempotency-Key: want 400, got %d", code)
	}
	code, up, hdr := c.do("POST", "/api/v1/uploads", "user", body, "Idempotency-Key", "k1")
	if code != 201 || up["method"] != "PUT" || hdr.Get("Location") == "" {
		t.Fatalf("create upload: %d %v", code, up)
	}
	id := up["videoId"].(string)
	if code, again, _ := c.do("POST", "/api/v1/uploads", "user", body, "Idempotency-Key", "k1"); code != 201 || again["videoId"] != id {
		t.Fatalf("idempotent retry: %d %v", code, again)
	}
	if code, _, _ := c.do("POST", "/api/v1/uploads", "user", strings.Replace(body, "Mi video", "Otro", 1), "Idempotency-Key", "k1"); code != 409 {
		t.Fatalf("key reuse with another body: want 409, got %d", code)
	}
	if code, _, _ := c.do("POST", "/api/v1/uploads", "user", strings.Replace(body, "video/mp4", "video/avi", 1), "Idempotency-Key", "k2"); code != 422 {
		t.Fatalf("bad content type: want 422, got %d", code)
	}

	// The presigned URL points at the mock; the path is all the handler needs.
	u := up["uploadUrl"].(string)
	path := u[strings.Index(u, "/mock-s3/"):]
	if code, _, h := c.do("PUT", path, "", strings.Repeat("x", 2048)); code != 200 || h.Get("ETag") == "" {
		t.Fatalf("PUT object: %d", code)
	}
	if _, d, _ := c.do("GET", "/api/v1/videos/"+id, "user", ""); d["status"] != StatusProcessing {
		t.Fatalf("after PUT: %v", d["status"])
	}
	time.Sleep(150 * time.Millisecond)
	if _, d, _ := c.do("GET", "/api/v1/videos/"+id, "", ""); d["publishedAt"] == nil {
		t.Fatalf("after processing delay: %v", d)
	}

	big := `{"title":"Grande","fileName":"b.mp4","contentType":"video/mp4","sizeBytes":209715200}`
	code, mp, _ := c.do("POST", "/api/v1/uploads", "user", big, "Idempotency-Key", "k3")
	if code != 201 || mp["method"] != "MULTIPART" || len(mp["parts"].([]any)) != 4 {
		t.Fatalf("multipart: %d %v", code, mp)
	}
	parts := `{"parts":[{"partNumber":1,"eTag":"a"},{"partNumber":2,"eTag":"b"},{"partNumber":3,"eTag":"c"},{"partNumber":4,"eTag":"d"}]}`
	if code, _, _ := c.do("POST", "/api/v1/uploads/"+mp["videoId"].(string)+"/complete", "user", parts); code != 204 {
		t.Fatalf("complete: %d", code)
	}
}
