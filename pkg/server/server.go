package server

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kemalhafizh/strife/pkg/attest"
	"github.com/kemalhafizh/strife/pkg/campaign"
	"github.com/kemalhafizh/strife/pkg/checker"
	"github.com/kemalhafizh/strife/pkg/compliance"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/report"
)

// Server is the run registry.
type Server struct {
	cfg      *Config
	projects map[string]*project
	store    *store
	mux      *http.ServeMux
	client   *http.Client
	logf     func(format string, args ...any)
	now      func() time.Time

	ingest sync.Mutex     // compare-then-store must not interleave
	hooks  sync.WaitGroup // webhooks in flight
}

// Options tune a Server; zero values are fine.
type Options struct {
	Logf func(format string, args ...any)
	Now  func() time.Time
	// HTTPClient sends webhooks (default: 10s timeout).
	HTTPClient *http.Client
}

// New opens (or creates) the data directory and builds the registry.
func New(cfg *Config, o Options) (*Server, error) {
	projects, err := cfg.resolve()
	if err != nil {
		return nil, err
	}
	if cfg.Data == "" {
		cfg.Data = "strife-data"
	}
	if cfg.MaxUpload <= 0 {
		cfg.MaxUpload = 256 << 20
	}
	st, err := openStore(cfg.Data, projects)
	if err != nil {
		return nil, err
	}
	s := &Server{cfg: cfg, projects: projects, store: st, logf: o.Logf, now: o.Now, client: o.HTTPClient}
	if s.logf == nil {
		s.logf = func(string, ...any) {}
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.client == nil {
		s.client = &http.Client{Timeout: 10 * time.Second}
	}
	s.routes()
	return s, nil
}

// Handler serves the API and the dashboard.
func (s *Server) Handler() http.Handler { return securityHeaders(s.mux) }

// Wait blocks until every webhook in flight has been sent or has failed.
func (s *Server) Wait() { s.hooks.Wait() }

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("X-Frame-Options", "DENY")
		h.ServeHTTP(w, r)
	})
}

func (s *Server) routes() {
	m := http.NewServeMux()
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok\n") })
	m.HandleFunc("GET /api/v1/projects", s.apiProjects)
	m.HandleFunc("POST /api/v1/projects/{project}/runs", s.apiPush)
	m.HandleFunc("GET /api/v1/projects/{project}/runs", s.read(s.apiRuns))
	m.HandleFunc("GET /api/v1/projects/{project}/series", s.read(s.apiSeries))
	m.HandleFunc("GET /api/v1/projects/{project}/runs/{id}", s.read(s.apiRun))
	m.HandleFunc("GET /api/v1/projects/{project}/runs/{id}/files/{name}", s.read(s.apiFile))
	m.HandleFunc("GET /api/v1/projects/{project}/runs/{id}/report.html", s.read(s.apiReportHTML))
	m.HandleFunc("GET /api/v1/projects/{project}/runs/{id}/dossier", s.read(s.apiDossier))
	m.HandleFunc("GET /{$}", s.uiIndex)
	m.HandleFunc("GET /ui/{project}", s.read(s.uiProject))
	m.HandleFunc("GET /ui/{project}/runs/{id}", s.read(s.uiRun))
	m.HandleFunc("GET /ui/{project}/series", s.read(s.uiSeries))
	s.mux = m
}

// ---- authentication ----

func token(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	if _, pw, ok := r.BasicAuth(); ok {
		return pw
	}
	return ""
}

