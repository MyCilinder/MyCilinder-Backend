package auth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/cognito"
	"github.com/MyCilinder/MyCilinder-Backend/services/auth/internal/store"
)

// fakeCognito returns err for every call, or canned successes.
type fakeCognito struct {
	err     error
	tokens  cognito.Tokens
	revoked []string
}

func (f *fakeCognito) SignUp(context.Context, string, string, string) (string, error) {
	return "new-sub", f.err
}
func (f *fakeCognito) ConfirmSignUp(context.Context, string, string) error { return f.err }
func (f *fakeCognito) ResendCode(context.Context, string) error            { return f.err }
func (f *fakeCognito) Login(context.Context, string, string) (cognito.Tokens, error) {
	return f.tokens, f.err
}
func (f *fakeCognito) Refresh(context.Context, string) (cognito.Tokens, error) {
	return f.tokens, f.err
}
func (f *fakeCognito) Revoke(_ context.Context, rt string) error {
	f.revoked = append(f.revoked, rt)
	return f.err
}

type fakeUsers struct {
	err     error
	created map[string]string
}

func (f *fakeUsers) EnsureUser(_ context.Context, id, name string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if f.created == nil {
		f.created = map[string]string{}
	}
	if _, ok := f.created[id]; ok {
		return false, nil
	}
	f.created[id] = name
	return true, nil
}

type fakeAudit struct{ entries []store.AuditEntry }

func (f *fakeAudit) Write(_ context.Context, e store.AuditEntry) error {
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeAudit) actions() []string {
	var out []string
	for _, e := range f.entries {
		out = append(out, e.Action)
	}
	return out
}

type fakeThrottle struct {
	block    error
	failures int
	resets   int
}

func (f *fakeThrottle) Check(context.Context, string, string) error   { return f.block }
func (f *fakeThrottle) RecordFailure(context.Context, string, string) { f.failures++ }
func (f *fakeThrottle) Reset(context.Context, string)                 { f.resets++ }

func cerr(base error) error { return errors.Join(base, errors.New("sdk detail")) }

// idToken builds an unsigned JWT-shaped string with the given claims.
func idToken(claims map[string]any) string {
	b, _ := json.Marshal(claims)
	return "e30." + base64.RawURLEncoding.EncodeToString(b) + ".sig"
}

func TestRegister(t *testing.T) {
	tests := []struct {
		name      string
		cognito   error
		want      error
		wantAudit bool
	}{
		{"new user", nil, nil, true},
		{"existing email looks the same", cerr(cognito.ErrUserExists), nil, false},
		{"password policy", &cognito.InvalidPasswordError{Message: "Password did not conform with policy: Password must have symbol characters"}, &PasswordPolicyError{}, false},
		{"too many requests", cerr(cognito.ErrTooManyRequests), ErrRateLimited, false},
		{"invalid parameter", cerr(cognito.ErrInvalidParameter), &InvalidInputError{}, false},
		{"outage", cerr(cognito.ErrUnavailable), ErrUnavailable, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			audit := &fakeAudit{}
			svc := New(&fakeCognito{err: tt.cognito}, &fakeUsers{}, audit, nil)
			err := svc.Register(context.Background(), RegisterInput{Email: "a@b.co", Password: "x", DisplayName: "A"})
			assertErr(t, err, tt.want)
			if got := len(audit.entries) == 1 && audit.entries[0].Action == store.ActionRegister; got != tt.wantAudit {
				t.Errorf("audit = %v", audit.actions())
			}
		})
	}

	t.Run("policy message is trimmed", func(t *testing.T) {
		svc := New(&fakeCognito{err: &cognito.InvalidPasswordError{Message: "Password did not conform with policy: Password must have symbol characters"}}, &fakeUsers{}, &fakeAudit{}, nil)
		var pe *PasswordPolicyError
		if err := svc.Register(context.Background(), RegisterInput{}); !errors.As(err, &pe) || pe.Message != "Password must have symbol characters" {
			t.Fatalf("got %v", err)
		}
	})
}

