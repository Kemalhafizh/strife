package server

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeIssuer is an OIDC issuer: discovery, and a key set it can rotate.
type fakeIssuer struct {
	srv     *httptest.Server
	mu      sync.Mutex
	keys    map[string]crypto.Signer
	fetches int
}

func newIssuer(t *testing.T) *fakeIssuer {
	f := &fakeIssuer{keys: map[string]crypto.Signer{}}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(map[string]string{"issuer": f.srv.URL, "jwks_uri": f.srv.URL + "/keys"})
		case "/keys":
			f.mu.Lock()
			defer f.mu.Unlock()
			f.fetches++
			var keys []map[string]string
			for kid, k := range f.keys {
				switch pub := k.Public().(type) {
				case *rsa.PublicKey:
					keys = append(keys, map[string]string{"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
						"n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes())})
				case *ecdsa.PublicKey:
					keys = append(keys, map[string]string{"kty": "EC", "kid": kid, "crv": "P-256",
						"x": b64(pub.X.FillBytes(make([]byte, 32))), "y": b64(pub.Y.FillBytes(make([]byte, 32)))})
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIssuer) publish(kid string, k crypto.Signer) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[kid] = k
}

func (f *fakeIssuer) fetched() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetches
}

func b64(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func rsaKey(t *testing.T) *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// mint signs claims as alg with key: an RSA or ECDSA key, an HMAC secret,
// or nil for an unsigned token.
func mint(t *testing.T, alg, kid string, key any, claims map[string]any) string {
	t.Helper()
	hdr := map[string]string{"alg": alg, "typ": "JWT"}
	if kid != "" {
		hdr["kid"] = kid
	}
	h, _ := json.Marshal(hdr)
	c, _ := json.Marshal(claims)
	signed := b64(h) + "." + b64(c)
	digest := sha256.Sum256([]byte(signed))
	var sig []byte
	switch k := key.(type) {
	case *rsa.PrivateKey:
		s, err := rsa.SignPKCS1v15(rand.Reader, k, crypto.SHA256, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		sig = s
	case *ecdsa.PrivateKey:
		r, s, err := ecdsa.Sign(rand.Reader, k, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		sig = append(r.FillBytes(make([]byte, 32)), s.FillBytes(make([]byte, 32))...)
	case []byte:
		m := hmac.New(sha256.New, k)
		m.Write([]byte(signed))
		sig = m.Sum(nil)
	}
	return signed + "." + b64(sig)
}

// githubClaims are the claims of a GitHub Actions job on acme/payments'
// main branch at now, changed by over (a nil value removes a claim).
func githubClaims(iss string, now time.Time, over map[string]any) map[string]any {
	c := map[string]any{
		"iss": iss, "aud": "strife", "sub": "repo:acme/payments:ref:refs/heads/main",
		"repository": "acme/payments", "repository_id": "123456", "ref": "refs/heads/main",
		"iat": now.Unix(), "nbf": now.Add(-10 * time.Second).Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	}
	for k, v := range over {
		if v == nil {
			delete(c, k)
		} else {
			c[k] = v
		}
	}
	return c
}

// A CI job pushes with its platform's identity token instead of a stored
// secret, and only the jobs the project names get in.
func TestOIDCPush(t *testing.T) {
	iss := newIssuer(t)
	k1 := rsaKey(t)
	iss.publish("k1", k1)
	h := newHarness(t, func(c *Config) {
		c.Projects[0].Token = "" // no shared write token at all
		c.Projects[0].OIDC = []OIDCRule{
			{Issuer: iss.srv.URL, Audience: "strife", Claims: map[string]string{"repository_id": "123456", "ref": "refs/heads/main"}},
			{Issuer: iss.srv.URL, Audience: "strife", Claims: map[string]string{"repository_id": "123456", "ref": "refs/heads/*"}, Access: "read"},
		}
	})
	h.srv.client = iss.srv.Client()
	clock := time.Now()
	h.srv.now = func() time.Time { return clock }

	seed := uint64(0)
	push := func(tok string) (int, PushResult) {
		t.Helper()
		seed++
		_, data, hist := liveReport(t, "etcd-3", false, seed)
		code, body := h.do("POST", "/api/v1/projects/payments/runs", tok, Upload{Report: data, History: hist})
		var res PushResult
		_ = json.Unmarshal(body, &res)
		return code, res
	}

	good := mint(t, "RS256", "k1", k1, githubClaims(iss.srv.URL, clock, nil))
	code, res := push(good)
	host := strings.TrimPrefix(iss.srv.URL, "https://")
	if code != http.StatusCreated || res.Run.Pusher != "repo:acme/payments:ref:refs/heads/main ("+host+")" {
		t.Fatalf("push with a good token: %d %+v", code, res.Run)
	}

	other := rsaKey(t)
	hs := []byte("the key set is public, so HMAC with it proves nothing")
	for name, tok := range map[string]string{
		"wrong audience":        mint(t, "RS256", "k1", k1, githubClaims(iss.srv.URL, clock, map[string]any{"aud": "someone-else"})),
		"audience list without": mint(t, "RS256", "k1", k1, githubClaims(iss.srv.URL, clock, map[string]any{"aud": []string{"a", "b"}})),
		"expired":               mint(t, "RS256", "k1", k1, githubClaims(iss.srv.URL, clock, map[string]any{"exp": clock.Add(-2 * time.Minute).Unix()})),
		"no expiry":             mint(t, "RS256", "k1", k1, githubClaims(iss.srv.URL, clock, map[string]any{"exp": nil})),
		"not yet valid":         mint(t, "RS256", "k1", k1, githubClaims(iss.srv.URL, clock, map[string]any{"nbf": clock.Add(5 * time.Minute).Unix()})),
		"another branch":        mint(t, "RS256", "k1", k1, githubClaims(iss.srv.URL, clock, map[string]any{"ref": "refs/heads/feature"})),
		"another repository":    mint(t, "RS256", "k1", k1, githubClaims(iss.srv.URL, clock, map[string]any{"repository_id": "999"})),
		"claim missing":         mint(t, "RS256", "k1", k1, githubClaims(iss.srv.URL, clock, map[string]any{"repository_id": nil})),
		"another issuer":        mint(t, "RS256", "k1", k1, githubClaims("https://evil.example", clock, nil)),
		"forged signature":      mint(t, "RS256", "k1", other, githubClaims(iss.srv.URL, clock, nil)),
		"alg none":              mint(t, "none", "k1", nil, githubClaims(iss.srv.URL, clock, nil)),
		"HMAC":                  mint(t, "HS256", "k1", hs, githubClaims(iss.srv.URL, clock, nil)),
		"garbage":               "eyJhbGciOiJSUzI1NiJ9.not-base64!.x",
	} {
		if code, _ := push(tok); code != http.StatusUnauthorized {
			t.Errorf("%s: push answered %d", name, code)
		}
	}

	// A feature branch may read (the read rule) but not push.
	feature := mint(t, "RS256", "k1", k1, githubClaims(iss.srv.URL, clock, map[string]any{"ref": "refs/heads/feature"}))
	if code, _ := h.do("GET", "/api/v1/projects/payments/runs", feature, nil); code != http.StatusOK {
		t.Fatalf("read with a read-only identity: %d", code)
	}
	if code, _ := h.do("GET", "/api/v1/projects/payments/runs", mint(t, "RS256", "k1", k1, githubClaims(iss.srv.URL, clock, map[string]any{"repository_id": "1"})), nil); code != http.StatusUnauthorized {
		t.Fatalf("read with a stranger's identity: %d", code)
	}

	// The issuer rotates its keys: a token from the new key gets the key
	// set fetched again, but an unknown key at most once a minute.
	if n := iss.fetched(); n != 1 {
		t.Fatalf("the key set was fetched %d times, want once", n)
	}
	iss.publish("k2", rsaKey(t))
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	iss.publish("e1", ec)
	if code, _ := push(mint(t, "ES256", "e1", ec, githubClaims(iss.srv.URL, clock, nil))); code != http.StatusUnauthorized || iss.fetched() != 1 {
		t.Fatalf("an unknown key within a minute: %d, %d fetches", code, iss.fetched())
	}
	clock = clock.Add(2 * time.Minute)
	if code, res := push(mint(t, "ES256", "e1", ec, githubClaims(iss.srv.URL, clock, nil))); code != http.StatusCreated || iss.fetched() != 2 {
		t.Fatalf("an ES256 token from a new key: %d %+v, %d fetches", code, res.Run, iss.fetched())
	}
	if code, _ := push(mint(t, "RS256", "k1", k1, githubClaims(iss.srv.URL, clock, nil))); code != http.StatusCreated {
		t.Fatalf("the old key after the rotation: %d", code)
	}
	if code, _ := push(mint(t, "RS256", "nope", k1, githubClaims(iss.srv.URL, clock, nil))); code != http.StatusUnauthorized || iss.fetched() != 2 {
		t.Fatalf("a key nobody published: %d, %d fetches", code, iss.fetched())
	}
	// A shared token was never configured.
	if code, _ := push(writeTok); code != http.StatusUnauthorized {
		t.Fatalf("the old shared token: %d", code)
	}
}

func TestOIDCConfig(t *testing.T) {
	rule := OIDCRule{Issuer: "https://token.actions.githubusercontent.com", Audience: "strife", Claims: map[string]string{"repository_id": "1"}}
	with := func(f func(*OIDCRule)) []ProjectConfig {
		r := rule
		f(&r)
		return []ProjectConfig{{Name: "a", OIDC: []OIDCRule{r}}}
	}
	for name, ps := range map[string][]ProjectConfig{
		"read only, no token": with(func(r *OIDCRule) { r.Access = "read" }),
		"no claims":           with(func(r *OIDCRule) { r.Claims = nil }),
		"plain http issuer":   with(func(r *OIDCRule) { r.Issuer = "http://issuer" }),
		"no audience":         with(func(r *OIDCRule) { r.Audience = "" }),
		"bad access":          with(func(r *OIDCRule) { r.Access = "admin" }),
		"bad pattern":         with(func(r *OIDCRule) { r.Claims = map[string]string{"ref": "refs/["} }),
		"bad jwks":            with(func(r *OIDCRule) { r.JWKS = "http://keys" }),
	} {
		if _, err := New(&Config{Data: t.TempDir(), Projects: ps}, Options{}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := New(&Config{Data: t.TempDir(), Projects: with(func(*OIDCRule) {})}, Options{}); err != nil {
		t.Fatalf("a project with only an OIDC write rule: %v", err)
	}
}
