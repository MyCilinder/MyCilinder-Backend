package httpapi

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/cfsign"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/playback"
	"github.com/MyCilinder/MyCilinder-Backend/services/catalog/internal/video"
)

func TestPlayback(t *testing.T) {
	c := func(name, value string) *http.Cookie {
		return &http.Cookie{Name: name, Value: value, Path: "/videos/" + readyID + "/", MaxAge: 7200,
			Secure: true, HttpOnly: true, SameSite: http.SameSiteNoneMode, Partitioned: true}
	}
	fp := &fakePlayback{grant: cfsign.Grant{
		Cookies:           []*http.Cookie{c("CloudFront-Policy", "p"), c("CloudFront-Signature", "s"), c("CloudFront-Key-Pair-Id", "K1")},
		ExpiresAt:         time.Date(2026, 11, 22, 22, 0, 0, 0, time.UTC),
		MasterPlaylistURL: "https://d1.cloudfront.net/videos/" + readyID + "/master.m3u8",
	}}
	opts := testOptions()
	opts.Playback = fp
	rec, body := send(t, NewRouter(opts), "GET", "/api/v1/videos/"+readyID+"/playback", "user", "")
	if rec.Code != 200 || fp.viewer != userSub {
		t.Fatalf("status %d viewer %q", rec.Code, fp.viewer)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	lines := rec.Header().Values("Set-Cookie")
	if len(lines) != 3 {
		t.Fatalf("%d Set-Cookie lines", len(lines))
	}
	for _, l := range lines {
		for _, part := range []string{"Path=/videos/" + readyID + "/", "Max-Age=7200", "Secure", "HttpOnly", "SameSite=None", "Partitioned"} {
			if !strings.Contains(l, part) {
				t.Errorf("%q lacks %s", l, part)
			}
		}
	}
	cookies, _ := body["cookies"].(map[string]any)
	if len(cookies) != 3 || cookies["CloudFront-Key-Pair-Id"] != "K1" || cookies["CloudFront-Policy"] != "p" {
		t.Errorf("cookies = %v", body["cookies"])
	}
	if body["expiresAt"] != "2026-11-22T22:00:00Z" || body["masterPlaylistUrl"] != fp.grant.MasterPlaylistURL {
		t.Errorf("body = %v", body)
	}

	for _, tt := range []struct {
		err  error
		want int
	}{
		{video.ErrNotFound, 404},
		{&video.ConflictError{Current: video.StatusProcessing}, 409},
		{playback.ErrNotConfigured, 503},
		{video.ErrUnavailable, 503},
	} {
		opts.Playback = &fakePlayback{err: tt.err}
		rec, _ := send(t, NewRouter(opts), "GET", "/api/v1/videos/"+readyID+"/playback", "", "")
		if rec.Code != tt.want || len(rec.Header().Values("Set-Cookie")) != 0 {
			t.Errorf("err %v: status %d, cookies %d", tt.err, rec.Code, len(rec.Header().Values("Set-Cookie")))
		}
	}
}
