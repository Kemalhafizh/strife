package transport

import (
	"context"
	"errors"
	"slices"
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
