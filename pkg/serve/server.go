package serve

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/subtle"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
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
	TenantTokens    map[string]string
	SigningKeyPath  string
	TenantKeyPaths  map[string]string
	Signer          string
	Webhooks        []string
	WebhookTimeout  time.Duration
	MaxMTTR         time.Duration
	MinAvailability float64
	JobLease        time.Duration
}

func normalizeAvailability(v float64) float64 {
	if v > 1.0 {
		return v / 100.0
	}
	return v
}

func validateAvailability(v float64) (float64, error) {
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v > 100 {
		return 0, fmt.Errorf("min_availability %v must be within [0, 100]", v)
	}
	return normalizeAvailability(v), nil
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
	cfg            Config
	store          *Store
	privKey        ed25519.PrivateKey
	keyID          string
	tenantKeys     map[string]ed25519.PrivateKey
	tenantTokens   map[string]string
	startTime      time.Time
	httpSrv        *http.Server
	webhookMu      sync.Mutex
	deliveries     []WebhookDelivery
	jobMu          sync.Mutex
	jobs           []*Job
	jobSeq         uint64
	beforeSaveJobs func() error
}

// IngestPayload accepts either a wrapped ingest envelope, a raw report.Report,
// or a raw campaign.Manifest.
type IngestPayload struct {
	Tenant         string             `json:"tenant,omitempty"`
	Commit         string             `json:"commit,omitempty"`
	Branch         string             `json:"branch,omitempty"`
	IdempotencyKey string             `json:"idempotency_key,omitempty"`
	Report         *report.Report     `json:"report,omitempty"`
	Campaign       *campaign.Manifest `json:"campaign,omitempty"`
}

