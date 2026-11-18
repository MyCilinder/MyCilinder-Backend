package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/jwtauth"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/problem"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/auth"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/profile"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/user"
)

// --- fakes ---

type fakeAuth struct {
	err    error
	tokens auth.Tokens
	gotIn  any
	gotSub string
	gotRT  string
	gotIP  string
	calls  int
}

func (f *fakeAuth) Register(_ context.Context, in auth.RegisterInput) error {
	f.calls++
	f.gotIn = in
	return f.err
}
func (f *fakeAuth) Confirm(_ context.Context, email, code string) error {
	f.calls++
	f.gotIn = [2]string{email, code}
	return f.err
}
func (f *fakeAuth) ResendCode(_ context.Context, email string) error {
	f.calls++
	f.gotIn = email
	return f.err
}
func (f *fakeAuth) Login(_ context.Context, in auth.LoginInput) (auth.Tokens, error) {
	f.calls++
	f.gotIn, f.gotIP = in, in.ClientIP
	return f.tokens, f.err
}
func (f *fakeAuth) Refresh(_ context.Context, rt string) (auth.Tokens, error) {
	f.calls++
	f.gotRT = rt
	return f.tokens, f.err
}
func (f *fakeAuth) Logout(_ context.Context, sub, rt string) error {
	f.calls++
	f.gotSub, f.gotRT = sub, rt
	return f.err
}

type fakeProfile struct {
	err      error
	p        profile.Profile
	gotToken string
	gotName  string
}

func (f *fakeProfile) Get(_ context.Context, _ string, token string) (profile.Profile, error) {
	f.gotToken = token
	return f.p, f.err
}
func (f *fakeProfile) UpdateDisplayName(_ context.Context, _, token, name string) (profile.Profile, error) {
	f.gotToken, f.gotName = token, name
	if f.err != nil {
		return profile.Profile{}, f.err
	}
	p := f.p
	p.DisplayName = strings.TrimSpace(name)
	return p, nil
}

// fakeVerifier accepts "good-token" as user "sub-1".
type fakeVerifier struct{}

func (fakeVerifier) Verify(_ context.Context, raw string) (jwtauth.Claims, error) {
	if raw == "good-token" {
		return jwtauth.Claims{Subject: "sub-1", Token: raw}, nil
	}
	return jwtauth.Claims{}, jwtauth.ErrInvalidToken
}

type env struct {
	h       http.Handler
	auth    *fakeAuth
	profile *fakeProfile
}

func newEnv(opts ...func(*Options)) *env {
	e := &env{auth: &fakeAuth{}, profile: &fakeProfile{}}
	o := Options{Auth: e.auth, Profile: e.profile, Verifier: fakeVerifier{}}
	for _, f := range opts {
		f(&o)
	}
	e.h = NewRouter(o)
	return e
}

type resp struct {
	code   int
	header http.Header
	body   map[string]any
	raw    string
}

func (e *env) do(method, path, body string, headers ...string) resp {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	out := resp{code: rec.Code, header: rec.Header(), raw: rec.Body.String()}
	_ = json.Unmarshal(rec.Body.Bytes(), &out.body)
	return out
}

func firstError(r resp) (field, rule string) {
	errs, _ := r.body["errors"].([]any)
	if len(errs) == 0 {
		return "", ""
	}
	e := errs[0].(map[string]any)
	f, _ := e["field"].(string)
	ru, _ := e["rule"].(string)
	return f, ru
}

// --- tests ---

func TestMain(m *testing.M) {
	// Request logs are noise here; middleware tests cover logging.
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	os.Exit(m.Run())
}

func TestHealthAndOrigin(t *testing.T) {
	e := newEnv(func(o *Options) { o.OriginVerifyEnabled, o.OriginVerifySecret = true, "s3cret" })
	if r := e.do("GET", "/healthz", ""); r.code != 200 {
		t.Errorf("healthz = %d", r.code)
	}
	if r := e.do("GET", "/readyz", ""); r.code != 200 {
		t.Errorf("readyz = %d", r.code)
	}
	if r := e.do("POST", "/api/v1/auth/login", `{}`); r.code != 403 {
		t.Errorf("missing origin header = %d", r.code)
	}
	r := e.do("POST", "/api/v1/auth/resend-code", `{"email":"a@b.co"}`, "X-Origin-Verify", "s3cret")
	if r.code != 204 {
		t.Errorf("with origin header = %d", r.code)
	}
	// Unknown API paths are also behind the origin check, so nothing is revealed to
	// callers that bypass CloudFront.
	if r := e.do("GET", "/api/v1/nope", ""); r.code != 403 {
		t.Errorf("unknown path without origin header = %d", r.code)
	}
	if r := e.do("GET", "/api/v1/nope", "", "X-Origin-Verify", "s3cret"); r.code != 404 || r.header.Get("Content-Type") != problem.ContentType {
		t.Errorf("unknown path = %d", r.code)
	}
}

