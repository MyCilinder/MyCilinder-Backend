// Package cfsign issues CloudFront signed cookies for HLS playback (Interface Design
// §1.3).
//
// One custom policy covers https://{domain}/videos/{videoId}/*, so a single cookie set
// grants the master playlist, the variant playlists, and every segment. The SDK signs the
// policy; this package adds the attributes the SDK cannot set (Max-Age, Partitioned,
// SameSite=None) and scopes the cookies to the video's path.
//
// The signature uses SHA-1, the SDK default. With SHA-256 the SDK adds a fourth cookie,
// CloudFront-Hash-Algorithm, which the API contract does not allow (exactly 3 cookies).
package cfsign

import (
	"crypto"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/feature/cloudfront/sign"
)

// Lifetime is how long playback cookies are valid (BR-LIM).
const Lifetime = 2 * time.Hour

// Cookie names, in the order they are returned.
const (
	CookiePolicy    = sign.CookiePolicyName
	CookieSignature = sign.CookieSignatureName
	CookieKeyPairID = sign.CookieKeyIDName
)

// Signer issues playback cookies for one distribution and key pair.
type Signer struct {
	domain string
	signer *sign.CookieSigner
	now    func() time.Time
}

// Grant is a signed cookie set.
type Grant struct {
	Cookies           []*http.Cookie // CloudFront-Policy, -Signature, -Key-Pair-Id
	ExpiresAt         time.Time
	MasterPlaylistURL string
}

// LoadKey reads an RSA private key in PEM form, PKCS #1 ("RSA PRIVATE KEY") or PKCS #8
// ("PRIVATE KEY"). The SDK's LoadPEMPrivKeyFile accepts PKCS #1 only, while openssl and
// the AWS console produce either.
func LoadKey(path string) (*rsa.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cloudfront key: %w", err)
	}
	return ParseKey(b)
}

// ParseKey parses an RSA private key in PEM form, PKCS #1 or PKCS #8.
func ParseKey(pemBytes []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("cloudfront key: no PEM block found")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("cloudfront key: %w", err)
		}
		return k, nil
	case "PRIVATE KEY":
		k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("cloudfront key: %w", err)
		}
		rk, ok := k.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("cloudfront key: not an RSA key (CloudFront requires RSA)")
		}
		return rk, nil
	default:
		return nil, fmt.Errorf("cloudfront key: unexpected PEM block %q", block.Type)
	}
}

// New returns a Signer. domain is the distribution domain without a scheme
// (dxxxx.cloudfront.net) and keyPairID the public key id in the trusted key group.
func New(domain, keyPairID string, key crypto.Signer) *Signer {
	return &Signer{domain: domain, signer: sign.NewCookieSigner(keyPairID, key), now: time.Now}
}

// Sign issues the cookie set for one video.
func (s *Signer) Sign(videoID string) (Grant, error) {
	now := s.now()
	// Whole seconds: the policy stores epoch seconds, and expiresAt must match it.
	expires := now.Add(Lifetime).Truncate(time.Second)
	base := "https://" + s.domain + "/videos/" + videoID + "/"
	policy := &sign.Policy{Statements: []sign.Statement{{
		Resource:  base + "*",
		Condition: sign.Condition{DateLessThan: sign.NewAWSEpochTime(expires)},
	}}}
	cookies, err := s.signer.SignWithPolicy(policy)
	if err != nil {
		return Grant{}, fmt.Errorf("sign playback policy: %w", err)
	}
	if len(cookies) != 3 {
		return Grant{}, fmt.Errorf("sign playback policy: got %d cookies, want 3", len(cookies))
	}
	for _, c := range cookies {
		c.Path = "/videos/" + videoID + "/"
		c.Domain = "" // host-only: the API and media share the CloudFront domain
		c.Expires = time.Time{}
		c.MaxAge = int(Lifetime / time.Second)
		c.Secure = true
		c.HttpOnly = true
		c.SameSite = http.SameSiteNoneMode
		c.Partitioned = true
	}
	return Grant{Cookies: cookies, ExpiresAt: expires, MasterPlaylistURL: base + "master.m3u8"}, nil
}
