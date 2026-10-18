package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

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
