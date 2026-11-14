package middleware

import (
	"crypto/subtle"
	"net/http"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/problem"
)

// OriginHeader is the secret header CloudFront adds to every /api/* request
// (Interface Design §3.2).
const OriginHeader = "X-Origin-Verify"

// OriginVerify rejects requests whose X-Origin-Verify header does not match secret, so
// API Gateway's public endpoint cannot be used to bypass CloudFront and WAF.
//
// When enabled is false the middleware passes every request through; this is the local
// development setting. Mount it on API routes only: ALB health checks do not go through
// CloudFront and do not carry the header.
func OriginVerify(enabled bool, secret string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if !enabled {
			return next
		}
		want := []byte(secret)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got := []byte(r.Header.Get(OriginHeader))
			if len(want) == 0 || subtle.ConstantTimeCompare(got, want) != 1 {
				problem.Write(w, r, http.StatusForbidden, problem.Forbidden, "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
