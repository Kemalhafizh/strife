// Command sitedata writes the data files the website is built from:
//
//	presets.json     the playground's scenarios, run with the native engine,
//	                 and the lists its form offers
//	release.json     the latest GitHub release and its files
//	key.json         the release signing key, once release.pub is published
//	compliance.json  the provision each compliance control cites
//
// The browser compares its WebAssembly runs with presets.json, so a visitor
// sees whether they reproduced the native engine's hashes.
//
// Usage: go run ./cmd/sitedata -out ../data -root ../.. [-offline]
package main

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/kemalhafizh/strife/pkg/attest"
	"github.com/kemalhafizh/strife/pkg/compliance"
	"github.com/kemalhafizh/strife/pkg/history"
	"github.com/kemalhafizh/strife/pkg/report"
	"github.com/kemalhafizh/strife/site/wasm/bridge"
)

// The scenarios come from the README and the CI gauntlet: each built-in bug
// under the faults that expose it, and correct Raft for contrast.
var presets = []struct {
	ID  string
	Req bridge.SimRequest
}{
	{"unsafe-fsync", bridge.SimRequest{System: "raft-unsafe-fsync", Faults: "kill,io-stall", Seed: 13}},
	{"stale-reads", bridge.SimRequest{System: "raft-stale-reads", Faults: "partition", Seed: 2}},
	{"lease", bridge.SimRequest{System: "raft-lease", Faults: "clock-skew,partition", Seed: 2}},
	{"correct", bridge.SimRequest{System: "raft", Faults: "all", Seed: 7}},
}

func main() {
	out := flag.String("out", "data", "directory to write the data files into")
	root := flag.String("root", "../..", "repository root (for release.pub)")
	repo := flag.String("repo", "Kemalhafizh/strife", "GitHub repository whose latest release the download page offers")
	offline := flag.Bool("offline", false, "do not ask GitHub for the latest release")
	flag.Parse()
	if err := os.MkdirAll(*out, 0o755); err != nil {
		fail(err)
	}
	steps := []struct {
		file string
		make func() (any, error)
	}{
		{"presets.json", runPresets},
		{"release.json", func() (any, error) { return latestRelease(*repo, *offline) }},
		{"key.json", func() (any, error) { return releaseKey(filepath.Join(*root, "release.pub")) }},
		{"compliance.json", controls},
	}
	for _, s := range steps {
		v, err := s.make()
		if err != nil {
			fail(fmt.Errorf("%s: %w", s.file, err))
		}
		data, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			fail(err)
		}
		if err := os.WriteFile(filepath.Join(*out, s.file), append(data, '\n'), 0o644); err != nil {
			fail(err)
		}
	}
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "sitedata:", err)
	os.Exit(1)
}

type preset struct {
	ID          string         `json:"id"`
	System      string         `json:"system"`
	Faults      string         `json:"faults"`
	Seed        uint64         `json:"seed"`
	Verdict     string         `json:"verdict"`
	Events      uint64         `json:"events"`
	Simulated   string         `json:"simulated"`
	Ops         history.Stats  `json:"ops"`
	Schedule    []bridge.Fault `json:"schedule"`
	Failure     string         `json:"failure,omitempty"`
	TraceHash   string         `json:"trace_hash"`
	HistoryHash string         `json:"history_hash"`
	Command     string         `json:"command"`
}

