// Package jwtauthtest issues Cognito-shaped tokens signed by a throwaway RSA key and
// serves the matching JWKS, so tests can exercise jwtauth without a real user pool.
package jwtauthtest

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lestrrat-go/jwx/v3/jwa"
	"github.com/lestrrat-go/jwx/v3/jwk"
	"github.com/lestrrat-go/jwx/v3/jwt"
)

// Issuer signs tokens and serves /.well-known/jwks.json.
type Issuer struct {
	Server   *httptest.Server
	ClientID string
	key      jwk.Key
}

// New starts the JWKS server. It is closed when the test ends.
func New(t testing.TB, clientID string) *Issuer {
	t.Helper()
	raw, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	priv, err := jwk.Import(raw)
	if err != nil {
		t.Fatal(err)
	}
	_ = priv.Set(jwk.KeyIDKey, "test-key-1")
	_ = priv.Set(jwk.AlgorithmKey, jwa.RS256())

	pub, err := priv.PublicKey()
	if err != nil {
		t.Fatal(err)
	}
	set := jwk.NewSet()
	_ = set.AddKey(pub)
	body, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return &Issuer{Server: srv, ClientID: clientID, key: priv}
}

// IssuerURL is the iss value of issued tokens.
func (i *Issuer) IssuerURL() string { return i.Server.URL }

// JWKSURL is where the public key is served.
func (i *Issuer) JWKSURL() string { return i.Server.URL + "/.well-known/jwks.json" }

// Options adjusts an issued token.
type Options struct {
	TokenUse string        // default "access"
	ClientID string        // default the issuer's ClientID
	TTL      time.Duration // default 1h; negative for an expired token
	Groups   []string
	Name     string // ID tokens only
	Issuer   string // default IssuerURL
}

// Token returns a signed token for sub.
func (i *Issuer) Token(t testing.TB, sub string, o Options) string {
	t.Helper()
	if o.TokenUse == "" {
		o.TokenUse = "access"
	}
	if o.ClientID == "" {
		o.ClientID = i.ClientID
	}
	if o.TTL == 0 {
		o.TTL = time.Hour
	}
	if o.Issuer == "" {
		o.Issuer = i.IssuerURL()
	}
	now := time.Now()
	b := jwt.NewBuilder().
		Issuer(o.Issuer).
		Subject(sub).
		IssuedAt(now.Add(-time.Minute)).
		Expiration(now.Add(o.TTL)).
		Claim("token_use", o.TokenUse)
	if o.TokenUse == "access" {
		b = b.Claim("client_id", o.ClientID)
	} else {
		b = b.Audience([]string{o.ClientID})
	}
	if len(o.Groups) > 0 {
		b = b.Claim("cognito:groups", o.Groups)
	}
	if o.Name != "" {
		b = b.Claim("name", o.Name)
	}
	tok, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	signed, err := jwt.Sign(tok, jwt.WithKey(jwa.RS256(), i.key))
	if err != nil {
		t.Fatal(err)
	}
	return string(signed)
}
