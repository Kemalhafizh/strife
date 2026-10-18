package transport

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestKubernetesExecArgs(t *testing.T) {
	k := &Kubernetes{Binary: "kubectl", Namespace: "db", Context: "kind-strife", Kubeconfig: "/tmp/kc"}
	got, err := k.ExecArgs(Node{Name: "n1", Pod: "etcd-0", Container: "etcd"}, "pkill -9 etcd")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--kubeconfig", "/tmp/kc", "--context", "kind-strife", "--namespace", "db",
		"exec", "etcd-0", "--container", "etcd", "--", "sh", "-c", "pkill -9 etcd"}
	if !slices.Equal(got, want) {
		t.Fatalf("got  %q\nwant %q", got, want)
	}
	// A namespace in the pod name wins over the configured one.
	got, _ = (&Kubernetes{Namespace: "db"}).ExecArgs(Node{Pod: "other/etcd-1"}, "true")
	if !slices.Equal(got, []string{"--namespace", "other", "exec", "etcd-1", "--", "sh", "-c", "true"}) {
		t.Fatalf("got %q", got)
	}
	if _, err := k.ExecArgs(Node{Name: "n1"}, "true"); err == nil {
		t.Fatal("a node without a pod was accepted")
	}
	if _, err := k.HostExec(context.Background(), Node{}, "true"); !errors.Is(err, ErrNoHost) {
		t.Fatalf("host exec: %v", err)
	}
	tr, err := New(Config{Type: "k8s", Namespace: "db"})
	if err != nil || tr.Name() != "kubernetes" {
		t.Fatalf("New: %v %v", tr, err)
	}
	if _, ok := tr.(IPResolver); !ok {
		t.Fatal("kubernetes transport cannot resolve pod IPs")
	}
}

// A pod's container is the one the node names, else the one kubectl exec
// reaches: the kubelet lists statuses by name, so the first status of a
// pod with a metrics sidecar is the sidecar's.
func TestPodContainerMatchesKubectlExec(t *testing.T) {
	a, b := strings.Repeat("a", 64), strings.Repeat("b", 64)
	statuses := "metrics=containerd://" + a + " postgresql=containerd://" + b + " "
	for _, c := range []struct {
		container, out, want string
	}{
		{"", "|postgresql|" + statuses, b},
		{"", "metrics|postgresql|" + statuses, a},
		{"metrics", "|postgresql|" + statuses, a},
		{"postgresql", "metrics|postgresql|" + statuses, b},
		{"", "|postgresql|postgresql= ", ""}, // created, not started
		{"", "|postgresql|", ""},             // no statuses yet
		{"exporter", "|postgresql|" + statuses, ""},
		{"", "garbage", ""},
	} {
		got, err := podContainer(Node{Container: c.container}, "pg-0", c.out)
		if got != c.want || (err == nil) != (c.want != "") {
			t.Errorf("container %q from %q: got %q, %v; want %q", c.container, c.out, got, err, c.want)
		}
	}
}
