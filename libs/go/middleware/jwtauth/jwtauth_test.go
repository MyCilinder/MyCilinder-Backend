package jwtauth_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/jwtauth"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/middleware/jwtauth/jwtauthtest"
	"github.com/MyCilinder/MyCilinder-Backend/libs/go/problem"
)

const clientID = "test-client"

func newVerifier(t *testing.T, iss *jwtauthtest.Issuer) *jwtauth.CognitoVerifier {
	t.Helper()
	v, err := jwtauth.NewCognitoVerifier(context.Background(), jwtauth.CognitoConfig{
		ClientID: clientID, Issuer: iss.IssuerURL(), JWKSURL: iss.JWKSURL(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = v.Close(context.Background()) })
	return v
}

func TestVerify(t *testing.T) {
	iss := jwtauthtest.New(t, clientID)
	other := jwtauthtest.New(t, clientID) // different signing key
	v := newVerifier(t, iss)
	const sub = "6f1c2a3b-0000-4000-8000-000000000001"

	tests := []struct {
		name  string
		token string
		ok    bool
	}{
		{"valid access token", iss.Token(t, sub, jwtauthtest.Options{Groups: []string{"operators"}}), true},
		{"expired", iss.Token(t, sub, jwtauthtest.Options{TTL: -2 * time.Minute}), false},
		{"wrong client", iss.Token(t, sub, jwtauthtest.Options{ClientID: "someone-else"}), false},
		{"id token", iss.Token(t, sub, jwtauthtest.Options{TokenUse: "id"}), false},
		{"wrong issuer", iss.Token(t, sub, jwtauthtest.Options{Issuer: "https://evil.example"}), false},
		{"signed by another key", other.Token(t, sub, jwtauthtest.Options{Issuer: iss.IssuerURL()}), false},
		{"garbage", "not.a.jwt", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := v.Verify(context.Background(), tt.token)
			if tt.ok {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if c.Subject != sub || !c.InGroup("operators") || c.Token != tt.token {
					t.Errorf("claims = %+v", c)
				}
				return
			}
			if err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestVerifyKeysUnavailable(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer down.Close()
	v, err := jwtauth.NewCognitoVerifier(context.Background(), jwtauth.CognitoConfig{
		ClientID: clientID, Issuer: down.URL, JWKSURL: down.URL + "/jwks.json",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer v.Close(context.Background())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	if _, err := v.Verify(ctx, "x.y.z"); err != jwtauth.ErrKeysUnavailable {
		t.Fatalf("got %v, want ErrKeysUnavailable", err)
	}
}

func TestRequire(t *testing.T) {
	iss := jwtauthtest.New(t, clientID)
	v := newVerifier(t, iss)
	var seen jwtauth.Claims
	h := jwtauth.Require(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, _ = jwtauth.FromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	valid := iss.Token(t, "user-1", jwtauthtest.Options{})
	tests := []struct {
		name   string
		header string
		want   int
	}{
		{"no header", "", http.StatusUnauthorized},
		{"wrong scheme", "Basic abc", http.StatusUnauthorized},
		{"empty token", "Bearer ", http.StatusUnauthorized},
		{"invalid token", "Bearer x.y.z", http.StatusUnauthorized},
		{"valid", "Bearer " + valid, http.StatusNoContent},
		{"scheme is case-insensitive", "bearer " + valid, http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen = jwtauth.Claims{}
			req := httptest.NewRequest(http.MethodGet, "/me", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if tt.want == http.StatusUnauthorized {
				var p problem.Details
				_ = json.Unmarshal(rec.Body.Bytes(), &p)
				if p.Type != problem.Unauthenticated || rec.Header().Get("WWW-Authenticate") == "" {
					t.Errorf("body %s, WWW-Authenticate %q", rec.Body.String(), rec.Header().Get("WWW-Authenticate"))
				}
			} else if seen.Subject != "user-1" {
				t.Errorf("claims not in context: %+v", seen)
			}
		})
	}
}

func TestOptional(t *testing.T) {
	iss := jwtauthtest.New(t, clientID)
	v := newVerifier(t, iss)
	var seen jwtauth.Claims
	var hadClaims bool
	h := jwtauth.Optional(v)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, hadClaims = jwtauth.FromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	}))

	valid := iss.Token(t, "user-1", jwtauthtest.Options{})
	expired := iss.Token(t, "user-1", jwtauthtest.Options{TTL: -time.Hour})
	tests := []struct {
		name       string
		header     string
		want       int
		wantClaims bool
	}{
		{"no header is a guest", "", http.StatusNoContent, false},
		{"valid token", "Bearer " + valid, http.StatusNoContent, true},
		{"invalid token", "Bearer x.y.z", http.StatusUnauthorized, false},
		{"expired token", "Bearer " + expired, http.StatusUnauthorized, false},
		{"wrong scheme", "Basic abc", http.StatusUnauthorized, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seen, hadClaims = jwtauth.Claims{}, false
			req := httptest.NewRequest(http.MethodGet, "/videos/x", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
			if hadClaims != tt.wantClaims {
				t.Errorf("claims in context = %v, want %v", hadClaims, tt.wantClaims)
			}
			if tt.wantClaims && seen.Subject != "user-1" {
				t.Errorf("subject = %q", seen.Subject)
			}
		})
	}
}

func TestRequireGroup(t *testing.T) {
	iss := jwtauthtest.New(t, clientID)
	v := newVerifier(t, iss)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	chained := jwtauth.Require(v)(jwtauth.RequireGroup("operators")(ok))

	tests := []struct {
		name   string
		header string
		want   int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"no groups", "Bearer " + iss.Token(t, "u", jwtauthtest.Options{}), http.StatusForbidden},
		{"other group", "Bearer " + iss.Token(t, "u", jwtauthtest.Options{Groups: []string{"creators"}}), http.StatusForbidden},
		{"operator", "Bearer " + iss.Token(t, "u", jwtauthtest.Options{Groups: []string{"creators", "operators"}}), http.StatusNoContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/ops/videos/x/hide", nil)
			if tt.header != "" {
				req.Header.Set("Authorization", tt.header)
			}
			rec := httptest.NewRecorder()
			chained.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.want, rec.Body.String())
			}
		})
	}

	t.Run("without Require in front", func(t *testing.T) {
		rec := httptest.NewRecorder()
		jwtauth.RequireGroup("operators")(ok).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/", nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", rec.Code)
		}
	})
}
