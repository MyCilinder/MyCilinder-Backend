//go:build integration

package httpapi_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/db"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/jwtauth"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/jwtauth/jwtauthtest"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/ratelimit"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/auth"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/cognito"
	httpapi "github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/http"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/profile"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/store"
)

// End-to-end test of the auth facade with everything real except Cognito: the Compose
// Postgres (as auth_svc), the Compose Redis, and JWT validation against a locally served
// JWKS. Run with the local stack up:
//
//	TEST_DATABASE_URL=postgres://auth_svc:auth_svc_local@127.0.0.1:5432/mycilinder?sslmode=disable \
//	TEST_ADMIN_DATABASE_URL=postgres://postgres:postgres@127.0.0.1:5432/mycilinder?sslmode=disable \
//	TEST_REDIS_ADDR=127.0.0.1:6379 \
//	  go test -tags integration ./services/auth/internal/http/...
//
// TEST_ADMIN_DATABASE_URL is optional; with it the test also checks the audit rows,
// which auth_svc cannot read.

// --- in-memory Cognito ---

type fakeUser struct {
	sub, password, name string
	confirmed           bool
	revokedAt           time.Time
}

type fakeCognito struct {
	mu      sync.Mutex
	iss     *jwtauthtest.Issuer
	t       testing.TB
	users   map[string]*fakeUser // by email
	refresh map[string]string    // refresh token -> email
}