func equal(a, b string) bool {
	return b != "" && subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// canWrite reports whether r may push to p, and who pushes: "token" for
// the project's write token, or the subject of an OIDC token.
func (s *Server) canWrite(p *project, r *http.Request) (string, bool) {
	t := token(r)
	if equal(t, p.token) {
		return "token", true
	}
	if id, ok := s.oidcAccess(r.Context(), p, t, true); ok {
		return id.String(), true
	}
	return "", false
}

func (s *Server) canRead(p *project, r *http.Request) bool {
	t := token(r)
	if s.cfg.PublicRead || equal(t, p.token) || equal(t, p.readToken) {
		return true
	}
	_, ok := s.oidcAccess(r.Context(), p, t, false)
	return ok
}

type ctxKey struct{}

// read resolves {project} and requires read access to it. Unknown projects
// and missing access answer alike, so names do not leak.
func (s *Server) read(h func(http.ResponseWriter, *http.Request, *project)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := s.projects[r.PathValue("project")]
		if p == nil || !s.canRead(p, r) {
			if strings.HasPrefix(r.URL.Path, "/ui/") {
				w.Header().Set("WWW-Authenticate", `Basic realm="strife", charset="UTF-8"`)
			}
			httpError(w, http.StatusUnauthorized, "unknown project or missing token")
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, p)), p)
	}
}

// ---- API ----

func httpError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(v)
}

// ProjectSummary is one entry of GET /api/v1/projects.
type ProjectSummary struct {
	Name        string    `json:"name"`
	Runs        int       `json:"runs"`
	Series      int       `json:"series"`
	Failing     int       `json:"failing_series"`
	LastRun     time.Time `json:"last_run"`
	Regressions int       `json:"regressions"`
}

func (s *Server) summary(p *project) ProjectSummary {
	sum := ProjectSummary{Name: p.name}
	for _, se := range s.seriesOf(p) {
		sum.Series++
		if se.Latest.Verdict == checker.Violation {
			sum.Failing++
		}
	}
	for _, r := range s.store.list(p.name, nil, 0) {
		sum.Runs++
		if r.Received.After(sum.LastRun) {
			sum.LastRun = r.Received
		}
		if r.Change == ChangeRegression {
			sum.Regressions++
		}
	}
	return sum
}

func (s *Server) visible(r *http.Request) []*project {
	var out []*project
	for _, p := range s.projects {
		if s.canRead(p, r) {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out
}

func (s *Server) apiProjects(w http.ResponseWriter, r *http.Request) {
	out := []ProjectSummary{}
	for _, p := range s.visible(r) {
		out = append(out, s.summary(p))
	}
	writeJSON(w, http.StatusOK, out)
}

// PushResult answers a push.
type PushResult struct {
	Run     *Run   `json:"run"`
	Created bool   `json:"created"` // false: the same run was already stored
	URL     string `json:"url"`     // the run's dashboard page
}

func (s *Server) apiPush(w http.ResponseWriter, r *http.Request) {
	p := s.projects[r.PathValue("project")]
	var pusher string
	if p != nil {
		pusher, _ = s.canWrite(p, r)
	}
	if pusher == "" {
		httpError(w, http.StatusUnauthorized, "unknown project or missing write token")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.cfg.MaxUpload))
	if err != nil {
		httpError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("upload larger than %d bytes", s.cfg.MaxUpload))
		return
	}
	var u Upload
	if err := strictJSON(body, &u); err != nil {
		httpError(w, http.StatusBadRequest, "body: "+err.Error())
		return
	}
	in, err := verify(p, &u, s.now())
	if err == nil {
		in.run.Pusher = pusher
	}
	var rj *rejection
	if errors.As(err, &rj) {
		httpError(w, http.StatusUnprocessableEntity, rj.msg)
		return
	}
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}

	s.ingest.Lock()
	if old := s.store.get(p.name, in.run.ID); old != nil {
		s.ingest.Unlock()
		writeJSON(w, http.StatusOK, PushResult{Run: old, URL: s.runURL(r, old)})
		return
	}
	prev := s.store.latest(p.name, in.run.Series)
	var prevCamp *campaign.Manifest
	if prev != nil && in.camp != nil {
		if data, err := s.store.file(p.name, prev.ID, fileCampaign); err == nil {
			var m campaign.Manifest
			if json.Unmarshal(data, &m) == nil {
				prevCamp = &m
			}
		}
	}
	compare(in.run, prev, prevCamp, in.camp)
	err = s.store.put(in.run, in.files, p.retain)
	s.ingest.Unlock()
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.logf("%s: stored %s %s %s (%s)", p.name, in.run.Kind, in.run.Target, in.run.Verdict, orDash(in.run.Change))
	if in.run.Change == ChangeRegression || in.run.Change == ChangeFixed {
		s.notify(p, in.run, s.runURL(r, in.run))
	}
	writeJSON(w, http.StatusCreated, PushResult{Run: in.run, Created: true, URL: s.runURL(r, in.run)})
}

