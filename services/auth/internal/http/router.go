// Package httpapi wires the auth facade routes (directory internal/http; the package is
// named httpapi so it does not shadow net/http).
package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/health"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/jwtauth"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/ratelimit"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/problem"
)

// Options configures the router. Auth, Profile, and Verifier are required.
type Options struct {
	OriginVerifyEnabled bool
	OriginVerifySecret  string
	ReadinessChecks     []health.Check

	Auth     AuthService
	Profile  ProfileService
	Verifier jwtauth.Verifier

	// PublicLimit is applied to the public /auth/* routes (per-IP request limit).
	// Nil disables it.
	PublicLimit func(http.Handler) http.Handler
	// ClientIP resolves the client address for login throttling. Nil uses the TCP peer.
	ClientIP func(*http.Request) string
}

// NewRouter returns the service handler.
//
// /healthz and /readyz sit outside /api/v1 and skip origin verification, because the ALB
// health check calls them directly (Interface Design §2.2).
func NewRouter(opts Options) http.Handler {
	if opts.ClientIP == nil {
		opts.ClientIP = ratelimit.ClientIP(0)
	}
	if opts.PublicLimit == nil {
		opts.PublicLimit = func(next http.Handler) http.Handler { return next }
	}
	h := &handlers{auth: opts.Auth, profile: opts.Profile, clientIP: opts.ClientIP}

	r := chi.NewRouter()
	r.Use(middleware.Correlation)
	r.Use(middleware.RequestLog("/healthz", "/readyz"))
	r.Use(middleware.Recover)

	r.Get("/healthz", health.Liveness())
	r.Get("/readyz", health.Readiness(2*time.Second, opts.ReadinessChecks...))

	r.Route("/api/v1", func(api chi.Router) {
		api.Use(middleware.OriginVerify(opts.OriginVerifyEnabled, opts.OriginVerifySecret))

		// Public routes: no token, throttled per IP (SEC-09). Login is also throttled
		// per email and IP on failures, inside the auth service.
		api.Group(func(pub chi.Router) {
			pub.Use(opts.PublicLimit)
			pub.Post("/auth/register", h.register)
			pub.Post("/auth/confirm", h.confirm)
			pub.Post("/auth/resend-code", h.resendCode)
			pub.Post("/auth/login", h.login)
			pub.Post("/auth/refresh", h.refresh)
		})

		// JWT routes: the service re-validates the token (SEC-01).
		api.Group(func(priv chi.Router) {
			priv.Use(jwtauth.Require(opts.Verifier))
			priv.Post("/auth/logout", h.logout)
			priv.Get("/me", h.getMe)
			priv.Patch("/me", h.patchMe)
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
