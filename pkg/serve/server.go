package serve

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/pkg/attest"
	"github.com/kemalhafizh/strife/pkg/campaign"
	"github.com/kemalhafizh/strife/pkg/compliance"
	"github.com/kemalhafizh/strife/pkg/report"
)

// Config configures the strife serve HTTP daemon.
type Config struct {
	ListenAddr      string
	StoreDir        string
	Token           string
	SigningKeyPath  string
	TenantKeyPaths  map[string]string
	Signer          string
	Webhooks        []string
	WebhookTimeout  time.Duration
	MaxMTTR         time.Duration
	MinAvailability float64
}

// PublicKeyInfo describes an active Ed25519 verification key exposed by the server.
type PublicKeyInfo struct {
	Tenant       string `json:"tenant"`
	Signer       string `json:"signer"`
	KeyID        string `json:"key_id"`
	PublicKeyPEM string `json:"public_key_pem"`
}

// Server is the strife serve HTTP daemon and run registry.
type Server struct {
	cfg        Config
	store      *Store
	privKey    ed25519.PrivateKey
	keyID      string
	tenantKeys map[string]ed25519.PrivateKey
	startTime  time.Time
	httpSrv    *http.Server
	webhookMu  sync.Mutex
	deliveries []WebhookDelivery
}

// IngestPayload accepts either a wrapped ingest envelope, a raw report.Report,
// or a raw campaign.Manifest.
type IngestPayload struct {
	Tenant   string             `json:"tenant,omitempty"`
	Commit   string             `json:"commit,omitempty"`
	Branch   string             `json:"branch,omitempty"`
	Report   *report.Report     `json:"report,omitempty"`
	Campaign *campaign.Manifest `json:"campaign,omitempty"`
}

// NewServer initializes a Server backed by StoreDir.
func NewServer(cfg Config) (*Server, error) {
	if cfg.ListenAddr == "" {
		cfg.ListenAddr = "127.0.0.1:8080"
	}
	if cfg.Signer == "" {
		cfg.Signer = "strife-serve"
	}
	st, err := NewStore(cfg.StoreDir)
	if err != nil {
		return nil, err
	}
	var priv ed25519.PrivateKey
	var keyID string
	if cfg.SigningKeyPath != "" {
		k, err := attest.LoadPrivateKey(cfg.SigningKeyPath)
		if err != nil {
			return nil, fmt.Errorf("serve: load signing key: %w", err)
		}
		priv = k
		if pub, ok := priv.Public().(ed25519.PublicKey); ok {
			keyID = attest.KeyID(pub)
		}
	}
	tenantKeys := make(map[string]ed25519.PrivateKey)
	for rawTenant, path := range cfg.TenantKeyPaths {
		t := normalizeTenant(rawTenant)
		k, err := attest.LoadPrivateKey(strings.TrimSpace(path))
		if err != nil {
			return nil, fmt.Errorf("serve: load tenant %q signing key: %w", t, err)
		}
		tenantKeys[t] = k
	}

	s := &Server{
		cfg:        cfg,
		store:      st,
		privKey:    priv,
		keyID:      keyID,
		tenantKeys: tenantKeys,
		startTime:  time.Now().UTC(),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleUI)
	mux.HandleFunc("/api/v1/health", s.handleHealth)
	mux.HandleFunc("/api/v1/keys", s.handleKeys)
	mux.HandleFunc("/api/v1/runs", s.handleRuns)
	mux.HandleFunc("/api/v1/runs/", s.handleRunByID)
	mux.HandleFunc("/api/v1/series", s.handleSeries)
	mux.HandleFunc("/api/v1/ingest", s.handleIngest)
	mux.HandleFunc("/api/v1/webhooks", s.handleWebhooks)

	s.httpSrv = &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	return s, nil
}

// Store returns the underlying persistent Store.
func (s *Server) Store() *Store {
	return s.store
}

// Serve accepts HTTP requests on listener l.
func (s *Server) Serve(l net.Listener) error {
	return s.httpSrv.Serve(l)
}

// ListenAndServe starts listening on cfg.ListenAddr.
func (s *Server) ListenAndServe() error {
	return s.httpSrv.ListenAndServe()
}

// Shutdown gracefully stops the HTTP server.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpSrv.Shutdown(ctx)
}

func (s *Server) checkAuth(w http.ResponseWriter, r *http.Request) bool {
	if s.cfg.Token == "" {
		return true
	}
	got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.Token)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return false
	}
	return true
}