func TestConfirm(t *testing.T) {
	tests := []struct {
		cognito error
		want    error
	}{
		{nil, nil},
		{cerr(cognito.ErrInvalidCode), ErrInvalidCode},
		{cerr(cognito.ErrExpiredCode), ErrExpiredCode},
		{cerr(cognito.ErrUserNotFound), ErrInvalidCode},  // no enumeration
		{cerr(cognito.ErrNotAuthorized), ErrInvalidCode}, // already confirmed
		{cerr(cognito.ErrTooManyRequests), ErrRateLimited},
		{cerr(cognito.ErrUnavailable), ErrUnavailable},
	}
	for _, tt := range tests {
		svc := New(&fakeCognito{err: tt.cognito}, &fakeUsers{}, &fakeAudit{}, nil)
		assertErr(t, svc.Confirm(context.Background(), "a@b.co", "123456"), tt.want)
	}
}

func TestResendCode(t *testing.T) {
	tests := []struct {
		cognito error
		want    error
	}{
		{nil, nil},
		{cerr(cognito.ErrUserNotFound), nil},
		{cerr(cognito.ErrInvalidParameter), nil}, // already confirmed
		{cerr(cognito.ErrTooManyRequests), ErrRateLimited},
		{cerr(cognito.ErrUnavailable), ErrUnavailable},
	}
	for _, tt := range tests {
		svc := New(&fakeCognito{err: tt.cognito}, &fakeUsers{}, &fakeAudit{}, nil)
		assertErr(t, svc.ResendCode(context.Background(), "a@b.co"), tt.want)
	}
}

func TestLoginSuccessCreatesProfileOnce(t *testing.T) {
	sub := "6f1c2a3b-0000-4000-8000-000000000001"
	tokens := cognito.Tokens{
		AccessToken: "at", RefreshToken: "rt", ExpiresIn: 3600,
		IDToken: idToken(map[string]any{"sub": sub, "name": "Diego"}),
	}
	users, audit, thr := &fakeUsers{}, &fakeAudit{}, &fakeThrottle{}
	svc := New(&fakeCognito{tokens: tokens}, users, audit, thr)

	for i := range 2 {
		got, err := svc.Login(context.Background(), LoginInput{Email: "a@b.co", Password: "pw"})
		if err != nil {
			t.Fatal(err)
		}
		if got.AccessToken != "at" || got.RefreshToken != "rt" || got.ExpiresIn != 3600 {
			t.Errorf("tokens = %+v", got)
		}
		e := audit.entries[i]
		if e.Action != store.ActionLoginSucceeded || e.ActorID != sub || e.Details["firstLogin"] != (i == 0) {
			t.Errorf("audit[%d] = %+v", i, e)
		}
	}
	if users.created[sub] != "Diego" {
		t.Errorf("profile = %v", users.created)
	}
	if thr.resets != 2 {
		t.Errorf("throttle resets = %d", thr.resets)
	}
}

func TestLoginFallbackDisplayName(t *testing.T) {
	users := &fakeUsers{}
	svc := New(&fakeCognito{tokens: cognito.Tokens{IDToken: idToken(map[string]any{"sub": "abcd1234-ef56-4000-8000-000000000000"})}}, users, &fakeAudit{}, nil)
	if _, err := svc.Login(context.Background(), LoginInput{}); err != nil {
		t.Fatal(err)
	}
	if got := users.created["abcd1234-ef56-4000-8000-000000000000"]; got != "user-abcd1234" {
		t.Errorf("fallback name = %q", got)
	}
}