// NewServer initializes a Server backed by StoreDir.
func NewServer(cfg Config) (*Server, error) {
	if cfg.MaxMTTR < 0 {
		return nil, fmt.Errorf("serve: max_mttr %v must be non-negative", cfg.MaxMTTR)
	}
	normAvail, err := validateAvailability(cfg.MinAvailability)
	if err != nil {
		return nil, fmt.Errorf("serve: %w", err)
	}
	cfg.MinAvailability = normAvail
	if cfg.JobLease < 0 {
		return nil, fmt.Errorf("serve: job_lease %v must be non-negative", cfg.JobLease)
	}
	if cfg.JobLease == 0 {
		cfg.JobLease = 2 * time.Minute
	}

	tenantTokens := make(map[string]string, len(cfg.TenantTokens))
	seenTokens := make(map[string]string, len(cfg.TenantTokens)+1)
	if cfg.Token != "" {
		seenTokens[cfg.Token] = "admin"
	}
	for rawTenant, tok := range cfg.TenantTokens {
		trimmedTenant := strings.TrimSpace(rawTenant)
		if trimmedTenant == "" {
			return nil, fmt.Errorf("serve: tenant token entry has empty tenant name")
		}
		t := normalizeTenant(trimmedTenant)
		if tok == "" {
			return nil, fmt.Errorf("serve: tenant %q token must be non-empty", t)
		}
		if _, dup := tenantTokens[t]; dup {
			return nil, fmt.Errorf("serve: duplicate tenant token entry for %q", t)
		}
		if prevOwner, dup := seenTokens[tok]; dup {
			return nil, fmt.Errorf("serve: duplicate bearer token shared between %s and tenant %q", prevOwner, t)
		}
		tenantTokens[t] = tok
		seenTokens[tok] = fmt.Sprintf("tenant %q", t)
	}

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
		cfg:          cfg,
		store:        st,
		privKey:      priv,
		keyID:        keyID,
		tenantKeys:   tenantKeys,
		tenantTokens: tenantTokens,
		startTime:    time.Now().UTC(),
	}
	if err := s.loadJobs(); err != nil {
		return nil, fmt.Errorf("serve: load jobs: %w", err)
	}
	if err := s.reconcileJobsWithStore(); err != nil {
		return nil, fmt.Errorf("serve: reconcile jobs: %w", err)
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
	mux.HandleFunc("/api/v1/trends", s.handleTrends)
	mux.HandleFunc("/api/v1/jobs", s.handleJobs)
	mux.HandleFunc("/api/v1/jobs/", s.handleJobsSubpath)

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

type authIdentity struct {
	enabled bool
	ok      bool
	isAdmin bool
	tenant  string
}

func (a authIdentity) allowsTenant(tenant string) bool {
	if a.isAdmin {
		return true
	}
	return normalizeTenant(tenant) == a.tenant
}

func (s *Server) authEnabled() bool {
	return s.cfg.Token != "" || len(s.tenantTokens) > 0
}

func extractBearerToken(r *http.Request) (string, bool) {
	hdr := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(hdr) < len("Bearer ") {
		return "", false
	}
	if !strings.EqualFold(hdr[:6], "Bearer") || (hdr[6] != ' ' && hdr[6] != '\t') {
		return "", false
	}
	tok := strings.TrimSpace(hdr[7:])
	if tok == "" {
		return "", false
	}
	return tok, true
}

func (s *Server) authFromRequest(r *http.Request) authIdentity {
	if !s.authEnabled() {
		return authIdentity{enabled: false, ok: true, isAdmin: true}
	}
	tok, hasBearer := extractBearerToken(r)
	if !hasBearer {
		return authIdentity{enabled: true, ok: false}
	}
	id := authIdentity{enabled: true}
	if s.cfg.Token != "" && subtle.ConstantTimeCompare([]byte(tok), []byte(s.cfg.Token)) == 1 {
		id.ok = true
		id.isAdmin = true
	}
	for tenant, secretTok := range s.tenantTokens {
		if subtle.ConstantTimeCompare([]byte(tok), []byte(secretTok)) == 1 {
			id.ok = true
			id.tenant = tenant
		}
	}
	return id
}

func (s *Server) hasValidAuth(r *http.Request) bool {
	return s.authFromRequest(r).ok
}

func (s *Server) checkAuthIdentity(w http.ResponseWriter, r *http.Request) (authIdentity, bool) {
	id := s.authFromRequest(r)
	if !id.ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return id, false
	}
	return id, true
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
	id := s.authFromRequest(r)
	resp := map[string]any{
		"status":              "ok",
		"version":             report.Version,
		"uptime_seconds":      int64(time.Since(s.startTime).Seconds()),
		"auth_enabled":        s.authEnabled(),
		"signing_active":      len(s.privKey) == ed25519.PrivateKeySize || len(s.tenantKeys) > 0,
		"key_id":              s.keyID,
		"tenant_keys":         len(s.tenantKeys),
		"webhooks_configured": len(s.cfg.Webhooks),
	}
	if id.ok {
		if id.isAdmin {
			runs := s.store.List("", "")
			resp["stored_runs"] = len(runs)
			resp["targets"] = s.store.Targets()
			resp["tenants"] = s.store.Tenants()
		} else {
			runs := s.store.List("", "", id.tenant)
			var targets []string
			for _, e := range runs {
				if e.Target != "" && !slices.Contains(targets, e.Target) {
					targets = append(targets, e.Target)
				}
			}
			slices.Sort(targets)
			resp["stored_runs"] = len(runs)
			resp["targets"] = targets
			resp["tenants"] = []string{id.tenant}
		}
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
	id, ok := s.checkAuthIdentity(w, r)
	if !ok {
		return
	}
	if !id.isAdmin {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: admin token required"})
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
	id, ok := s.checkAuthIdentity(w, r)
	if !ok {
		return
	}
	target := strings.TrimSpace(r.URL.Query().Get("target"))
	kind := strings.TrimSpace(r.URL.Query().Get("kind"))
	tenant := strings.TrimSpace(r.URL.Query().Get("tenant"))
	if !id.isAdmin {
		if tenant != "" && normalizeTenant(tenant) != id.tenant {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: tenant mismatch"})
			return
		}
		tenant = id.tenant
	}
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
	id, ok := s.checkAuthIdentity(w, r)
	if !ok {
		return
	}
	tenant := strings.TrimSpace(r.URL.Query().Get("tenant"))
	if !id.isAdmin {
		if tenant != "" && normalizeTenant(tenant) != id.tenant {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: tenant mismatch"})
			return
		}
		tenant = id.tenant
	}
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
	idAuth, ok := s.checkAuthIdentity(w, r)
	if !ok {
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
		if runID == "" || strings.Contains(runID, "/") {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid run id"})
			return
		}
		s.handleRunDossier(w, r, idAuth, runID)
		return
	}
	if strings.Contains(id, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid run id"})
		return
	}
	entry, payload, err := s.store.Lookup(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if !idAuth.allowsTenant(entry.Tenant) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: run belongs to another tenant"})
		return
	}
	writeJSON(w, http.StatusOK, payload)
}

func (s *Server) handleRunDossier(w http.ResponseWriter, r *http.Request, idAuth authIdentity, id string) {
	entry, _, err := s.store.Lookup(id)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	if !idAuth.allowsTenant(entry.Tenant) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: run belongs to another tenant"})
		return
	}
	rep, camp, bundle, err := s.store.LoadDossierInputs(id)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
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
		d, derr := time.ParseDuration(q)
		if derr != nil || d < 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid max_mttr %q", q)})
			return
		}
		maxMTTR = d
	}
	minAvail := s.cfg.MinAvailability
	if q := strings.TrimSpace(r.URL.Query().Get("min_availability")); q != "" {
		v, ferr := strconv.ParseFloat(q, 64)
		if ferr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid min_availability %q", q)})
			return
		}
		norm, verr := validateAvailability(v)
		if verr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": verr.Error()})
			return
		}
		minAvail = norm
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

