// Package server is Strife's self-hosted run registry: CI jobs push their
// reports, campaign manifests, histories and signed attestations to it, and
// it verifies them, keeps their history per project, flags regressions,
// alerts a webhook, serves a dashboard and generates compliance dossiers on
// demand. It runs inside the customer's own network: nothing it stores ever
// leaves it.
package server

import (
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"github.com/kemalhafizh/strife/internal/secret"
	"github.com/kemalhafizh/strife/pkg/attest"
)

// Config is the server's configuration file (server.json).
type Config struct {
	Listen string `json:"listen,omitempty"` // default :8080
	Data   string `json:"data,omitempty"`   // default ./strife-data
	// PublicRead lets anyone read every project without a token (for a
	// registry reachable only from a trusted network).
	PublicRead bool `json:"public_read,omitempty"`
	// PublicURL is how users reach the server (e.g. https://strife.internal),
	// for links in webhooks and push results; default: the request's host.
	PublicURL string `json:"public_url,omitempty"`
	// MaxUpload bounds a pushed run in bytes (default 256 MiB).
	MaxUpload int64           `json:"max_upload,omitempty"`
	Projects  []ProjectConfig `json:"projects"`
}

// ProjectConfig is one tenant: a team, a service or a product line.
type ProjectConfig struct {
	Name string `json:"name"`
	// Token allows pushing runs and reading; ReadToken allows reading only.
	// Both take "env:NAME" or "file:PATH" references.
	Token     string `json:"token"`
	ReadToken string `json:"read_token,omitempty"`
	// TrustedKeys are Ed25519 public keys (.pub files from strife keygen)
	// whose signatures make a run "attested".
	TrustedKeys []string `json:"trusted_keys,omitempty"`
	// RequireSignature rejects runs without a valid signature by a trusted
	// key.
	RequireSignature bool `json:"require_signature,omitempty"`
	// Webhook receives a JSON POST when a run changes its series' outcome
	// (a regression or a fix). Slack-compatible: the body has a "text".
	Webhook string `json:"webhook,omitempty"`
	// Retain keeps at most this many runs (0 = all); the oldest go first.
	Retain int `json:"retain,omitempty"`
}

// project is a ProjectConfig with its secrets and keys resolved.
type project struct {
	name             string
	token, readToken string
	keys             []ed25519.PublicKey
	requireSignature bool
	webhook          string
	retain           int
}

var validName = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// LoadConfig reads a server.json; relative key paths resolve against its
// directory.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	dir := filepath.Dir(path)
	for i := range c.Projects {
		for j, k := range c.Projects[i].TrustedKeys {
			if !filepath.IsAbs(k) {
				c.Projects[i].TrustedKeys[j] = filepath.Join(dir, k)
			}
		}
	}
	return &c, nil
}

func (c *Config) resolve() (map[string]*project, error) {
	if len(c.Projects) == 0 {
		return nil, fmt.Errorf("server: no projects configured")
	}
	out := map[string]*project{}
	for _, pc := range c.Projects {
		if !validName.MatchString(pc.Name) {
			return nil, fmt.Errorf("server: project name %q: use lowercase letters, digits, - and _", pc.Name)
		}
		if out[pc.Name] != nil {
			return nil, fmt.Errorf("server: project %q is configured twice", pc.Name)
		}
		p := &project{name: pc.Name, requireSignature: pc.RequireSignature, retain: pc.Retain}
		var err error
		if p.token, err = secret.Resolve(pc.Token); err != nil || p.token == "" {
			return nil, fmt.Errorf("server: project %s: a token is required (%v)", pc.Name, err)
		}
		if len(p.token) < 16 {
			return nil, fmt.Errorf("server: project %s: the token must be at least 16 characters", pc.Name)
		}
		if p.readToken, err = secret.Resolve(pc.ReadToken); err != nil {
			return nil, fmt.Errorf("server: project %s: read_token: %w", pc.Name, err)
		}
		if p.webhook, err = secret.Resolve(pc.Webhook); err != nil {
			return nil, fmt.Errorf("server: project %s: webhook: %w", pc.Name, err)
		}
		for _, k := range pc.TrustedKeys {
			pub, err := attest.LoadPublicKey(k)
			if err != nil {
				return nil, fmt.Errorf("server: project %s: trusted key: %w", pc.Name, err)
			}
			p.keys = append(p.keys, pub)
		}
		if p.requireSignature && len(p.keys) == 0 {
			return nil, fmt.Errorf("server: project %s requires signatures but trusts no key", pc.Name)
		}
		out[pc.Name] = p
	}
	return out, nil
}
