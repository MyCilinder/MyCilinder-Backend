// Package jwtauth validates Cognito access tokens in the service (SEC-01). API Gateway
// already checks the token at the edge; this is the second, independent check.
//
// A token is accepted only if:
//   - its signature verifies against the user pool's JWKS (fetched and cached);
//   - iss is the user pool, token_use is "access", and client_id is our app client;
//   - it is not expired (30 seconds of clock skew allowed).
//
// ID tokens are rejected (token_use "id"): the API accepts access tokens only.
package jwtauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lestrrat-go/httprc/v3"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/problem"
)

var (
	// ErrInvalidToken: missing, malformed, badly signed, expired, or wrong audience.
	ErrInvalidToken = errors.New("jwtauth: invalid token")
	// ErrKeysUnavailable: the JWKS could not be fetched yet; the request cannot be judged.
	ErrKeysUnavailable = errors.New("jwtauth: signing keys unavailable")
)

// Claims are the verified token values handlers may use.
type Claims struct {
	Subject string   // Cognito sub, the user id
	Groups  []string // cognito:groups
	Token   string   // the raw access token, for calls such as Cognito GetUser
}

// InGroup reports whether the user belongs to group (for example "operators").
func (c Claims) InGroup(group string) bool {
	for _, g := range c.Groups {
		if g == group {
			return true
		}
	}
	return false
}

// Verifier checks a raw access token.
type Verifier interface {
	Verify(ctx context.Context, raw string) (Claims, error)
}

// CognitoConfig identifies the user pool and app client.
type CognitoConfig struct {
	Region     string
	UserPoolID string
	ClientID   string
	// Issuer and JWKSURL override the values derived from Region and UserPoolID. Tests
	// use them; services leave them empty.
	Issuer  string
	JWKSURL string
}

// CognitoVerifier verifies tokens against a cached JWKS.
type CognitoVerifier struct {
	cache    *jwk.Cache
	jwksURL  string
	issuer   string
	clientID string
}

// NewCognitoVerifier starts the JWKS cache. It does not wait for the first fetch, so the
// service starts even if Cognito is briefly unreachable; requests in that window get
// ErrKeysUnavailable. Call Close on shutdown.
func NewCognitoVerifier(ctx context.Context, cfg CognitoConfig) (*CognitoVerifier, error) {
	issuer := cfg.Issuer
	if issuer == "" {
		issuer = fmt.Sprintf("https://cognito-idp.%s.amazonaws.com/%s", cfg.Region, cfg.UserPoolID)
	}
	jwksURL := cfg.JWKSURL
	if jwksURL == "" {
		jwksURL = issuer + "/.well-known/jwks.json"
	}
	if cfg.ClientID == "" {
		return nil, errors.New("jwtauth: client id is required")
	}

	client := httprc.NewClient(httprc.WithWhitelist(httprc.NewMapWhitelist().Add(jwksURL)))
	cache, err := jwk.NewCache(ctx, client)
	if err != nil {
		return nil, fmt.Errorf("jwtauth: start JWKS cache: %w", err)
	}
	if err := cache.Register(ctx, jwksURL,
		jwk.WithWaitReady(false),
		jwk.WithMinInterval(5*time.Minute),
	); err != nil {
		return nil, fmt.Errorf("jwtauth: register JWKS: %w", err)
	}
	return &CognitoVerifier{cache: cache, jwksURL: jwksURL, issuer: issuer, clientID: cfg.ClientID}, nil
}

// Close stops the JWKS refresh goroutines.
func (v *CognitoVerifier) Close(ctx context.Context) error { return v.cache.Shutdown(ctx) }

// Ready reports whether the JWKS has been fetched.
func (v *CognitoVerifier) Ready(ctx context.Context) error {
	if !v.cache.Ready(ctx, v.jwksURL) {
		return ErrKeysUnavailable
	}
	return nil
}