func newFakeCognito(t testing.TB, iss *jwtauthtest.Issuer) *fakeCognito {
	return &fakeCognito{iss: iss, t: t, users: map[string]*fakeUser{}, refresh: map[string]string{}}
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randomUUID() string {
	h := randomHex(16)
	return h[0:8] + "-" + h[8:12] + "-4" + h[13:16] + "-a" + h[17:20] + "-" + h[20:32]
}

func (f *fakeCognito) SignUp(_ context.Context, email, password, name string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[email]; ok {
		return "", cognito.ErrUserExists
	}
	if !strings.ContainsAny(password, "#!@$%") {
		return "", &cognito.InvalidPasswordError{Message: "Password did not conform with policy: Password must have symbol characters"}
	}
	u := &fakeUser{sub: randomUUID(), password: password, name: name}
	f.users[email] = u
	return u.sub, nil
}

func (f *fakeCognito) ConfirmSignUp(_ context.Context, email, code string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[email]
	switch {
	case !ok:
		return cognito.ErrUserNotFound
	case code != "123456":
		return cognito.ErrInvalidCode
	}
	u.confirmed = true
	return nil
}

func (f *fakeCognito) ResendCode(context.Context, string) error { return nil }

func (f *fakeCognito) Login(_ context.Context, email, password string) (cognito.Tokens, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[email]
	switch {
	case !ok:
		return cognito.Tokens{}, cognito.ErrUserNotFound
	case u.password != password:
		return cognito.Tokens{}, cognito.ErrNotAuthorized
	case !u.confirmed:
		return cognito.Tokens{}, cognito.ErrNotConfirmed
	}
	rt := "rt-" + randomHex(16)
	f.refresh[rt] = email
	return cognito.Tokens{
		AccessToken:  f.iss.Token(f.t, u.sub, jwtauthtest.Options{}),
		IDToken:      f.iss.Token(f.t, u.sub, jwtauthtest.Options{TokenUse: "id", Name: u.name}),
		RefreshToken: rt,
		ExpiresIn:    3600,
	}, nil
}

func (f *fakeCognito) Refresh(_ context.Context, rt string) (cognito.Tokens, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	email, ok := f.refresh[rt]
	if !ok {
		return cognito.Tokens{}, cognito.ErrNotAuthorized
	}
	u := f.users[email]
	return cognito.Tokens{
		AccessToken: f.iss.Token(f.t, u.sub, jwtauthtest.Options{}),
		IDToken:     f.iss.Token(f.t, u.sub, jwtauthtest.Options{TokenUse: "id", Name: u.name}),
		ExpiresIn:   3600,
	}, nil
}

// Revoke behaves like Cognito: the refresh token stops working, and so do the access
// tokens issued from it (approximated here as every access token of the user).
func (f *fakeCognito) Revoke(_ context.Context, rt string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if email, ok := f.refresh[rt]; ok {
		f.users[email].revokedAt = time.Now()
		delete(f.refresh, rt)
	}
	return nil
}

func (f *fakeCognito) GetUser(_ context.Context, accessToken string) (cognito.UserInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for email, u := range f.users {
		if strings.Contains(accessToken, ".") && tokenSub(accessToken) == u.sub {
			if !u.revokedAt.IsZero() {
				return cognito.UserInfo{}, cognito.ErrNotAuthorized
			}
			return cognito.UserInfo{Sub: u.sub, Email: email, DisplayName: u.name}, nil
		}
	}
	return cognito.UserInfo{}, cognito.ErrNotAuthorized
}

func tokenSub(tok string) string {
	var c struct {
		Sub string `json:"sub"`
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return ""
	}
	b, err := jsonB64(parts[1])
	if err != nil || json.Unmarshal(b, &c) != nil {
		return ""
	}
	return c.Sub
}

func jsonB64(s string) ([]byte, error) { return base64.RawURLEncoding.DecodeString(s) }

// --- harness ---

type client struct {
	t      *testing.T
	h      http.Handler
	remote string
}

type result struct {
	code   int
	header http.Header
	body   map[string]any
}

func (c client) call(method, path, body, token string) result {
	c.t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = c.remote
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	c.h.ServeHTTP(rec, req)
	out := result{code: rec.Code, header: rec.Header()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

func (r result) rule() string {
	errs, _ := r.body["errors"].([]any)
	if len(errs) == 0 {
		return ""
	}
	s, _ := errs[0].(map[string]any)["rule"].(string)
	return s
}

func expect(t *testing.T, step string, r result, code int) {
	t.Helper()
	if r.code != code {
		t.Fatalf("%s: status %d, want %d, body %v", step, r.code, code, r.body)
	}
}

func setup(t *testing.T) (client, *pgxpool.Pool) {
	t.Helper()
	dbURL, redisAddr := os.Getenv("TEST_DATABASE_URL"), os.Getenv("TEST_REDIS_ADDR")
	if dbURL == "" || redisAddr == "" {
		t.Skip("TEST_DATABASE_URL and TEST_REDIS_ADDR are required")
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx := context.Background()

	pool, err := db.NewPool(ctx, db.Options{URL: dbURL})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	rdb := redis.NewClient(&redis.Options{Addr: redisAddr})
	t.Cleanup(func() { _ = rdb.Close() })

	iss := jwtauthtest.New(t, "e2e-client")
	verifier, err := jwtauth.NewCognitoVerifier(ctx, jwtauth.CognitoConfig{
		ClientID: "e2e-client", Issuer: iss.IssuerURL(), JWKSURL: iss.JWKSURL(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = verifier.Close(ctx) })

	idp := newFakeCognito(t, iss)
	users := store.NewUsers(pool)
	throttle := auth.NewRedisThrottle(rdb, auth.ThrottleLimits{MaxFailuresPerEmail: 5, MaxFailuresPerIP: 20, Window: time.Minute})
	clientIP := ratelimit.ClientIP(0)

	h := httpapi.NewRouter(httpapi.Options{
		Auth:        auth.New(idp, users, store.NewAudit(pool, "auth"), throttle),
		Profile:     profile.New(idp, users),
		Verifier:    verifier,
		PublicLimit: ratelimit.PerIP(ratelimit.New(rdb, "e2e:"+randomHex(4), 100, time.Minute), clientIP),
		ClientIP:    clientIP,
	})
	// A fresh client address per run keeps the per-IP failure counter independent of
	// earlier runs.
	b := make([]byte, 2)
	_, _ = rand.Read(b)
	return client{t: t, h: h, remote: fmt.Sprintf("198.18.%d.%d:4000", b[0], b[1])}, pool
}

// --- the flow ---

func TestAuthFlowEndToEnd(t *testing.T) {
	c, pool := setup(t)
	ctx := context.Background()
	email := "e2e-" + randomHex(4) + "@example.com"
	const password = "Secret#123"

	reg := fmt.Sprintf(`{"email":%q,"password":%q,"displayName":"Diego E2E"}`, email, password)
	expect(t, "register", c.call("POST", "/api/v1/auth/register", reg, ""), 202)
	expect(t, "register again (no enumeration)", c.call("POST", "/api/v1/auth/register", reg, ""), 202)

	weak := fmt.Sprintf(`{"email":"other-%s","password":"Password123","displayName":"X"}`, email)
	r := c.call("POST", "/api/v1/auth/register", weak, "")
	if expect(t, "weak password", r, 400); r.rule() != "password-policy" {
		t.Errorf("weak password rule = %q", r.rule())
	}

	login := fmt.Sprintf(`{"email":%q,"password":%q}`, strings.ToUpper(email), password)
	r = c.call("POST", "/api/v1/auth/login", login, "")
	if expect(t, "login before confirm", r, 403); r.rule() != "account-not-confirmed" {
		t.Errorf("rule = %q", r.rule())
	}

	r = c.call("POST", "/api/v1/auth/confirm", fmt.Sprintf(`{"email":%q,"code":"000000"}`, email), "")
	if expect(t, "wrong code", r, 400); r.rule() != "invalid-code" {
		t.Errorf("rule = %q", r.rule())
	}
	expect(t, "confirm", c.call("POST", "/api/v1/auth/confirm", fmt.Sprintf(`{"email":%q,"code":"123456"}`, email), ""), 204)
	expect(t, "resend for unknown email", c.call("POST", "/api/v1/auth/resend-code", `{"email":"nobody@example.com"}`, ""), 204)

	// Login creates the profile.
	r = c.call("POST", "/api/v1/auth/login", login, "")
	expect(t, "login", r, 200)
	access, refresh := r.body["accessToken"].(string), r.body["refreshToken"].(string)
	if access == "" || refresh == "" || r.body["tokenType"] != "Bearer" || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("login response %v %v", r.body, r.header)
	}

	r = c.call("GET", "/api/v1/me", "", access)
	expect(t, "GET /me", r, 200)
	sub := r.body["id"].(string)
	if r.body["email"] != email || r.body["displayName"] != "Diego E2E" {
		t.Errorf("profile = %v", r.body)
	}
	var dbName string
	if err := pool.QueryRow(ctx, `SELECT display_name FROM auth.users WHERE id = $1`, sub).Scan(&dbName); err != nil || dbName != "Diego E2E" {
		t.Fatalf("auth.users row: %q %v", dbName, err)
	}

	r = c.call("PATCH", "/api/v1/me", `{"displayName":"  Renamed  "}`, access)
	if expect(t, "PATCH /me", r, 200); r.body["displayName"] != "Renamed" {
		t.Errorf("patched = %v", r.body)
	}
	expect(t, "PATCH /me invalid", c.call("PATCH", "/api/v1/me", `{"displayName":""}`, access), 400)

	// A second login does not overwrite the changed name.
	expect(t, "second login", c.call("POST", "/api/v1/auth/login", login, ""), 200)
	_ = pool.QueryRow(ctx, `SELECT display_name FROM auth.users WHERE id = $1`, sub).Scan(&dbName)
	if dbName != "Renamed" {
		t.Errorf("display name after second login = %q", dbName)
	}

	r = c.call("POST", "/api/v1/auth/refresh", fmt.Sprintf(`{"refreshToken":%q}`, refresh), "")
	if expect(t, "refresh", r, 200); r.body["accessToken"] == "" || r.body["refreshToken"] != nil {
		t.Errorf("refresh response = %v", r.body)
	}

	expect(t, "logout without token", c.call("POST", "/api/v1/auth/logout", fmt.Sprintf(`{"refreshToken":%q}`, refresh), ""), 401)
	expect(t, "logout", c.call("POST", "/api/v1/auth/logout", fmt.Sprintf(`{"refreshToken":%q}`, refresh), access), 204)
	expect(t, "logout again (idempotent)", c.call("POST", "/api/v1/auth/logout", fmt.Sprintf(`{"refreshToken":%q}`, refresh), access), 204)
	expect(t, "refresh after logout", c.call("POST", "/api/v1/auth/refresh", fmt.Sprintf(`{"refreshToken":%q}`, refresh), ""), 401)
	expect(t, "GET /me after logout", c.call("GET", "/api/v1/me", "", access), 401)

	checkAudit(t, sub)
}

func TestLoginThrottling(t *testing.T) {
	c, _ := setup(t)
	email := "throttle-" + randomHex(4) + "@example.com"
	expect(t, "register", c.call("POST", "/api/v1/auth/register", fmt.Sprintf(`{"email":%q,"password":"Secret#123","displayName":"T"}`, email), ""), 202)
	expect(t, "confirm", c.call("POST", "/api/v1/auth/confirm", fmt.Sprintf(`{"email":%q,"code":"123456"}`, email), ""), 204)

	wrong := fmt.Sprintf(`{"email":%q,"password":"wrong"}`, email)
	for i := 1; i <= 5; i++ {
		expect(t, fmt.Sprintf("wrong password %d", i), c.call("POST", "/api/v1/auth/login", wrong, ""), 401)
	}
	r := c.call("POST", "/api/v1/auth/login", wrong, "")
	expect(t, "6th wrong password", r, 429)
	if ra := r.header.Get("Retry-After"); ra == "" || ra == "0" {
		t.Errorf("Retry-After = %q", ra)
	}
	// While throttled, even the right password is refused.
	expect(t, "right password while throttled", c.call("POST", "/api/v1/auth/login", fmt.Sprintf(`{"email":%q,"password":"Secret#123"}`, email), ""), 429)
}

// checkAudit reads audit.audit_log as the admin role, if configured.
func checkAudit(t *testing.T, sub string) {
	url := os.Getenv("TEST_ADMIN_DATABASE_URL")
	if url == "" {
		t.Log("TEST_ADMIN_DATABASE_URL not set; skipping audit checks")
		return
	}
	pool, err := db.NewPool(context.Background(), db.Options{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	rows, err := pool.Query(context.Background(),
		`SELECT action, details::text FROM audit.audit_log WHERE actor_id = $1 ORDER BY id`, sub)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var action, details string
		_ = rows.Scan(&action, &details)
		got = append(got, action+" "+details)
		if strings.Contains(details, "@") {
			t.Errorf("audit details contain an email: %s", details)
		}
	}
	want := []string{
		`auth.register {}`,
		`auth.login_succeeded {"firstLogin": true}`,
		`auth.login_succeeded {"firstLogin": false}`,
		`auth.logout {}`,
		`auth.logout {}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("audit rows:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
