package cfsign

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"strings"
	"testing"
	"time"
)

const vid = "8d0c5e2a-3b4f-4a8e-9c1d-2f6b7a8e9d01"

func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// cfDecode reverses CloudFront's URL-safe base64 (+ -> -, = -> _, / -> ~).
func cfDecode(t *testing.T, s string) []byte {
	t.Helper()
	s = strings.NewReplacer("-", "+", "_", "=", "~", "/").Replace(s)
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return b
}

func TestSign(t *testing.T) {
	key := testKey(t)
	s := New("d123.cloudfront.net", "K2TEST", key)
	now := time.Date(2026, 11, 22, 20, 0, 0, 500_000_000, time.UTC)
	s.now = func() time.Time { return now }

	g, err := s.Sign(vid)
	if err != nil {
		t.Fatal(err)
	}
	if g.MasterPlaylistURL != "https://d123.cloudfront.net/videos/"+vid+"/master.m3u8" {
		t.Errorf("master = %s", g.MasterPlaylistURL)
	}
	if want := time.Date(2026, 11, 22, 22, 0, 0, 0, time.UTC); !g.ExpiresAt.Equal(want) {
		t.Errorf("expiresAt = %v, want %v", g.ExpiresAt, want)
	}

	if len(g.Cookies) != 3 {
		t.Fatalf("%d cookies, want 3", len(g.Cookies))
	}
	byName := map[string]*http.Cookie{}
	for _, c := range g.Cookies {
		byName[c.Name] = c
		if c.Path != "/videos/"+vid+"/" || !c.Secure || !c.HttpOnly || c.SameSite != http.SameSiteNoneMode ||
			!c.Partitioned || c.MaxAge != 7200 || c.Domain != "" || !c.Expires.IsZero() {
			t.Errorf("cookie %s attributes: %+v", c.Name, c)
		}
		line := c.String()
		for _, part := range []string{"Path=/videos/" + vid + "/", "Max-Age=7200", "HttpOnly", "Secure", "SameSite=None", "Partitioned"} {
			if !strings.Contains(line, part) {
				t.Errorf("Set-Cookie %q lacks %s", line, part)
			}
		}
	}
	if byName[CookieKeyPairID].Value != "K2TEST" {
		t.Errorf("key pair id = %q", byName[CookieKeyPairID].Value)
	}

	// The policy JSON: one statement for the video prefix, expiring at expiresAt.
	policyJSON := cfDecode(t, byName[CookiePolicy].Value)
	var p struct {
		Statement []struct {
			Resource  string
			Condition struct {
				DateLessThan struct {
					Epoch int64 `json:"AWS:EpochTime"`
				}
			}
		}
	}
	if err := json.Unmarshal(policyJSON, &p); err != nil {
		t.Fatalf("policy %s: %v", policyJSON, err)
	}
	if len(p.Statement) != 1 || p.Statement[0].Resource != "https://d123.cloudfront.net/videos/"+vid+"/*" ||
		p.Statement[0].Condition.DateLessThan.Epoch != g.ExpiresAt.Unix() {
		t.Errorf("policy = %s", policyJSON)
	}

	// The signature verifies against the public key (RSA PKCS #1 v1.5, SHA-1).
	sig := cfDecode(t, byName[CookieSignature].Value)
	sum := sha1.Sum(policyJSON)
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA1, sum[:], sig); err != nil {
		t.Errorf("signature does not verify: %v", err)
	}
	// And not against another key.
	if err := rsa.VerifyPKCS1v15(&testKey(t).PublicKey, crypto.SHA1, sum[:], sig); err == nil {
		t.Error("signature verified with the wrong key")
	}
}

func TestParseKey(t *testing.T) {
	key := testKey(t)
	pkcs1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	der8, _ := x509.MarshalPKCS8PrivateKey(key)
	pkcs8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der8})
	for name, b := range map[string][]byte{"pkcs1": pkcs1, "pkcs8": pkcs8} {
		got, err := ParseKey(b)
		if err != nil || !got.Equal(key) {
			t.Errorf("%s: %v", name, err)
		}
	}
	pub := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&key.PublicKey)})
	for name, b := range map[string][]byte{"empty": nil, "garbage": []byte("nope"), "public key": pub,
		"bad body": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("x")})} {
		if _, err := ParseKey(b); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