func TestRegister(t *testing.T) {
	e := newEnv()
	r := e.do("POST", "/api/v1/auth/register", `{"email":"  Diego@Example.com ","password":"Secret#123","displayName":" Diego "}`)
	if r.code != 202 {
		t.Fatalf("code = %d %s", r.code, r.raw)
	}
	if got := e.auth.gotIn.(auth.RegisterInput); got.Email != "diego@example.com" || got.DisplayName != "Diego" {
		t.Errorf("input = %+v", got)
	}

	tests := []struct {
		name, body, field, rule string
	}{
		{"empty body", ``, "", ""},
		{"not json", `{"email":`, "", ""},
		{"unknown field", `{"email":"a@b.co","password":"Secret#123","displayName":"A","admin":true}`, "admin", "unknown-field"},
		{"wrong type", `{"email":1}`, "email", "type"},
		{"missing email", `{"password":"Secret#123","displayName":"A"}`, "email", "required"},
		{"bad email", `{"email":"nope","password":"Secret#123","displayName":"A"}`, "email", "format"},
		{"display name with name part", `{"email":"Diego <d@x.co>","password":"Secret#123","displayName":"A"}`, "email", "format"},
		{"short password", `{"email":"a@b.co","password":"short","displayName":"A"}`, "password", "length"},
		{"long display name", `{"email":"a@b.co","password":"Secret#123","displayName":"` + strings.Repeat("x", 51) + `"}`, "displayName", "length"},
		{"two objects", `{"email":"a@b.co"}{"x":1}`, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv()
			r := e.do("POST", "/api/v1/auth/register", tt.body)
			if r.code != 400 || r.body["type"] != string(problem.ValidationError) {
				t.Fatalf("code = %d body %s", r.code, r.raw)
			}
			if f, ru := firstError(r); f != tt.field || ru != tt.rule {
				t.Errorf("errors[0] = %s/%s, want %s/%s", f, ru, tt.field, tt.rule)
			}
			if e.auth.calls != 0 {
				t.Error("service called on invalid input")
			}
		})
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		body  string
		err   error
		code  int
		typ   problem.Type
		field string
		rule  string
	}{
		{"password policy", "/api/v1/auth/register", `{"email":"a@b.co","password":"Secret#123","displayName":"A"}`,
			&auth.PasswordPolicyError{Message: "Password must have symbol characters"}, 400, problem.ValidationError, "password", "password-policy"},
		{"invalid code", "/api/v1/auth/confirm", `{"email":"a@b.co","code":"123456"}`, auth.ErrInvalidCode, 400, problem.ValidationError, "code", "invalid-code"},
		{"expired code", "/api/v1/auth/confirm", `{"email":"a@b.co","code":"123456"}`, auth.ErrExpiredCode, 400, problem.ValidationError, "code", "expired-code"},
		{"bad credentials", "/api/v1/auth/login", `{"email":"a@b.co","password":"x"}`, auth.ErrInvalidCredentials, 401, problem.Unauthenticated, "", ""},
		{"not confirmed", "/api/v1/auth/login", `{"email":"a@b.co","password":"x"}`, auth.ErrNotConfirmed, 403, problem.Forbidden, "", "account-not-confirmed"},
		{"rate limited", "/api/v1/auth/login", `{"email":"a@b.co","password":"x"}`, auth.NewRateLimitedError(90 * time.Second), 429, problem.RateLimited, "", ""},
		{"unavailable", "/api/v1/auth/login", `{"email":"a@b.co","password":"x"}`, errors.Join(auth.ErrUnavailable, errors.New("cognito down")), 503, problem.Unavailable, "", ""},
		{"unexpected", "/api/v1/auth/login", `{"email":"a@b.co","password":"x"}`, errors.New("boom"), 500, problem.Internal, "", ""},
		{"bad refresh token", "/api/v1/auth/refresh", `{"refreshToken":"rt"}`, auth.ErrInvalidRefreshToken, 401, problem.Unauthenticated, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv()
			e.auth.err = tt.err
			r := e.do("POST", tt.path, tt.body)
			if r.code != tt.code || r.body["type"] != string(tt.typ) {
				t.Fatalf("got %d %s", r.code, r.raw)
			}
			if f, ru := firstError(r); f != tt.field || ru != tt.rule {
				t.Errorf("errors[0] = %s/%s", f, ru)
			}
			if r.body["correlationId"] == "" || r.body["correlationId"] != r.header.Get(middleware.CorrelationHeader) {
				t.Error("correlation id missing or mismatched")
			}
			if tt.code == 429 && r.header.Get("Retry-After") != "90" {
				t.Errorf("Retry-After = %q", r.header.Get("Retry-After"))
			}
		})
	}
}