func runPresets() (any, error) {
	type output struct {
		Engine   string          `json:"engine"`
		Platform string          `json:"platform"`
		Presets  []preset        `json:"presets"`
		Systems  []bridge.System `json:"systems"`
		Faults   []string        `json:"faults"`
		Models   []string        `json:"models"`
		Limits   map[string]any  `json:"limits"`
	}
	o := output{
		Engine:   report.Version,
		Platform: runtime.GOOS + "/" + runtime.GOARCH,
		Systems:  bridge.Systems(),
		Faults:   bridge.FaultKinds(),
		Models:   bridge.Models(),
		Limits: map[string]any{
			"duration_s": bridge.MaxDuration.Seconds(),
			"nodes":      bridge.MaxNodes,
			"clients":    bridge.MaxClients,
		},
	}
	for _, p := range presets {
		res, err := bridge.Simulate(p.Req)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", p.ID, err)
		}
		x := preset{
			ID: p.ID, System: res.System, Faults: p.Req.Faults, Seed: res.Seed,
			Verdict: res.Verdict, Events: res.Events, Simulated: res.Simulated,
			Ops: res.Ops, Schedule: res.Faults, TraceHash: res.TraceHash,
			HistoryHash: res.HistoryHash, Command: res.Command,
		}
		if len(res.Failures) > 0 {
			x.Failure = "key " + res.Failures[0].Key + ": " + res.Failures[0].Summary
		}
		o.Presets = append(o.Presets, x)
	}
	return o, nil
}

type asset struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
	Kind string `json:"kind"` // archive, checksums, sbom, attestation or other
	OS   string `json:"os,omitempty"`
	Arch string `json:"arch,omitempty"`
}

type release struct {
	Available bool    `json:"available"`
	Checked   bool    `json:"checked"` // false when built with -offline
	Tag       string  `json:"tag,omitempty"`
	Published string  `json:"published,omitempty"`
	Page      string  `json:"page,omitempty"`
	Assets    []asset `json:"assets,omitempty"`
}

var archiveName = regexp.MustCompile(`^strife_.+_(linux|darwin|windows)_(amd64|arm64)\.(tar\.gz|zip)$`)

// latestRelease asks GitHub for the latest release. A repository without one
// yields Available: false; any other failure fails the build, so a hiccup
// never publishes a download page without downloads.
func latestRelease(repo string, offline bool) (*release, error) {
	if offline {
		return &release{}, nil
	}
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/"+repo+"/releases/latest", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "strife-site")
	if tok := os.Getenv("GITHUB_TOKEN"); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return &release{Checked: true}, nil
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("GitHub answered %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var gh struct {
		Tag       string `json:"tag_name"`
		Published string `json:"published_at"`
		Page      string `json:"html_url"`
		Assets    []struct {
			Name string `json:"name"`
			Size int64  `json:"size"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&gh); err != nil {
		return nil, err
	}
	r := &release{Available: true, Checked: true, Tag: gh.Tag, Published: gh.Published, Page: gh.Page}
	for _, a := range gh.Assets {
		x := asset{Name: a.Name, Size: a.Size, URL: a.URL, Kind: "other"}
		switch m := archiveName.FindStringSubmatch(a.Name); {
		case m != nil:
			x.Kind, x.OS, x.Arch = "archive", m[1], m[2]
		case a.Name == "SHA256SUMS":
			x.Kind = "checksums"
		case strings.HasSuffix(a.Name, ".spdx.json"):
			x.Kind = "sbom"
		case strings.HasSuffix(a.Name, ".intoto.json"):
			x.Kind = "attestation"
		}
		r.Assets = append(r.Assets, x)
	}
	return r, nil
}

type key struct {
	Available bool   `json:"available"`
	KeyID     string `json:"key_id,omitempty"`
	PEM       string `json:"pem,omitempty"`
}

func releaseKey(path string) (*key, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &key{}, nil
	}
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("release.pub is not a PEM file")
	}
	k, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	pub, ok := k.(ed25519.PublicKey)
	if !ok {
		return nil, errors.New("release.pub is not an Ed25519 key")
	}
	return &key{Available: true, KeyID: attest.KeyID(pub), PEM: string(data)}, nil
}

func controls() (any, error) {
	type control struct {
		ID        string `json:"id"`
		Framework string `json:"framework"`
		Citation  string `json:"citation"`
	}
	var out []control
	for id, c := range compliance.Citations {
		fw, _, _ := strings.Cut(id, ".")
		out = append(out, control{ID: id, Framework: fw, Citation: c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
