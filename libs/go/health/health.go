// Package health provides the /healthz (liveness) and /readyz (readiness) handlers that
// every HTTP service exposes (Interface Design §2.2, REL-11).
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// Check is a named dependency probe, for example a database ping.
type Check struct {
	Name string
	Fn   func(ctx context.Context) error
}

type response struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

// Liveness reports that the process is up. It never touches dependencies, so a database
// outage does not make the container restart.
func Liveness() http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		write(w, http.StatusOK, response{Status: "ok"})
	}
}

// Readiness runs every check concurrently with the given timeout. It returns 200 when all
// pass and 503 otherwise. Error details are not exposed; only "ok" or "fail" per check.
func Readiness(timeout time.Duration, checks ...Check) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()

		results := make(map[string]string, len(checks))
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, c := range checks {
			wg.Add(1)
			go func(c Check) {
				defer wg.Done()
				status := "ok"
				if err := c.Fn(ctx); err != nil {
					status = "fail"
				}
				mu.Lock()
				results[c.Name] = status
				mu.Unlock()
			}(c)
		}
		wg.Wait()

		resp := response{Status: "ok", Checks: results}
		code := http.StatusOK
		for _, s := range results {
			if s != "ok" {
				resp.Status, code = "fail", http.StatusServiceUnavailable
				break
			}
		}
		write(w, code, resp)
	}
}

func write(w http.ResponseWriter, code int, body response) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
