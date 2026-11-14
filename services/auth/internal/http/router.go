// Package httpapi wires the auth facade routes (directory internal/http; the package is
// named httpapi so it does not shadow net/http).
package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/health"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/problem"
)

// Options configures the router.
type Options struct {
	OriginVerifyEnabled bool
	OriginVerifySecret  string
	ReadinessChecks     []health.Check
}

// NewRouter returns the service handler.
//
// /healthz and /readyz sit outside /api/v1 and skip origin verification, because the ALB
// health check calls them directly (Interface Design §2.2).
func NewRouter(opts Options) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Correlation)
	r.Use(middleware.RequestLog("/healthz", "/readyz"))
	r.Use(middleware.Recover)

	r.Get("/healthz", health.Liveness())
	r.Get("/readyz", health.Readiness(2*time.Second, opts.ReadinessChecks...))

	r.Route("/api/v1", func(api chi.Router) {
		api.Use(middleware.OriginVerify(opts.OriginVerifyEnabled, opts.OriginVerifySecret))

		api.Post("/auth/register", notImplemented)
		api.Post("/auth/confirm", notImplemented)
		api.Post("/auth/resend-code", notImplemented)
		api.Post("/auth/login", notImplemented)
		api.Post("/auth/refresh", notImplemented)
		api.Post("/auth/logout", notImplemented)
		api.Get("/me", notImplemented)
		api.Patch("/me", notImplemented)
	})

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		problem.Write(w, r, http.StatusNotFound, problem.NotFound, "")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		problem.Write(w, r, http.StatusMethodNotAllowed, problem.NotFound, "method not allowed")
	})
	return r
}

func notImplemented(w http.ResponseWriter, r *http.Request) {
	problem.Write(w, r, http.StatusNotImplemented, problem.NotImplemented, "endpoint not implemented yet")
}
