package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/jwtauth"
)

type config struct {
	Port            string
	CognitoRegion   string
	CognitoPoolID   string
	CognitoClientID string
	OperatorsGroup  string
	MediaDir        string
	MediaBaseURL    string
	AuthURL         string
	ProcessingDelay time.Duration
	SeedPerUser     bool
}

type server struct {
	cfg      config
	store    *Store
	media    *Media
	verifier jwtauth.Verifier
	names    *nameResolver
}

func newServer(cfg config, store *Store, media *Media, v jwtauth.Verifier) *server {
	if cfg.OperatorsGroup == "" {
		cfg.OperatorsGroup = "operators"
	}
	return &server{cfg: cfg, store: store, media: media, verifier: v, names: newNameResolver(cfg.AuthURL)}
}

func (s *server) routes() http.Handler {
	opt := func(h http.HandlerFunc) http.Handler { return jwtauth.Optional(s.verifier)(s.withUser(h)) }
	req := func(h http.HandlerFunc) http.Handler { return jwtauth.Require(s.verifier)(s.withUser(h)) }
	ops := func(h http.HandlerFunc) http.Handler {
		return jwtauth.Require(s.verifier)(jwtauth.RequireGroup(s.cfg.OperatorsGroup)(s.withUser(h)))
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })

	const api = "/api/v1"
	// Catalog.
	mux.HandleFunc("GET "+api+"/feed", s.getFeed)
	mux.HandleFunc("GET "+api+"/search", s.searchVideos)
	mux.HandleFunc("GET "+api+"/creators/{userId}/videos", s.listCreatorVideos)
	mux.Handle("GET "+api+"/videos/{videoId}", opt(s.getVideo))
	mux.Handle("GET "+api+"/videos/{videoId}/playback", opt(s.getPlayback))
	mux.Handle("PATCH "+api+"/videos/{videoId}", req(s.updateVideo))
	mux.Handle("DELETE "+api+"/videos/{videoId}", req(s.deleteVideo))
	mux.Handle("GET "+api+"/me/videos", req(s.listMyVideos))
	mux.Handle("POST "+api+"/ops/videos/{videoId}/hide", ops(s.hideVideo))
	mux.Handle("POST "+api+"/ops/videos/{videoId}/restore", ops(s.restoreVideo))
	// Interactions.
	mux.Handle("GET "+api+"/videos/{videoId}/stats", opt(s.getStats))
	mux.HandleFunc("GET "+api+"/videos/{videoId}/comments", s.listComments)
	mux.Handle("POST "+api+"/videos/{videoId}/comments", req(s.createComment))
	mux.Handle("PUT "+api+"/videos/{videoId}/like", req(s.likeVideo))
	mux.Handle("DELETE "+api+"/videos/{videoId}/like", req(s.unlikeVideo))
	mux.Handle("POST "+api+"/videos/{videoId}/views", opt(s.recordView))
	mux.Handle("POST "+api+"/reports", req(s.createReport))
	mux.Handle("GET "+api+"/ops/reports", ops(s.listReports))
	mux.Handle("PATCH "+api+"/ops/reports/{reportId}", ops(s.resolveReport))
	mux.Handle("POST "+api+"/ops/comments/{commentId}/hide", ops(s.hideComment))
	// Upload.
	mux.Handle("POST "+api+"/uploads", req(s.createUpload))
	mux.Handle("POST "+api+"/uploads/{videoId}/urls", req(s.refreshUploadURLs))
	mux.Handle("POST "+api+"/uploads/{videoId}/complete", req(s.completeUpload))
	// Stand-ins for S3 (presigned PUT) and CloudFront (media).
	mux.HandleFunc("PUT /mock-s3/raw/{videoId}/source", s.putObject)
	mux.HandleFunc("GET /media/videos/{videoId}/video.mp4", func(w http.ResponseWriter, r *http.Request) {
		s.media.serveVideo(w, r, r.PathValue("videoId"))
	})
	mux.HandleFunc("GET /media/thumbnails/{file}", func(w http.ResponseWriter, r *http.Request) {
		id, ok := cutSuffix(r.PathValue("file"), ".jpg")
		if !ok {
			http.NotFound(w, r)
			return
		}
		s.media.serveThumb(w, r, id)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { notFound(w, r) })

	var h http.Handler = mux
	h = middleware.Recover(h)
	h = middleware.RequestLog("/healthz", "/readyz")(h)
	h = middleware.Correlation(h)
	return h
}

func cutSuffix(s, suffix string) (string, bool) {
	if len(s) > len(suffix) && s[len(s)-len(suffix):] == suffix {
		return s[:len(s)-len(suffix)], true
	}
	return "", false
}

// viewer returns the caller's sub, or "" for guests.
func viewer(r *http.Request) string {
	c, ok := jwtauth.FromContext(r.Context())
	if !ok {
		return ""
	}
	return c.Subject
}

// withUser registers a signed-in caller in the store on first sight: it looks up the
// display name from the auth facade and, when enabled, gives them starter videos.
func (s *server) withUser(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, ok := jwtauth.FromContext(r.Context()); ok {
			name := s.names.resolve(r.Context(), c.Subject, c.Token)
			s.store.mu.Lock()
			u, exists := s.store.users[c.Subject]
			if !exists {
				u = &User{ID: c.Subject}
				s.store.users[c.Subject] = u
			}
			if name != "" {
				u.DisplayName = name
			} else if u.DisplayName == "" {
				u.DisplayName = "Usuario " + c.Subject[:min(8, len(c.Subject))]
			}
			if s.cfg.SeedPerUser {
				s.store.seedForUser(c.Subject, time.Now().UTC())
			}
			s.store.mu.Unlock()
		}
		next(w, r)
	})
}

// nameResolver reads display names from the real auth facade (GET /api/v1/me), because
// Cognito access tokens do not carry the name.
type nameResolver struct {
	url    string
	client *http.Client
	mu     sync.Mutex
	cache  map[string]string
}

func newNameResolver(authURL string) *nameResolver {
	return &nameResolver{url: authURL, client: &http.Client{Timeout: 2 * time.Second}, cache: map[string]string{}}
}

func (n *nameResolver) resolve(ctx context.Context, sub, token string) string {
	n.mu.Lock()
	name, ok := n.cache[sub]
	n.mu.Unlock()
	if ok || n.url == "" || token == "" {
		return name
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, n.url+"/api/v1/me", nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := n.client.Do(req)
	if err != nil {
		middleware.Logger(ctx).Warn("auth /me lookup failed", "err", err)
		return ""
	}
	defer resp.Body.Close()
	var body struct {
		DisplayName string `json:"displayName"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&body) != nil || body.DisplayName == "" {
		return ""
	}
	n.mu.Lock()
	n.cache[sub] = body.DisplayName
	n.mu.Unlock()
	return body.DisplayName
}