func TestLoginFailures(t *testing.T) {
	tests := []struct {
		name         string
		cognito      error
		want         error
		wantFailures int
		wantAudit    bool
	}{
		{"wrong password", cerr(cognito.ErrNotAuthorized), ErrInvalidCredentials, 1, true},
		{"unknown email", cerr(cognito.ErrUserNotFound), ErrInvalidCredentials, 1, true},
		{"not confirmed", cerr(cognito.ErrNotConfirmed), ErrNotConfirmed, 0, true},
		{"cognito throttled", cerr(cognito.ErrTooManyRequests), ErrRateLimited, 0, false},
		{"outage", cerr(cognito.ErrUnavailable), ErrUnavailable, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			audit, thr := &fakeAudit{}, &fakeThrottle{}
			svc := New(&fakeCognito{err: tt.cognito}, &fakeUsers{}, audit, thr)
			_, err := svc.Login(context.Background(), LoginInput{Email: "a@b.co", Password: "pw"})
			assertErr(t, err, tt.want)
			if thr.failures != tt.wantFailures {
				t.Errorf("failures = %d", thr.failures)
			}
			if tt.wantAudit {
				if len(audit.entries) != 1 || audit.entries[0].Action != store.ActionLoginFailed || audit.entries[0].ActorID != "" {
					t.Errorf("audit = %+v", audit.entries)
				}
			}
		})
	}

	t.Run("throttled before calling Cognito", func(t *testing.T) {
		c := &fakeCognito{err: errors.New("must not be called")}
		svc := New(c, &fakeUsers{}, &fakeAudit{}, &fakeThrottle{block: NewRateLimitedError(30 * time.Second)})
		_, err := svc.Login(context.Background(), LoginInput{})
		var rl *RateLimitedError
		if !errors.As(err, &rl) || rl.RetryAfter != 30*time.Second {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("database down fails the login", func(t *testing.T) {
		svc := New(&fakeCognito{tokens: cognito.Tokens{IDToken: idToken(map[string]any{"sub": "s"})}},
			&fakeUsers{err: errors.New("db down")}, &fakeAudit{}, nil)
		_, err := svc.Login(context.Background(), LoginInput{})
		assertErr(t, err, ErrUnavailable)
	})

	t.Run("malformed ID token", func(t *testing.T) {
		svc := New(&fakeCognito{tokens: cognito.Tokens{IDToken: "garbage"}}, &fakeUsers{}, &fakeAudit{}, nil)
		_, err := svc.Login(context.Background(), LoginInput{})
		assertErr(t, err, ErrUnavailable)
	})
}

func TestRefresh(t *testing.T) {
	svc := New(&fakeCognito{tokens: cognito.Tokens{AccessToken: "at2", IDToken: "it2", ExpiresIn: 3600}}, &fakeUsers{}, &fakeAudit{}, nil)
	got, err := svc.Refresh(context.Background(), "rt")
	if err != nil || got.AccessToken != "at2" || got.RefreshToken != "" {
		t.Fatalf("got %+v %v", got, err)
	}
	for in, want := range map[error]error{
		cerr(cognito.ErrNotAuthorized):    ErrInvalidRefreshToken,
		cerr(cognito.ErrInvalidParameter): ErrInvalidRefreshToken,
		cerr(cognito.ErrUnavailable):      ErrUnavailable,
	} {
		_, err := New(&fakeCognito{err: in}, &fakeUsers{}, &fakeAudit{}, nil).Refresh(context.Background(), "rt")
		assertErr(t, err, want)
	}
}

func TestLogout(t *testing.T) {
	for _, tt := range []struct {
		cognito   error
		want      error
		wantAudit bool
	}{
		{nil, nil, true},
		{cerr(cognito.ErrNotAuthorized), nil, true}, // already revoked: idempotent
		{cerr(cognito.ErrInvalidParameter), ErrInvalidRefreshToken, false},
		{cerr(cognito.ErrUnavailable), ErrUnavailable, false},
	} {
		audit := &fakeAudit{}
		err := New(&fakeCognito{err: tt.cognito}, &fakeUsers{}, audit, nil).Logout(context.Background(), "sub-1", "rt")
		assertErr(t, err, tt.want)
		if got := len(audit.entries) == 1 && audit.entries[0].Action == store.ActionLogout && audit.entries[0].ActorID == "sub-1"; got != tt.wantAudit {
			t.Errorf("%v: audit = %+v", tt.cognito, audit.entries)
		}
	}
}

func assertErr(t *testing.T, got, want error) {
	t.Helper()
	switch w := want.(type) {
	case nil:
		if got != nil {
			t.Fatalf("unexpected error %v", got)
		}
	case *PasswordPolicyError:
		var pe *PasswordPolicyError
		if !errors.As(got, &pe) {
			t.Fatalf("got %v, want PasswordPolicyError", got)
		}
	case *InvalidInputError:
		var ie *InvalidInputError
		if !errors.As(got, &ie) {
			t.Fatalf("got %v, want InvalidInputError", got)
		}
	default:
		if !errors.Is(got, w) {
			t.Fatalf("got %v, want %v", got, w)
		}
	}
}