func (s *Server) signingKeyForTenant(tenant string) (ed25519.PrivateKey, string) {
	t := normalizeTenant(tenant)
	if k, ok := s.tenantKeys[t]; ok && len(k) == ed25519.PrivateKeySize {
		return k, fmt.Sprintf("%s:%s", s.cfg.Signer, t)
	}
	if len(s.privKey) == ed25519.PrivateKeySize {
		return s.privKey, s.cfg.Signer
	}
	return nil, ""
}

func publicKeyPEM(pub ed25519.PublicKey) string {
	b, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return ""
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: b}))
}

func (s *Server) handleKeys(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var keys []PublicKeyInfo
	if len(s.privKey) == ed25519.PrivateKeySize {
		if pub, ok := s.privKey.Public().(ed25519.PublicKey); ok {
			keys = append(keys, PublicKeyInfo{
				Tenant:       "default",
				Signer:       s.cfg.Signer,
				KeyID:        attest.KeyID(pub),
				PublicKeyPEM: publicKeyPEM(pub),
			})
		}
	}
	var tenants []string
	for t := range s.tenantKeys {
		tenants = append(tenants, t)
	}
	slices.Sort(tenants)
	for _, t := range tenants {
		priv := s.tenantKeys[t]
		if pub, ok := priv.Public().(ed25519.PublicKey); ok {
			keys = append(keys, PublicKeyInfo{
				Tenant:       t,
				Signer:       fmt.Sprintf("%s:%s", s.cfg.Signer, t),
				KeyID:        attest.KeyID(pub),
				PublicKeyPEM: publicKeyPEM(pub),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total": len(keys),
		"keys":  keys,
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	runs := s.store.List("", "")
	resp := map[string]any{
		"status":              "ok",
		"version":             report.Version,
		"uptime_seconds":      int64(time.Since(s.startTime).Seconds()),
		"stored_runs":         len(runs),
		"targets":             s.store.Targets(),
		"tenants":             s.store.Tenants(),
		"signing_active":      len(s.privKey) == ed25519.PrivateKeySize || len(s.tenantKeys) > 0,
		"key_id":              s.keyID,
		"tenant_keys":         len(s.tenantKeys),
		"webhooks_configured": len(s.cfg.Webhooks),
	}
	if s.cfg.MaxMTTR > 0 {
		resp["sla_max_mttr_ms"] = s.cfg.MaxMTTR.Milliseconds()
	}
	if s.cfg.MinAvailability > 0 {
		resp["sla_min_availability"] = s.cfg.MinAvailability
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleWebhooks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	s.webhookMu.Lock()
	recent := make([]WebhookDelivery, len(s.deliveries))
	for i := range s.deliveries {
		recent[i] = s.deliveries[len(s.deliveries)-1-i]
	}
	s.webhookMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"configured": len(s.cfg.Webhooks),
		"total":      len(recent),
		"deliveries": recent,
	})
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	tenant := strings.TrimSpace(r.URL.Query().Get("tenant"))
	runs := s.store.List(target, kind, tenant)
	writeJSON(w, http.StatusOK, map[string]any{
		"total": len(runs),
		"runs":  runs,
	})
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	tenant := strings.TrimSpace(r.URL.Query().Get("tenant"))
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	series := s.store.Series(tenant, target)
	writeJSON(w, http.StatusOK, map[string]any{
		"total":  len(series),
		"series": series,
	})
}

func (s *Server) handleRunByID(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/runs/")
	id = strings.TrimSpace(strings.TrimSuffix(id, "/"))
	if id == "" {
		s.handleRuns(w, r)
		return
	}
	if strings.HasSuffix(id, "/dossier") {
		runID := strings.TrimSpace(strings.TrimSuffix(id, "/dossier"))
		s.handleRunDossier(w, r, runID)
		return
	}
	_, payload, err := s.store.Lookup(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleRunDossier(w http.ResponseWriter, r *http.Request, id string) {
	entry, _, err := s.store.Lookup(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	rep, camp, bundle, err := s.store.LoadDossierInputs(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	fws, err := compliance.ParseFrameworks(r.URL.Query().Get("frameworks"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	var ver *attest.VerificationResult
	if bundle != nil {
		priv, _ := s.signingKeyForTenant(entry.Tenant)
		if len(priv) == ed25519.PrivateKeySize {
			if pub, ok := priv.Public().(ed25519.PublicKey); ok {
				if v, verr := attest.Verify(bundle, pub); verr == nil && v.Valid {
					ver = v
				}
			}
		}
		if ver == nil && len(s.privKey) == ed25519.PrivateKeySize {
			if pub, ok := s.privKey.Public().(ed25519.PublicKey); ok {
				if v, verr := attest.Verify(bundle, pub); verr == nil && v.Valid {
					ver = v
				}
			}
		}
	}
	maxMTTR := s.cfg.MaxMTTR
	if q := strings.TrimSpace(r.URL.Query().Get("max_mttr")); q != "" {
		if d, derr := time.ParseDuration(q); derr == nil {
			maxMTTR = d
		}
	}
	minAvail := s.cfg.MinAvailability
	if q := strings.TrimSpace(r.URL.Query().Get("min_availability")); q != "" {
		if v, ferr := strconv.ParseFloat(q, 64); ferr == nil {
			minAvail = v
		}
	}
	dossier, err := compliance.Evaluate(compliance.Options{
		Report:          rep,
		Campaign:        camp,
		Verification:    ver,
		Frameworks:      fws,
		MaxMTTR:         maxMTTR,
		MinAvailability: minAvail,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	format := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format")))
	if format == "html" || (format == "" && strings.Contains(r.Header.Get("Accept"), "text/html")) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = compliance.HTML(w, dossier)
		return
	}
	writeJSON(w, http.StatusOK, dossier)
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if !s.checkAuth(w, r) {
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 32*1024*1024))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	tenant := strings.TrimSpace(r.URL.Query().Get("tenant"))
	commit := strings.TrimSpace(r.URL.Query().Get("commit"))
	branch := strings.TrimSpace(r.URL.Query().Get("branch"))

	var env IngestPayload
	if err := json.Unmarshal(body, &env); err == nil && (env.Report != nil || env.Campaign != nil) {
		if env.Tenant != "" {
			tenant = env.Tenant
		}
		if env.Commit != "" {
			commit = env.Commit
		}
		if env.Branch != "" {
			branch = env.Branch
		}
	} else {
		var rawMap map[string]json.RawMessage
		if err := json.Unmarshal(body, &rawMap); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid json: %v", err)})
			return
		}
		if _, hasSeeds := rawMap["seeds"]; hasSeeds {
			var m campaign.Manifest
			if err := json.Unmarshal(body, &m); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			env.Campaign = &m
		} else {
			var rep report.Report
			if err := json.Unmarshal(body, &rep); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				return
			}
			env.Report = &rep
		}
	}

	var (
		res  *IngestResult
		err2 error
	)
	if env.Campaign != nil {
		if commit != "" && env.Campaign.Commit == "" {
			env.Campaign.Commit = commit
		}
		if branch != "" && env.Campaign.Branch == "" {
			env.Campaign.Branch = branch
		}
		res, err2 = s.store.IngestCampaign(env.Campaign, tenant)
	} else {
		priv, signer := s.signingKeyForTenant(tenant)
		res, err2 = s.store.IngestReport(env.Report, commit, branch, priv, signer, tenant)
	}
	if err2 != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err2.Error()})
		return
	}
	res.Webhooks = s.notifyWebhooks(res, env.Report)
	writeJSON(w, http.StatusCreated, res)
}

// Push sends a Report or Campaign Manifest to a remote strife serve registry
// endpoint (either a base URL like "http://127.0.0.1:8080" or a full
// "/api/v1/ingest" URL).
func Push(ctx context.Context, registryURL, token, tenant, commit, branch string, artifact any) (*IngestResult, error) {
	u := strings.TrimSpace(registryURL)
	if u == "" {
		return nil, fmt.Errorf("serve: empty registry URL")
	}
	u = strings.TrimRight(u, "/")
	if !strings.HasSuffix(u, "/api/v1/ingest") {
		u += "/api/v1/ingest"
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return nil, err
	}
	q := parsed.Query()
	if tenant != "" {
		q.Set("tenant", tenant)
	}
	if commit != "" {
		q.Set("commit", commit)
	}
	if branch != "" {
		q.Set("branch", branch)
	}
	parsed.RawQuery = q.Encode()

	body, err := json.Marshal(artifact)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("serve: push failed with HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
	}
	var out IngestResult
	if err := json.Unmarshal(respBytes, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}
