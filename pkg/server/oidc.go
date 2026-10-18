package server

import (
	"context"
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Workload identity: a CI job proves who it is with the short-lived OIDC
// token its platform mints for it (GitHub Actions, GitLab CI, Buildkite…)
// instead of holding a shared write token. A project trusts a token when
// an issuer it names signed it, for its audience, with claims that match
// its patterns: "this repository, on its main branch".

// OIDCRule admits the tokens of one issuer whose claims match.
type OIDCRule struct {
	// Issuer is the token issuer, exactly as in its "iss" claim, e.g.
	// https://token.actions.githubusercontent.com.
	Issuer string `json:"issuer"`
	// Audience the token must be minted for (its "aud"). On GitHub it is
	// what the job asks for: strife push --token github-oidc:AUDIENCE.
	Audience string `json:"audience"`
	// Claims the token must carry, each matched as a path.Match pattern
	// against the claim's value: "refs/heads/*" matches every branch
	// without a slash. At least one is required, or every job on the
	// issuer could push. GitHub's repository_id and repository_owner_id
	// survive a repository being renamed or recreated by someone else.
	Claims map[string]string `json:"claims"`
	// Access is "write" (push and read, the default) or "read".
	Access string `json:"access,omitempty"`
	// JWKS replaces discovery with the issuer's keys: an https:// URL, or
	// file:PATH for a registry that cannot reach the issuer.
	JWKS string `json:"jwks,omitempty"`
}

// oidcRule is an OIDCRule with its issuer's key cache.
type oidcRule struct {
	OIDCRule
	write  bool
	issuer *issuer
}

// clockSkew is how far the registry's clock and an issuer's may disagree.
const clockSkew = time.Minute

// refetchEvery bounds how often an issuer's keys are fetched again for a
// token signed by a key the registry has not seen (a rotation, or junk).
const refetchEvery = time.Minute

// maxJWKS bounds a discovery document or key set.
const maxJWKS = 1 << 20

// issuer fetches and caches the signing keys of one OIDC issuer.
type issuer struct {
	url  string // the issuer, as in "iss"
	jwks string // an explicit key set: https:// URL or file:PATH; "" discovers it

	mu      sync.Mutex
	keys    map[string]jwk // by key ID
	fetched time.Time
}

type jwk struct {
	alg string // "" when the key set does not say
	key crypto.PublicKey
}

// issuers shares one key cache per issuer and key set across projects.
type issuers struct {
	mu sync.Mutex
	m  map[string]*issuer
}

func (is *issuers) get(url, jwks string) *issuer {
	is.mu.Lock()
	defer is.mu.Unlock()
	if is.m == nil {
		is.m = map[string]*issuer{}
	}
	k := url + "\x00" + jwks
	if is.m[k] == nil {
		is.m[k] = &issuer{url: url, jwks: jwks}
	}
	return is.m[k]
}

// checkRule validates a rule from the configuration.
func checkRule(r OIDCRule) error {
	switch {
	case !strings.HasPrefix(r.Issuer, "https://"):
		return fmt.Errorf("oidc: issuer %q must be an https:// URL", r.Issuer)
	case r.Audience == "":
		return fmt.Errorf("oidc: issuer %s: an audience is required", r.Issuer)
	case len(r.Claims) == 0:
		return fmt.Errorf("oidc: issuer %s: at least one claim is required, or every token the issuer mints would be accepted", r.Issuer)
	case r.Access != "" && r.Access != "read" && r.Access != "write":
		return fmt.Errorf("oidc: issuer %s: access is \"read\" or \"write\", not %q", r.Issuer, r.Access)
	case r.JWKS != "" && !strings.HasPrefix(r.JWKS, "https://") && !strings.HasPrefix(r.JWKS, "file:"):
		return fmt.Errorf("oidc: issuer %s: jwks is an https:// URL or file:PATH", r.Issuer)
	}
	for k, p := range r.Claims {
		if _, err := path.Match(p, ""); err != nil {
			return fmt.Errorf("oidc: issuer %s: claim %s: bad pattern %q", r.Issuer, k, p)
		}
	}
	return nil
}

// identity is who a verified token says pushed or read: its subject at
// its issuer.
type identity struct {
	issuer, subject string
}

func (id identity) String() string {
	host := strings.TrimPrefix(id.issuer, "https://")
	return id.subject + " (" + host + ")"
}

// looksLikeJWT tells a JWT from a shared token without trying to verify
// it: three base64url segments, the first a JSON object.
func looksLikeJWT(t string) bool {
	return strings.Count(t, ".") == 2 && strings.HasPrefix(t, "eyJ")
}

// oidcAccess returns the identity in tok when one of p's rules admits it
// for the access asked (write implies read).
func (s *Server) oidcAccess(ctx context.Context, p *project, tok string, write bool) (identity, bool) {
	if len(p.oidc) == 0 || !looksLikeJWT(tok) {
		return identity{}, false
	}
	t, err := parseJWT(tok)
	if err != nil {
		return identity{}, false
	}
	iss, _ := t.claims["iss"].(string)
	now := s.now()
	for _, r := range p.oidc {
		if r.Issuer != iss || (write && !r.write) {
			continue
		}
		if err := r.issuer.verify(ctx, s.client, t, now); err != nil {
			s.logf("%s: oidc token from %s refused: %v", p.name, iss, err)
			continue
		}
		if err := r.admits(t.claims, now); err != nil {
			s.logf("%s: oidc token from %s refused: %v", p.name, iss, err)
			continue
		}
		sub, _ := t.claims["sub"].(string)
		return identity{issuer: iss, subject: sub}, true
	}
	return identity{}, false
}

// admits checks a verified token's claims against the rule.
func (r *oidcRule) admits(c map[string]any, now time.Time) error {
	if iss, _ := c["iss"].(string); iss != r.Issuer {
		return fmt.Errorf("issuer %q", iss)
	}
	if !hasAudience(c["aud"], r.Audience) {
		return fmt.Errorf("audience %v, want %q", c["aud"], r.Audience)
	}
	exp, ok := numericDate(c["exp"])
	if !ok {
		return errors.New("no expiry")
	}
	if now.After(exp.Add(clockSkew)) {
		return fmt.Errorf("expired at %s", exp.UTC().Format(time.RFC3339))
	}
	if nbf, ok := numericDate(c["nbf"]); ok && now.Add(clockSkew).Before(nbf) {
		return fmt.Errorf("not valid before %s", nbf.UTC().Format(time.RFC3339))
	}
	for k, pattern := range r.Claims {
		v, ok := claimString(c[k])
		if !ok {
			return fmt.Errorf("no %s claim", k)
		}
		if m, _ := path.Match(pattern, v); !m {
			return fmt.Errorf("%s %q does not match %q", k, v, pattern)
		}
	}
	return nil
}

func hasAudience(aud any, want string) bool {
	switch a := aud.(type) {
	case string:
		return a == want
	case []any:
		for _, x := range a {
			if x == want {
				return true
			}
		}
	}
	return false
}

func numericDate(v any) (time.Time, bool) {
	f, ok := v.(float64)
	if !ok {
		return time.Time{}, false
	}
	return time.Unix(int64(f), 0), true
}

// claimString is a claim's value as text: strings as they are, numbers in
// decimal, booleans as true or false. Objects and lists never match.
func claimString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}