func TestLoginResponse(t *testing.T) {
	e := newEnv()
	e.auth.tokens = auth.Tokens{AccessToken: "at", IDToken: "it", RefreshToken: "rt", ExpiresIn: 3600}
	r := e.do("POST", "/api/v1/auth/login", `{"email":"A@B.co","password":"pw"}`)
	if r.code != 200 {
		t.Fatalf("code = %d %s", r.code, r.raw)
	}
	want := map[string]any{"accessToken": "at", "idToken": "it", "refreshToken": "rt", "expiresIn": float64(3600), "tokenType": "Bearer"}
	for k, v := range want {
		if r.body[k] != v {
			t.Errorf("%s = %v", k, r.body[k])
		}
	}
	if r.header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q", r.header.Get("Cache-Control"))
	}
	if got := e.auth.gotIn.(auth.LoginInput); got.Email != "a@b.co" || got.ClientIP != "192.0.2.1" {
		t.Errorf("input = %+v", got)
	}
}

func TestRefreshResponseOmitsRefreshToken(t *testing.T) {
	e := newEnv()
	e.auth.tokens = auth.Tokens{AccessToken: "at2", IDToken: "it2", ExpiresIn: 3600}
	r := e.do("POST", "/api/v1/auth/refresh", `{"refreshToken":"rt"}`)
	if r.code != 200 || r.body["accessToken"] != "at2" || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("got %d %s", r.code, r.raw)
	}
	if _, present := r.body["refreshToken"]; present {
		t.Error("refreshToken must not be returned")
	}
}

func TestProtectedRoutesNeedToken(t *testing.T) {
	e := newEnv()
	for _, rt := range []struct{ method, path string }{
		{"POST", "/api/v1/auth/logout"}, {"GET", "/api/v1/me"}, {"PATCH", "/api/v1/me"},
	} {
		if r := e.do(rt.method, rt.path, `{}`); r.code != 401 {
			t.Errorf("%s %s without token = %d", rt.method, rt.path, r.code)
		}
		if r := e.do(rt.method, rt.path, `{}`, "Authorization", "Bearer bad"); r.code != 401 {
			t.Errorf("%s %s with bad token = %d", rt.method, rt.path, r.code)
		}
	}
}

func TestLogout(t *testing.T) {
	e := newEnv()
	r := e.do("POST", "/api/v1/auth/logout", `{"refreshToken":"rt"}`, "Authorization", "Bearer good-token")
	if r.code != 204 || e.auth.gotSub != "sub-1" || e.auth.gotRT != "rt" {
		t.Fatalf("got %d sub=%q rt=%q", r.code, e.auth.gotSub, e.auth.gotRT)
	}

	e.auth.err = auth.ErrInvalidRefreshToken
	r = e.do("POST", "/api/v1/auth/logout", `{"refreshToken":"junk"}`, "Authorization", "Bearer good-token")
	if f, ru := firstError(r); r.code != 400 || f != "refreshToken" || ru != "format" {
		t.Errorf("invalid token: %d %s", r.code, r.raw)
	}
}

func TestMe(t *testing.T) {
	e := newEnv()
	e.profile.p = profile.Profile{ID: "sub-1", Email: "d@x.co", DisplayName: "Diego", CreatedAt: time.Date(2026, 11, 1, 2, 3, 4, 0, time.FixedZone("x", -6*3600))}

	r := e.do("GET", "/api/v1/me", "", "Authorization", "Bearer good-token")
	if r.code != 200 || e.profile.gotToken != "good-token" {
		t.Fatalf("got %d %s", r.code, r.raw)
	}
	want := map[string]any{"id": "sub-1", "email": "d@x.co", "displayName": "Diego", "createdAt": "2026-11-01T08:03:04Z"}
	for k, v := range want {
		if r.body[k] != v {
			t.Errorf("%s = %v, want %v", k, r.body[k], v)
		}
	}

	r = e.do("PATCH", "/api/v1/me", `{"displayName":"New"}`, "Authorization", "Bearer good-token")
	if r.code != 200 || r.body["displayName"] != "New" {
		t.Errorf("patch: %d %s", r.code, r.raw)
	}

	e.profile.err = &user.ValidationError{Rule: user.RuleLength, Message: "display name must be 1-50 characters"}
	r = e.do("PATCH", "/api/v1/me", `{"displayName":"x"}`, "Authorization", "Bearer good-token")
	if f, ru := firstError(r); r.code != 400 || f != "displayName" || ru != "length" {
		t.Errorf("invalid name: %d %s", r.code, r.raw)
	}

	e.profile.err = profile.ErrUnauthenticated
	if r := e.do("GET", "/api/v1/me", "", "Authorization", "Bearer good-token"); r.code != 401 {
		t.Errorf("revoked token: %d", r.code)
	}
}
