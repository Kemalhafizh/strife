#!/usr/bin/env bash
# Deploys the node helper and prints the host faults this kind cluster can
# take: freeze needs cgroup v2; io-stall also needs the io controller
# delegated down to the pod's container cgroup (io.max present).
set -euo pipefail
kubectl apply -f examples/kubernetes/node-helper.yaml >&2
kubectl -n strife-system rollout status daemonset/strife-node --timeout=180s >&2
id=$(kubectl -n strife get pod etcd-0 -o jsonpath='{.status.containerStatuses[?(@.name=="etcd")].containerID}')
id=${id#*://}
cg=$(kubectl -n strife-system exec ds/strife-node -- sh -c "find /host/sys/fs/cgroup -maxdepth 8 -type d -name '*$id*' | head -n 1")
echo "etcd-0 cgroup: $cg" >&2
[ -n "$cg" ] || { echo "no cgroup found for etcd-0" >&2; exit 1; }
faults=freeze
if kubectl -n strife-system exec ds/strife-node -- test -e "$cg/io.max"; then
  faults="$faults,io-stall"
else
  echo "io controller not delegated to pods: io-stall skipped" >&2
fi
echo "$faults"