// jwt is a parsed, not yet verified, compact JWS.
type jwt struct {
	alg, kid string
	signed   []byte // header.payload, what the signature covers
	sig      []byte
	claims   map[string]any
}

func parseJWT(tok string) (*jwt, error) {
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return nil, errors.New("not a JWS")
	}
	var hdr struct {
		Alg  string `json:"alg"`
		Kid  string `json:"kid"`
		Crit []any  `json:"crit"`
	}
	if err := decodeSegment(parts[0], &hdr); err != nil {
		return nil, fmt.Errorf("header: %w", err)
	}
	if len(hdr.Crit) > 0 {
		return nil, errors.New("critical header extensions are not supported")
	}
	t := &jwt{alg: hdr.Alg, kid: hdr.Kid, signed: []byte(parts[0] + "." + parts[1])}
	if err := decodeSegment(parts[1], &t.claims); err != nil {
		return nil, fmt.Errorf("claims: %w", err)
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("signature: %w", err)
	}
	t.sig = sig
	return t, nil
}

func decodeSegment(seg string, v any) error {
	data, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// verify checks t's signature with the issuer's key it names. Only
// asymmetric algorithms are accepted: "none" and HMAC would let anyone
// who knows the key set (it is public) mint tokens.
func (is *issuer) verify(ctx context.Context, client *http.Client, t *jwt, now time.Time) error {
	if t.alg != "RS256" && t.alg != "ES256" {
		return fmt.Errorf("algorithm %q (RS256 or ES256 only)", t.alg)
	}
	k, err := is.key(ctx, client, t.kid, now)
	if err != nil {
		return err
	}
	if k.alg != "" && k.alg != t.alg {
		return fmt.Errorf("key %s is for %s, the token says %s", t.kid, k.alg, t.alg)
	}
	h := sha256.Sum256(t.signed)
	switch pub := k.key.(type) {
	case *rsa.PublicKey:
		if t.alg != "RS256" {
			break
		}
		if rsa.VerifyPKCS1v15(pub, crypto.SHA256, h[:], t.sig) != nil {
			return errors.New("bad signature")
		}
		return nil
	case *ecdsa.PublicKey:
		if t.alg != "ES256" || len(t.sig) != 64 {
			break
		}
		r, s := new(big.Int).SetBytes(t.sig[:32]), new(big.Int).SetBytes(t.sig[32:])
		if !ecdsa.Verify(pub, h[:], r, s) {
			return errors.New("bad signature")
		}
		return nil
	}
	return fmt.Errorf("key %s does not fit %s", t.kid, t.alg)
}

// key returns the issuer's key kid, fetching the key set when it is not
// cached, at most once per refetchEvery.
func (is *issuer) key(ctx context.Context, client *http.Client, kid string, now time.Time) (jwk, error) {
	is.mu.Lock()
	defer is.mu.Unlock()
	if k, ok := is.lookup(kid); ok {
		return k, nil
	}
	if !is.fetched.IsZero() && now.Sub(is.fetched) < refetchEvery {
		return jwk{}, fmt.Errorf("unknown key %q", kid)
	}
	is.fetched = now
	keys, err := is.fetch(ctx, client)
	if err != nil {
		return jwk{}, fmt.Errorf("fetching the keys of %s: %w", is.url, err)
	}
	is.keys = keys
	if k, ok := is.lookup(kid); ok {
		return k, nil
	}
	return jwk{}, fmt.Errorf("unknown key %q", kid)
}

// lookup finds kid; a token without one may use the only key there is.
func (is *issuer) lookup(kid string) (jwk, bool) {
	if k, ok := is.keys[kid]; ok {
		return k, true
	}
	if kid == "" && len(is.keys) == 1 {
		for _, k := range is.keys {
			return k, true
		}
	}
	return jwk{}, false
}

func (is *issuer) fetch(ctx context.Context, client *http.Client) (map[string]jwk, error) {
	src := is.jwks
	if src == "" {
		var disc struct {
			Issuer  string `json:"issuer"`
			JWKSURI string `json:"jwks_uri"`
		}
		if err := getJSON(ctx, client, strings.TrimRight(is.url, "/")+"/.well-known/openid-configuration", &disc); err != nil {
			return nil, err
		}
		if disc.Issuer != is.url {
			return nil, fmt.Errorf("discovery names issuer %q", disc.Issuer)
		}
		if !strings.HasPrefix(disc.JWKSURI, "https://") {
			return nil, fmt.Errorf("discovery names key set %q, not an https:// URL", disc.JWKSURI)
		}
		src = disc.JWKSURI
	}
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			Alg string `json:"alg"`
			N   string `json:"n"`
			E   string `json:"e"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if file, ok := strings.CutPrefix(src, "file:"); ok {
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		if err := json.Unmarshal(data, &set); err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
	} else if err := getJSON(ctx, client, src, &set); err != nil {
		return nil, err
	}
	keys := map[string]jwk{}
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		var pub crypto.PublicKey
		switch k.Kty {
		case "RSA":
			n, err1 := base64.RawURLEncoding.DecodeString(k.N)
			e, err2 := base64.RawURLEncoding.DecodeString(k.E)
			if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 {
				continue
			}
			key := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
			if key.N.BitLen() < 2048 || key.E < 3 {
				continue
			}
			pub = key
		case "EC":
			if k.Crv != "P-256" {
				continue
			}
			x, err1 := base64.RawURLEncoding.DecodeString(k.X)
			y, err2 := base64.RawURLEncoding.DecodeString(k.Y)
			if err1 != nil || err2 != nil || len(x) != 32 || len(y) != 32 {
				continue
			}
			// ecdh refuses points off the curve; ecdsa would not check.
			if _, err := ecdh.P256().NewPublicKey(append(append([]byte{4}, x...), y...)); err != nil {
				continue
			}
			pub = &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		default:
			continue
		}
		keys[k.Kid] = jwk{alg: k.Alg, key: pub}
	}
	if len(keys) == 0 {
		return nil, errors.New("no usable signing keys (RSA of 2048 bits or more, or EC P-256)")
	}
	return keys, nil
}

func getJSON(ctx context.Context, client *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: status %d", url, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJWKS+1))
	if err != nil {
		return err
	}
	if len(data) > maxJWKS {
		return fmt.Errorf("%s: larger than %d bytes", url, maxJWKS)
	}
	return json.Unmarshal(data, v)
}
