// Package middleware holds the HTTP middleware shared by the Go services.
package middleware

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/problem"
)

// CorrelationHeader carries the request correlation id (MAINT-05).
const CorrelationHeader = "X-Correlation-Id"

type loggerKey struct{}

// Correlation reads X-Correlation-Id or generates one, stores it in the request context
// (see problem.CorrelationID) together with a request-scoped logger (see Logger), and
// echoes it in the response header.
//
// Incoming values are accepted only if they are short and contain safe characters, so a
// client cannot inject arbitrary text into logs.
func Correlation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(CorrelationHeader)
		if !validCorrelationID(id) {
			id = newUUID()
		}
		ctx := problem.WithCorrelationID(r.Context(), id)
		ctx = context.WithValue(ctx, loggerKey{}, slog.Default().With("correlationId", id))
		w.Header().Set(CorrelationHeader, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Logger returns the request-scoped logger, or slog.Default() outside a request.
func Logger(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey{}).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

func validCorrelationID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.':
		default:
			return false
		}
	}
	return true
}

// newUUID returns a random (version 4) UUID.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