// Verify checks raw and returns its claims.
func (v *CognitoVerifier) Verify(ctx context.Context, raw string) (Claims, error) {
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if !v.cache.Ready(waitCtx, v.jwksURL) {
		return Claims{}, ErrKeysUnavailable
	}
	set, err := v.cache.Lookup(ctx, v.jwksURL)
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %w", ErrKeysUnavailable, err)
	}

	tok, err := jwt.ParseString(raw,
		jwt.WithKeySet(set),
		jwt.WithValidate(true),
		jwt.WithIssuer(v.issuer),
		jwt.WithRequiredClaim("exp"),
		jwt.WithClaimValue("token_use", "access"),
		jwt.WithClaimValue("client_id", v.clientID),
		jwt.WithAcceptableSkew(30*time.Second),
	)
	if err != nil {
		return Claims{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	sub, ok := tok.Subject()
	if !ok || sub == "" {
		return Claims{}, fmt.Errorf("%w: no sub", ErrInvalidToken)
	}

	c := Claims{Subject: sub, Token: raw}
	var groups any
	if err := tok.Get("cognito:groups", &groups); err == nil {
		if list, ok := groups.([]any); ok {
			for _, g := range list {
				if s, ok := g.(string); ok {
					c.Groups = append(c.Groups, s)
				}
			}
		}
	}
	return c, nil
}

type claimsKey struct{}

// FromContext returns the claims set by Require or Optional.
func FromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(claimsKey{}).(Claims)
	return c, ok
}

// WithClaims stores claims in ctx. Exposed for handler tests.
func WithClaims(ctx context.Context, c Claims) context.Context {
	return context.WithValue(ctx, claimsKey{}, c)
}

// Require rejects requests without a valid bearer access token (401), or 503 when the
// signing keys are not available. On success the claims are in the request context and
// the request logger carries userId.
func Require(v Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := bearerToken(r); !ok {
				w.Header().Set("WWW-Authenticate", `Bearer`)
				problem.Write(w, r, http.StatusUnauthorized, problem.Unauthenticated, "missing bearer token")
				return
			}
			authenticate(v, w, r, next)
		})
	}
}

// Optional lets requests without an Authorization header through as guests (no claims
// in the context). A request that does send the header must carry a valid bearer token:
// an invalid or expired one gets 401, as on Require routes, so a client with a stale
// token is told to refresh instead of silently seeing the guest view.
func Optional(v Verifier) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") == "" {
				next.ServeHTTP(w, r)
				return
			}
			authenticate(v, w, r, next)
		})
	}
}

// RequireGroup returns 403 unless the verified user belongs to group (cognito:groups),
// for example "operators". It must run after Require; without claims it returns 401.
func RequireGroup(group string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := FromContext(r.Context())
			if !ok {
				w.Header().Set("WWW-Authenticate", `Bearer`)
				problem.Write(w, r, http.StatusUnauthorized, problem.Unauthenticated, "missing bearer token")
				return
			}
			if !claims.InGroup(group) {
				problem.Write(w, r, http.StatusForbidden, problem.Forbidden, "")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// authenticate verifies the bearer token and calls next with the claims in the context,
// or writes 401 (bad token) or 503 (signing keys unavailable).
func authenticate(v Verifier, w http.ResponseWriter, r *http.Request, next http.Handler) {
	raw, ok := bearerToken(r)
	if !ok {
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		problem.Write(w, r, http.StatusUnauthorized, problem.Unauthenticated, "invalid or expired token")
		return
	}
	claims, err := v.Verify(r.Context(), raw)
	if errors.Is(err, ErrKeysUnavailable) {
		middleware.Logger(r.Context()).Error("JWT signing keys unavailable", "err", err)
		problem.Write(w, r, http.StatusServiceUnavailable, problem.Unavailable, "")
		return
	}
	if err != nil {
		middleware.Logger(r.Context()).Debug("token rejected", "err", err)
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		problem.Write(w, r, http.StatusUnauthorized, problem.Unauthenticated, "invalid or expired token")
		return
	}
	ctx := WithClaims(r.Context(), claims)
	ctx = middleware.WithLogger(ctx, middleware.Logger(ctx).With("userId", claims.Subject))
	next.ServeHTTP(w, r.WithContext(ctx))
}

func bearerToken(r *http.Request) (string, bool) {
	h := r.Header.Get("Authorization")
	scheme, token, found := strings.Cut(h, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return "", false
	}
	token = strings.TrimSpace(token)
	return token, token != ""
}
