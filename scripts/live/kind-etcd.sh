#!/usr/bin/env bash
# Starts the examples/kubernetes etcd cluster in a kind cluster and routes the
# pod network to this machine, so Strife's clients reach the pods by IP.
#   scripts/live/kind-etcd.sh            create cluster, load image, deploy
#   scripts/live/kind-etcd.sh delete     tear it all down
set -euo pipefail
name=${KIND_CLUSTER:-strife}
image=${STRIFE_ETCD_IMAGE:-strife-etcd:local}
sudo=""; [ "$(id -u)" = 0 ] || sudo="sudo"

if [ "${1:-}" = delete ]; then
  kind delete cluster --name "$name"
  exit 0
fi

kind get clusters | grep -qx "$name" || kind create cluster --name "$name" --wait 180s
docker image inspect "$image" >/dev/null 2>&1 || docker build -t "$image" examples/etcd
kind load docker-image "$image" --name "$name"

node_ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$name-control-plane")
pod_cidr=$(kubectl get node "$name-control-plane" -o jsonpath='{.spec.podCIDR}')
$sudo ip route replace "$pod_cidr" via "$node_ip"
echo "pod network $pod_cidr routed via $node_ip"

kubectl apply -f examples/kubernetes/etcd.yaml
kubectl -n strife rollout status statefulset/etcd --timeout=180s
for _ in $(seq 1 60); do
  if kubectl -n strife exec etcd-0 -c etcd -- etcdctl --endpoints http://127.0.0.1:2379 endpoint health --cluster >/dev/null 2>&1; then
    kubectl -n strife get pods -o wide
    exit 0
  fi
  sleep 2
done
kubectl -n strife exec etcd-0 -c etcd -- tail -30 /var/log/etcd.log || true
exit 1
