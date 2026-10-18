package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/kemalhafizh/strife/pkg/agent"
	"github.com/kemalhafizh/strife/pkg/nemesis"
)

// A DaemonSet agent receives each fault with the pod's container as its
// target, at the address of the agent on the pod's node; when Kubernetes has
// replaced the container, the transport looks its ID up again and retries.
func TestKubernetesAgentTargetsPods(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in kubectl is a shell script")
	}
	oldID, newID := strings.Repeat("a", 64), strings.Repeat("b", 64)

	var (
		mu   sync.Mutex
		reqs []agent.ApplyRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req agent.ApplyRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		reqs = append(reqs, req)
		mu.Unlock()
		resp := agent.ApplyResponse{Success: true, Applied: "ok:" + req.Target.ContainerID[:4]}
		if req.Target.ContainerID == oldID {
			resp = agent.ApplyResponse{Error: agent.ErrUnknownContainer.Error() + " aaaaaaaaaaaa: gone"}
			w.WriteHeader(http.StatusInternalServerError)
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())

	// The stand-in kubectl reports the old container ID first and the new
	// one afterwards, and an agent pod at 127.0.0.1 on node-a.
	dir := t.TempDir()
	calls := filepath.Join(dir, "calls")
	script := fmt.Sprintf(`#!/bin/sh
echo "$*" >> %[1]s
case "$*" in
*"--selector app=strife-agent"*"spec.nodeName=node-a"*) echo 127.0.0.1 ;;
*"get pod etcd-0"*)
  if [ -e %[1]s.seen ]; then echo "node-a containerd://%[3]s"; else touch %[1]s.seen; echo "node-a containerd://%[2]s"; fi ;;
*) echo "unexpected: $*" >&2; exit 1 ;;
esac
`, calls, oldID, newID)
	kubectl := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(kubectl, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	p, _ := strconv.Atoi(port)
	tr, err := New(Config{Type: "kubernetes-agent", Kubectl: kubectl, Namespace: "strife", AgentPort: p})
	if err != nil {
		t.Fatal(err)
	}
	ka, ok := tr.(*KubernetesAgent)
	if !ok || ka.Name() != "kubernetes-agent" {
		t.Fatalf("New returned %T", tr)
	}
	var _ FaultApplier = ka
	var _ HostResolver = ka

	n := Node{Name: "n1", Pod: "etcd-0", Container: "etcd", IP: "10.0.0.1"}
	env := nemesis.Env{Nodes: []nemesis.Target{{Name: "n1", IP: "10.0.0.1"}, {Name: "n2", IP: "10.0.0.2"}}, Interface: "eth0"}
	f := nemesis.Fault{ID: 7, Kind: nemesis.Partition, Nodes: []int{0}}
	applied, err := ka.InjectFault(context.Background(), n, f, env)
	if err != nil {
		t.Fatal(err)
	}
	if applied != "ok:bbbb" {
		t.Fatalf("applied %q", applied)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(reqs) != 2 || reqs[0].Target.ContainerID != oldID || reqs[1].Target.ContainerID != newID {
		t.Fatalf("want the old container, then the new one after re-resolving; got %+v", reqs)
	}
	if got := reqs[1].TargetIPs; len(got) != 1 || got[0] != "10.0.0.2" {
		t.Fatalf("partition peers %v", got)
	}
	if cg, err := ka.ResolveCgroup(context.Background(), n); err != nil || cg != Auto {
		t.Fatalf("cgroup: %q %v", cg, err)
	}
}