func (s *Server) runURL(r *http.Request, run *Run) string {
	base := strings.TrimRight(s.cfg.PublicURL, "/")
	if base == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		base = scheme + "://" + r.Host
	}
	return base + "/ui/" + run.Project + "/runs/" + run.ID
}

func orDash(s string) string {
	if s == "" {
		return "unchanged"
	}
	return s
}

// notify posts a Slack-compatible message to the project's webhook.
func (s *Server) notify(p *project, run *Run, url string) {
	if p.webhook == "" {
		return
	}
	what := "regressed"
	if run.Change == ChangeFixed {
		what = "is fixed"
	}
	text := fmt.Sprintf("Strife: %s %s %s: %s (%s)", p.name, run.Kind, run.Target, what, run.Verdict)
	if run.Commit != "" {
		text += " at " + clip(run.Commit, 12)
	}
	text += "\n" + url
	body, _ := json.Marshal(map[string]any{"text": text, "run": run, "url": url})
	s.hooks.Add(1)
	go func() {
		defer s.hooks.Done()
		resp, err := s.client.Post(p.webhook, "application/json", bytes.NewReader(body))
		if err != nil {
			s.logf("%s: webhook: %v", p.name, err)
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			s.logf("%s: webhook answered %s", p.name, resp.Status)
		}
	}()
}

func (s *Server) apiRuns(w http.ResponseWriter, r *http.Request, p *project) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	runs := s.store.list(p.name, func(x *Run) bool {
		return match(q.Get("series"), x.Series) && match(q.Get("target"), x.Target) && match(q.Get("kind"), x.Kind) &&
			match(q.Get("verdict"), string(x.Verdict)) && match(q.Get("change"), x.Change) && match(q.Get("branch"), x.Branch)
	}, limit)
	if runs == nil {
		runs = []*Run{}
	}
	writeJSON(w, http.StatusOK, runs)
}

func match(want, got string) bool { return want == "" || want == got }

// Series summarises one series: its latest run and recent outcomes.
type Series struct {
	Name    string            `json:"name"`
	Kind    string            `json:"kind"`
	Target  string            `json:"target"`
	Latest  *Run              `json:"latest"`
	Runs    int               `json:"runs"`
	Recent  []checker.Verdict `json:"recent"` // newest first, at most 20
	Failing int               `json:"failing_since,omitempty"`
}

func (s *Server) seriesOf(p *project) []*Series {
	by := map[string]*Series{}
	var order []*Series
	for _, r := range s.store.list(p.name, nil, 0) { // newest first
		se := by[r.Series]
		if se == nil {
			se = &Series{Name: r.Series, Kind: r.Kind, Target: r.Target, Latest: r}
			by[r.Series] = se
			order = append(order, se)
		}
		se.Runs++
		if len(se.Recent) < 20 {
			se.Recent = append(se.Recent, r.Verdict)
		}
	}
	for _, se := range order {
		for _, v := range se.Recent {
			if v != checker.Violation {
				break
			}
			se.Failing++
		}
	}
	return order
}

func (s *Server) apiSeries(w http.ResponseWriter, r *http.Request, p *project) {
	writeJSON(w, http.StatusOK, s.seriesOf(p))
}

func (s *Server) run(w http.ResponseWriter, r *http.Request, p *project) *Run {
	id := r.PathValue("id")
	run := (*Run)(nil)
	if validID.MatchString(id) {
		run = s.store.get(p.name, id)
	}
	if run == nil {
		httpError(w, http.StatusNotFound, "no such run")
	}
	return run
}

