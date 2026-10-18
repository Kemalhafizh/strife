# Example: etcd on Kubernetes under Strife

The same three-member etcd cluster as [`examples/etcd`](../etcd), deployed
as a StatefulSet and driven through `kubectl exec`. Strife needs only a
kubeconfig: no SSH, no agent, no access to the nodes. CI runs this example in
a [kind](https://kind.sigs.k8s.io) cluster on every push (the `kubernetes`
job in `.github/workflows/live.yml`).

```sh
# kind cluster, the etcd image, the StatefulSet, and a route from this
# machine to the pod network (so Strife's clients reach the pods by IP):
scripts/live/kind-etcd.sh

strife plan --config examples/kubernetes/strife.json
strife run  --config examples/kubernetes/strife.json

scripts/live/kind-etcd.sh delete
```

## How the config maps to the cluster

```json
"nodes": [{ "name": "n1", "ip": "auto", "pod": "etcd-0", "container": "etcd" }, ...],
"transport": { "type": "kubernetes", "namespace": "strife" },
"db": { "kind": "etcd", "endpoints": ["http://{ip}:2379"], ... }
```

- `pod` is a pod name in `transport.namespace`, or `namespace/name`.
  `container` picks the container inside the pod.
- `"ip": "auto"` reads each pod's IP (`.status.podIP`) when the run starts;
  partitions drop traffic from those IPs. A StatefulSet pod keeps its IP
  across the container restarts the kill nemesis causes, but not across
  rescheduling, which is why it is read at every run.
- An endpoint containing `{ip}` expands into one endpoint per node.
- `transport` also takes `kubectl` (binary), `context`, `kubeconfig` and
  `node_helper` (below).

## What the pods need

- `NET_ADMIN` for `partition` (iptables), `latency` and `loss` (tc netem,
  which also needs `sch_netem` loaded on the nodes).
- `iptables`, `iproute2` and `procps` in the image.
- PID 1 must not be the database: here it is `sleep`, so `kill` can SIGKILL
  etcd and the nemesis restarts it with `db.start`. If the database is PID 1,
  Kubernetes restarts the whole container and the kill nemesis loses track.

## Host faults: `freeze` and `io-stall`

These act on the node's cgroups, which a pod cannot reach. Deploy the node
helper, a privileged DaemonSet that only sleeps until Strife execs into it,
and point the transport at it:

```sh
kubectl apply -f examples/kubernetes/node-helper.yaml
strife run --config examples/kubernetes/strife-host.json
```

```json
"transport": {
  "type": "kubernetes", "namespace": "strife",
  "node_helper": { "namespace": "strife-system", "selector": "app=strife-node" }
},
"nodes": [{ "name": "n1", "ip": "auto", "pod": "etcd-0", "container": "etcd", "cgroup": "auto" }, ...],
"nemesis": { "faults": ["freeze", "io-stall"], "io_device": "auto" }
```

- `"cgroup": "auto"` finds the container's cgroup on its node from the
  container ID the kubelet reports; `"io_device": "auto"` finds the disk that
  holds `/var/lib/containerd`.
- Each cgroup write runs in the helper on the node that hosts the target
  pod, and `freeze` is confirmed only once `cgroup.events` reports it.
- `io-stall` needs the io controller delegated down to pod cgroups (`io.max`
  present); `scripts/live/kind-host-faults.sh` checks that and CI skips the
  fault where the node does not delegate it.
- Anyone who can exec into the helper controls the node: keep its namespace
  restricted to whoever runs Strife.

## Limits

- `clock-skew` is refused: pods share their node's clock.
- Strife must reach the pod IPs. Inside the cluster that is a given; from
  outside, route the pod CIDR (as `kind-etcd.sh` does) or run Strife in a pod
  with a ServiceAccount allowed to `get pods` and `create pods/exec`.
