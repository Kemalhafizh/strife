package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kemalhafizh/strife/internal/secret"
	"github.com/kemalhafizh/strife/pkg/server"
)

func cmdServer(args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	fs.SetOutput(stderr)
	cfgPath := fs.String("config", "server.json", "server configuration (projects, tokens, trusted keys)")
	listen := fs.String("listen", "", "address to listen on (overrides the config; default :8080)")
	data := fs.String("data", "", "data directory (overrides the config; default ./strife-data)")
	tlsCert := fs.String("tls-cert", "", "serve HTTPS with this certificate (PEM)")
	tlsKey := fs.String("tls-key", "", "private key for --tls-cert")
	if err := fs.Parse(args); err != nil {
		return exitUsage, err
	}
	cfg, err := server.LoadConfig(*cfgPath)
	if err != nil {
		return exitUsage, err
	}
	if *listen != "" {
		cfg.Listen = *listen
	}
	if *data != "" {
		cfg.Data = *data
	}
	if cfg.Listen == "" {
		cfg.Listen = ":8080"
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		return exitUsage, errors.New("--tls-cert and --tls-key go together")
	}
	logf := func(format string, a ...any) {
		fmt.Fprintf(stderr, "%s %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, a...))
	}
	srv, err := server.New(cfg, server.Options{Logf: logf})
	if err != nil {
		return exitUsage, err
	}
	l, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		return exitError, err
	}
	hs := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       5 * time.Minute, // large histories on slow links
		WriteTimeout:      5 * time.Minute,
		IdleTimeout:       2 * time.Minute,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errc := make(chan error, 1)
	go func() {
		if *tlsCert != "" {
			errc <- hs.ServeTLS(l, *tlsCert, *tlsKey)
		} else {
			errc <- hs.Serve(l)
		}
	}()
	scheme := "http"
	if *tlsCert != "" {
		scheme = "https"
	}
	fmt.Fprintf(stdout, "strife server: %s://%s, data in %s, %d project(s)\n", scheme, l.Addr(), cfg.Data, len(cfg.Projects))
	select {
	case err := <-errc:
		return exitError, err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = hs.Shutdown(sctx)
	srv.Wait()
	return exitOK, nil
}

// cmdPush uploads a run directory (report.json, and history.jsonl and
// bundle.json when present) or a campaign.json (and campaign-bundle.json
// next to it when present) to a strife server.
func cmdPush(args []string, stdout, stderr io.Writer) (int, error) {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	fs.SetOutput(stderr)
	srvURL := fs.String("server", os.Getenv("STRIFE_SERVER"), "server URL, e.g. https://strife.internal (or $STRIFE_SERVER)")
	proj := fs.String("project", os.Getenv("STRIFE_PROJECT"), "project to push to (or $STRIFE_PROJECT)")
	tok := fs.String("token", "env:STRIFE_TOKEN", "write token: env:NAME or file:PATH")
	commit := fs.String("commit", firstEnv("GITHUB_SHA", "CI_COMMIT_SHA", "GIT_COMMIT"), "commit the run tested")
	branch := fs.String("branch", firstEnv("GITHUB_HEAD_REF", "GITHUB_REF_NAME", "CI_COMMIT_REF_NAME", "BRANCH_NAME"), "branch the run tested")
	source := fs.String("source", ciRunURL(), "link back to the CI job")
	noHistory := fs.Bool("no-history", false, "do not upload history.jsonl")
	failOn := fs.String("fail-on", "regression", `exit 1 when the server reports this change ("regression", "violation" or "none")`)
	if err := fs.Parse(args); err != nil {
		return exitUsage, err
	}
	if fs.NArg() != 1 || *srvURL == "" || *proj == "" {
		return exitUsage, errors.New("usage: strife push --server URL --project NAME [--token env:STRIFE_TOKEN] RUN_DIR|campaign.json")
	}
	token, err := secret.Resolve(*tok)
	if err != nil || token == "" {
		return exitUsage, fmt.Errorf("--token: %v", err)
	}
	u, err := uploadFor(fs.Arg(0), !*noHistory)
	if err != nil {
		return exitUsage, err
	}
	u.Commit, u.Branch, u.Source = *commit, *branch, *source
	body, err := json.Marshal(u)
	if err != nil {
		return exitError, err
	}
	endpoint := strings.TrimRight(*srvURL, "/") + "/api/v1/projects/" + *proj + "/runs"
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return exitUsage, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		return exitError, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 300 {
		var e struct{ Error string }
		_ = json.Unmarshal(data, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(data))
		}
		return exitError, fmt.Errorf("server refused the run (%s): %s", resp.Status, e.Error)
	}
	var res server.PushResult
	if err := json.Unmarshal(data, &res); err != nil || res.Run == nil {
		return exitError, fmt.Errorf("unexpected answer: %s", data)
	}
	state := "stored"
	if !res.Created {
		state = "already stored"
	}
	signed := "unsigned"
	if res.Run.Attested {
		signed = "signed by " + res.Run.Signer
	}
	fmt.Fprintf(stdout, "%s %s %s: %s, %s, change: %s\n%s\n", state, res.Run.Kind, res.Run.Target, res.Run.Verdict, signed, changeText(res.Run.Change), res.URL)
	switch {
	case *failOn == "regression" && res.Run.Change == server.ChangeRegression,
		*failOn == "violation" && res.Run.Verdict == "violation":
		return exitViolation, nil
	}
	return exitOK, nil
}

func changeText(c string) string {
	if c == "" {
		return "same outcome as the previous run"
	}
	return c
}

// uploadFor reads a run directory or a campaign manifest.
func uploadFor(path string, withHistory bool) (*server.Upload, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.IsDir() {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var probe struct {
			CampaignID string `json:"campaign_id"`
		}
		if json.Unmarshal(data, &probe) == nil && probe.CampaignID != "" {
			u := &server.Upload{Campaign: data}
			if b, err := os.ReadFile(filepath.Join(filepath.Dir(path), "campaign-bundle.json")); err == nil {
				u.Bundle = b // from strife sign --campaign
			}
			return u, nil
		}
		path = filepath.Dir(path) // a report.json: push its directory
	}
	u := &server.Upload{}
	if u.Report, err = os.ReadFile(filepath.Join(path, "report.json")); err != nil {
		return nil, fmt.Errorf("%s: no report.json (or pass a campaign.json): %w", path, err)
	}
	if withHistory {
		if h, err := os.ReadFile(filepath.Join(path, "history.jsonl")); err == nil {
			u.History = string(h)
		}
	}
	if b, err := os.ReadFile(filepath.Join(path, "bundle.json")); err == nil {
		u.Bundle = b
	}
	return u, nil
}

func firstEnv(names ...string) string {
	for _, n := range names {
		if v := os.Getenv(n); v != "" {
			return v
		}
	}
	return ""
}

// ciRunURL links to the current CI job when the environment says where.
func ciRunURL() string {
	if s, r, id := os.Getenv("GITHUB_SERVER_URL"), os.Getenv("GITHUB_REPOSITORY"), os.Getenv("GITHUB_RUN_ID"); s != "" && r != "" && id != "" {
		return s + "/" + r + "/actions/runs/" + id
	}
	return firstEnv("CI_JOB_URL", "BUILD_URL")
}