func (s *Server) apiRun(w http.ResponseWriter, r *http.Request, p *project) {
	if run := s.run(w, r, p); run != nil {
		writeJSON(w, http.StatusOK, run)
	}
}

var contentTypes = map[string]string{
	fileReport:   "application/json",
	fileCampaign: "application/json",
	fileBundle:   "application/json",
	fileHistory:  "application/x-ndjson",
}

func (s *Server) apiFile(w http.ResponseWriter, r *http.Request, p *project) {
	run := s.run(w, r, p)
	if run == nil {
		return
	}
	name := r.PathValue("name")
	ct, ok := contentTypes[name]
	if !ok {
		httpError(w, http.StatusNotFound, "no such file")
		return
	}
	data, err := s.store.file(p.name, run.ID, name)
	if err != nil {
		httpError(w, http.StatusNotFound, "this run has no "+name)
		return
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	_, _ = w.Write(data)
}

func (s *Server) loadReport(p *project, run *Run) (*report.Report, []history.Op, error) {
	data, err := s.store.file(p.name, run.ID, fileReport)
	if err != nil {
		return nil, nil, err
	}
	var rep report.Report
	if err := json.Unmarshal(data, &rep); err != nil {
		return nil, nil, err
	}
	var ops []history.Op
	if h, err := s.store.file(p.name, run.ID, fileHistory); err == nil {
		ops, _ = history.ReadJSONL(bytes.NewReader(h))
	}
	return &rep, ops, nil
}

func (s *Server) apiReportHTML(w http.ResponseWriter, r *http.Request, p *project) {
	run := s.run(w, r, p)
	if run == nil {
		return
	}
	rep, ops, err := s.loadReport(p, run)
	if err != nil {
		httpError(w, http.StatusNotFound, "campaign runs have no single report; open a seed's report instead")
		return
	}
	var buf bytes.Buffer
	if err := report.HTML(&buf, rep, ops); err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; img-src data:")
	_, _ = w.Write(buf.Bytes())
}

// apiDossier evaluates a run against compliance frameworks on demand,
// with the signature re-verified against the project's trusted keys.
func (s *Server) apiDossier(w http.ResponseWriter, r *http.Request, p *project) {
	run := s.run(w, r, p)
	if run == nil {
		return
	}
	fws, err := compliance.ParseFrameworks(r.URL.Query().Get("frameworks"))
	if err != nil {
		httpError(w, http.StatusBadRequest, err.Error())
		return
	}
	opts := compliance.Options{Frameworks: fws, Now: s.now()}
	if run.Kind == "campaign" {
		data, err := s.store.file(p.name, run.ID, fileCampaign)
		var m campaign.Manifest
		if err == nil {
			err = json.Unmarshal(data, &m)
		}
		if err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		opts.Campaign = &m
		// The signature is checked again against the keys trusted now: a
		// key removed since the push no longer vouches for the run.
		if data, err := s.store.file(p.name, run.ID, fileBundle); err == nil {
			if res, err := verifySignature(p, data); err == nil && m.Attests(res) == nil {
				opts.Verification = res
			}
		}
	} else {
		rep, _, err := s.loadReport(p, run)
		if err != nil {
			httpError(w, http.StatusInternalServerError, err.Error())
			return
		}
		opts.Report = rep
		if data, err := s.store.file(p.name, run.ID, fileBundle); err == nil {
			if res, err := verifySignature(p, data); err == nil && res.Kind == attest.KindReport {
				res.HistoryVerified = run.HistoryVerified
				opts.Verification = res
			}
		}
	}
	d, err := compliance.Evaluate(opts)
	if err != nil {
		httpError(w, http.StatusInternalServerError, err.Error())
		return
	}
	switch r.URL.Query().Get("format") {
	case "html":
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'")
		_ = compliance.HTML(w, d)
	case "md", "markdown":
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		_ = compliance.Markdown(w, d)
	default:
		writeJSON(w, http.StatusOK, d)
	}
}