func resolveIdempotencyKey(candidates ...string) (string, error) {
	var resolved string
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if resolved != "" && resolved != c {
			return "", fmt.Errorf("conflicting idempotency keys provided")
		}
		resolved = c
	}
	if len(resolved) > 256 {
		return "", fmt.Errorf("idempotency key exceeds 256 characters")
	}
	for _, r := range resolved {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("idempotency key contains control characters")
		}
	}
	return resolved, nil
}

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	idAuth, ok := s.checkAuthIdentity(w, r)
	if !ok {
		return
	}
	const maxIngestBodyBytes = 32 * 1024 * 1024
	body, err := io.ReadAll(io.LimitReader(r.Body, maxIngestBodyBytes+1))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if len(body) > maxIngestBodyBytes {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "ingest body exceeds 32 MiB limit"})
		return
	}

	var rawMap map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawMap); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid json: %v", err)})
		return
	}
	if len(rawMap) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "empty json payload"})
		return
	}

	hasNonNullKey := func(k string) bool {
		raw, exists := rawMap[k]
		if !exists {
			return false
		}
		trimmed := bytes.TrimSpace(raw)
		return len(trimmed) > 0 && !bytes.Equal(trimmed, []byte("null"))
	}

	tenant := strings.TrimSpace(r.URL.Query().Get("tenant"))
	commit := strings.TrimSpace(r.URL.Query().Get("commit"))
	branch := strings.TrimSpace(r.URL.Query().Get("branch"))

	var env IngestPayload
	if hasNonNullKey("report") || hasNonNullKey("campaign") {
		if err := json.Unmarshal(body, &env); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("invalid envelope json: %v", err)})
			return
		}
		if env.Report != nil && env.Campaign != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "ingest envelope must contain either report or campaign, not both"})
			return
		}
		if env.Tenant != "" {
			tenant = env.Tenant
		}
		if env.Commit != "" {
			commit = env.Commit
		}
		if env.Branch != "" {
			branch = env.Branch
		}
	} else if hasNonNullKey("seeds") {
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

	idempKey, idempErr := resolveIdempotencyKey(
		r.Header.Get("Idempotency-Key"),
		r.URL.Query().Get("idempotency_key"),
		env.IdempotencyKey,
	)
	if idempErr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": idempErr.Error()})
		return
	}

	if !idAuth.isAdmin {
		if tenant != "" && normalizeTenant(tenant) != idAuth.tenant {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden: tenant mismatch"})
			return
		}
		tenant = idAuth.tenant
	}

	opts := IngestOptions{IdempotencyKey: idempKey}
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
		res, err2 = s.store.IngestCampaignWithOptions(env.Campaign, tenant, opts)
	} else {
		priv, signer := s.signingKeyForTenant(tenant)
		res, err2 = s.store.IngestReportWithOptions(env.Report, commit, branch, priv, signer, tenant, opts)
	}
	if err2 != nil {
		if errors.Is(err2, ErrIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err2.Error()})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err2.Error()})
		return
	}
	if res.Deduplicated {
		writeJSON(w, http.StatusOK, res)
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
