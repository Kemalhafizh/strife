package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kemalhafizh/strife/pkg/server"
)

// TestPushToServer runs the registry in-process and pushes a signed
// simulation and a campaign to it with the CLI.
func TestPushToServer(t *testing.T) {
	dir := t.TempDir()
	if code, out := runCLI(t, "keygen", "--out", filepath.Join(dir, "ci")); code != exitOK {
		t.Fatalf("keygen: %s", out)
	}
	t.Setenv("STRIFE_TOKEN", "cli-test-token-0123456789")
	srv, err := server.New(&server.Config{
		Data: filepath.Join(dir, "data"),
		Projects: []server.ProjectConfig{{
			Name: "core", Token: "env:STRIFE_TOKEN", TrustedKeys: []string{filepath.Join(dir, "ci.pub")}, RequireSignature: false,
		}},
	}, server.Options{})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()

	out := filepath.Join(dir, "out")
	if code, o := runCLI(t, "sim", "--system", "raft", "--seed", "3", "--duration", "2s", "--out", out); code != exitOK {
		t.Fatalf("sim: %s", o)
	}
	run := filepath.Join(out, "raft-seed3")
	if code, o := runCLI(t, "sign", "--report", filepath.Join(run, "report.json"), "--key", filepath.Join(dir, "ci.key"), "--signer", "ci", "--out", filepath.Join(run, "bundle.json")); code != exitOK {
		t.Fatalf("sign: %s", o)
	}
	code, o := runCLI(t, "push", "--server", hs.URL, "--project", "core", "--commit", "c0ffee", run)
	if code != exitOK || !strings.Contains(o, "stored simulation raft: linearizable, signed by ci, change: new") || !strings.Contains(o, "/ui/core/runs/") {
		t.Fatalf("push: exit %d\n%s", code, o)
	}
	// A report.json path pushes its directory; the same run is not stored twice.
	if code, o := runCLI(t, "push", "--server", hs.URL, "--project", "core", filepath.Join(run, "report.json")); code != exitOK || !strings.Contains(o, "already stored") {
		t.Fatalf("second push: exit %d\n%s", code, o)
	}
	// A wrong token is refused with the server's reason.
	t.Setenv("WRONG", "not-the-token-at-all-000")
	if code, o := runCLI(t, "push", "--server", hs.URL, "--project", "core", "--token", "env:WRONG", run); code != exitError || !strings.Contains(o, "401") {
		t.Fatalf("wrong token: exit %d\n%s", code, o)
	}
	// A tampered report is refused.
	data, _ := os.ReadFile(filepath.Join(run, "report.json"))
	tampered := filepath.Join(dir, "tampered")
	_ = os.MkdirAll(tampered, 0o755)
	_ = os.WriteFile(filepath.Join(tampered, "report.json"), []byte(strings.Replace(string(data), `"seed": 3`, `"seed": 4`, 1)), 0o644)
	if code, o := runCLI(t, "push", "--server", hs.URL, "--project", "core", tampered); code != exitError || !strings.Contains(o, "fingerprint") {
		t.Fatalf("tampered: exit %d\n%s", code, o)
	}
	// A campaign manifest.
	camp := filepath.Join(dir, "camp")
	if code, o := runCLI(t, "campaign", "--system", "raft-stale-reads", "--faults", "partition", "--seeds", "1..4", "--duration", "2s", "--out", camp); code != exitOK && code != exitViolation {
		t.Fatalf("campaign: exit %d\n%s", code, o)
	}
	if code, o := runCLI(t, "push", "--server", hs.URL, "--project", "core", "--fail-on", "none", filepath.Join(camp, "campaign.json")); code != exitOK || !strings.Contains(o, "stored campaign raft-stale-reads") {
		t.Fatalf("campaign push: exit %d\n%s", code, o)
	}
}

// TestPushWithGitHubOIDC pushes with the identity token GitHub Actions
// gives a job, to a registry that trusts the job's repository and holds
// no shared token; the issuer's keys come from a file, as in a registry
// that cannot reach GitHub.
func TestPushWithGitHubOIDC(t *testing.T) {
	dir := t.TempDir()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	enc := base64.RawURLEncoding.EncodeToString
	jwks, _ := json.Marshal(map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": "gh", "n": enc(key.N.Bytes()), "e": enc(big.NewInt(int64(key.E)).Bytes()),
	}}})
	_ = os.WriteFile(filepath.Join(dir, "jwks.json"), jwks, 0o644)
	const issuer = "https://token.actions.githubusercontent.com"

	// GitHub's token service: the job's request token in, a JWT for the
	// audience asked out.
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer job-request-token" || r.URL.Query().Get("api-version") != "2.0" {
			http.Error(w, "no", http.StatusForbidden)
			return
		}
		hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "kid": "gh"})
		claims, _ := json.Marshal(map[string]any{
			"iss": issuer, "aud": r.URL.Query().Get("audience"), "sub": "repo:acme/core:ref:refs/heads/main",
			"repository_id": "42", "exp": time.Now().Add(5 * time.Minute).Unix(),
		})
		signed := enc(hdr) + "." + enc(claims)
		digest := sha256.Sum256([]byte(signed))
		sig, _ := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
		_ = json.NewEncoder(w).Encode(map[string]string{"value": signed + "." + enc(sig)})
	}))
	defer tokens.Close()

	srv, err := server.New(&server.Config{
		Data: filepath.Join(dir, "data"),
		Projects: []server.ProjectConfig{{Name: "core", OIDC: []server.OIDCRule{{
			Issuer: issuer, Audience: "strife-registry", Claims: map[string]string{"repository_id": "42"},
			JWKS: "file:" + filepath.Join(dir, "jwks.json"),
		}}}},
	}, server.Options{})
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(srv.Handler())
	defer hs.Close()
	out := filepath.Join(dir, "out")
	if code, o := runCLI(t, "sim", "--system", "raft", "--seed", "5", "--duration", "2s", "--out", out); code != exitOK {
		t.Fatalf("sim: %s", o)
	}
	run := filepath.Join(out, "raft-seed5")

	// Outside a job allowed to ask for tokens, the CLI says what is missing.
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", "")
	if code, o := runCLI(t, "push", "--server", hs.URL, "--project", "core", "--token", "github-oidc:strife-registry", run); code != exitError || !strings.Contains(o, "id-token: write") {
		t.Fatalf("outside a job: exit %d\n%s", code, o)
	}
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_URL", tokens.URL+"/token?api-version=2.0")
	t.Setenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN", "job-request-token")
	if code, o := runCLI(t, "push", "--server", hs.URL, "--project", "core", "--token", "github-oidc:another-registry", run); code != exitError || !strings.Contains(o, "401") {
		t.Fatalf("a token for another audience: exit %d\n%s", code, o)
	}
	code, o := runCLI(t, "push", "--server", hs.URL, "--project", "core", "--token", "github-oidc:strife-registry", run)
	if code != exitOK || !strings.Contains(o, "stored simulation raft") {
		t.Fatalf("push: exit %d\n%s", code, o)
	}
	// The registry records which job pushed the run.
	tok, err := githubOIDCToken("strife-registry")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, hs.URL+"/api/v1/projects/core/runs", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var runs []server.Run
	if err := json.NewDecoder(resp.Body).Decode(&runs); err != nil || len(runs) != 1 ||
		runs[0].Pusher != "repo:acme/core:ref:refs/heads/main (token.actions.githubusercontent.com)" {
		t.Fatalf("stored runs: %v %+v", err, runs)
	}
}
