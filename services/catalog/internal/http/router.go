// Package httpapi wires the Catalog routes (directory internal/http; the package is named
// httpapi so it does not shadow net/http).
package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/health"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/jwtauth"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/problem"
)

// Options configures the router. Verifier and every service are required.
type Options struct {
	OriginVerifyEnabled bool
	OriginVerifySecret  string
	ReadinessChecks     []health.Check

	Verifier jwtauth.Verifier
	// OperatorsGroup is the cognito:groups value required on /ops routes.
	OperatorsGroup string

	Videos     VideoService
	Playback   PlaybackService
	Browse     BrowseService
	Studio     StudioService
	Moderation ModerationService
	// MediaDomain is the CloudFront domain used for thumbnail URLs. Empty means the URLs
	// are relative (/thumbnails/{id}.jpg).
	MediaDomain string
}

// NewRouter returns the service handler.
//
// /healthz and /readyz sit outside /api/v1 and skip origin verification, because the ALB
// health check calls them directly (Interface Design §2.2).
func NewRouter(opts Options) http.Handler {
	if opts.OperatorsGroup == "" {
		opts.OperatorsGroup = "operators"
	}
	h := &handlers{
		videos:     opts.Videos,
		playback:   opts.Playback,
		browse:     opts.Browse,
		studio:     opts.Studio,
		moderation: opts.Moderation,
		media:      mediaURLs{domain: opts.MediaDomain},
	}

	r := chi.NewRouter()
	r.Use(middleware.Correlation)
	r.Use(middleware.RequestLog("/healthz", "/readyz"))
	r.Use(middleware.Recover)

	r.Get("/healthz", health.Liveness())
	r.Get("/readyz", health.Readiness(2*time.Second, opts.ReadinessChecks...))

	r.Route("/api/v1", func(api chi.Router) {
		api.Use(middleware.OriginVerify(opts.OriginVerifyEnabled, opts.OriginVerifySecret))

		// Public routes: READY videos only.
		api.Group(func(pub chi.Router) {
			pub.Get("/feed", h.getFeed)
			pub.Get("/search", h.searchVideos)
			pub.Get("/creators/{userId}/videos", h.listCreatorVideos)
		})

		// Optional token: guests see READY videos only, the owner sees more. A token that
		// is sent but invalid gets 401.
		api.Group(func(opt chi.Router) {
			opt.Use(jwtauth.Optional(opts.Verifier))
			opt.Get("/videos/{videoId}", h.getVideo)
			opt.Get("/videos/{videoId}/playback", h.getPlayback)
		})

		// JWT routes: the service re-validates the token (SEC-01). Ownership comes from
		// the token sub only (SEC-02).
		api.Group(func(priv chi.Router) {
			priv.Use(jwtauth.Require(opts.Verifier))
			priv.Get("/me/videos", h.listMyVideos)
			priv.Patch("/videos/{videoId}", h.updateVideo)
			priv.Delete("/videos/{videoId}", h.deleteVideo)
		})

		// Operator routes: JWT plus membership in the operators group.
		api.Group(func(ops chi.Router) {
			ops.Use(jwtauth.Require(opts.Verifier))
			ops.Use(jwtauth.RequireGroup(opts.OperatorsGroup))
			ops.Post("/ops/videos/{videoId}/hide", h.hideVideo)
			ops.Post("/ops/videos/{videoId}/restore", h.restoreVideo)
		})
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		problem.Write(w, r, http.StatusNotFound, problem.NotFound, "")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		problem.Write(w, r, http.StatusMethodNotAllowed, problem.NotFound, "method not allowed")
	})
	return r
}
