# Example: etcd on Kubernetes under Strife

The same three-member etcd cluster as [`examples/etcd`](../etcd), deployed
as a StatefulSet and driven two ways:

- **`kubernetes`**: through `kubectl exec`. Strife needs only a kubeconfig:
  no SSH, no agent, no access to the nodes. Each fault costs an exec round
  trip (~90 ms).
- **`kubernetes-agent`**: through [strife-agent as a DaemonSet](#the-strife-agent-daemonset),
  which applies each fault inside the target pod in about a millisecond and
  handles every fault kind, host faults included.

CI runs both in a [kind](https://kind.sigs.k8s.io) cluster on every push (the
`kubernetes` job in `.github/workflows/live.yml`).

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

## The strife-agent DaemonSet

One agent per node serves every pod on it. Strife sends each fault to the
agent on the target pod's node with the pod's container ID; the agent
applies it in the pod's network namespace (nf_tables partitions, netem), to
the pod's processes only (pause, kill, and the restart after a kill, run
inside the container with its environment), and to the pod's cgroup
(freeze, io-stall).

```sh
kubectl create namespace strife-system
kubectl -n strife-system create secret generic strife-agent --from-literal=token="$STRIFE_AGENT_TOKEN"
kubectl apply -f examples/kubernetes/strife-agent.yaml
strife run --config examples/kubernetes/strife-agent.json
```

```json
"transport": {
  "type": "kubernetes-agent", "namespace": "strife",
  "agent": { "namespace": "strife-system", "selector": "app=strife-agent" },
  "token": "env:STRIFE_AGENT_TOKEN"
},
"nodes": [{ "name": "n1", "ip": "auto", "pod": "etcd-0", "container": "etcd", "cgroup": "auto" }, ...],
"nemesis": { "faults": ["partition", "kill", "pause", "latency", "loss", "freeze", "io-stall"], "io_device": "auto" }
```

- The agent shares the node's network, so Strife reaches it at the node's
  address (port 9292, token required), and the node's PID namespace, so it
  sees the pods' processes. It runs privileged to enter their namespaces
  and write their cgroups.
- `-require-target` keeps it off the node itself. It refuses any fault that
  names no container, and network faults on a pod that uses the node's
  network (`hostNetwork`). A cgroup named in a request must lie inside the
  target container's (use `"auto"`). It runs no scripts: `/exec` and the
  shell fallback for unknown fault kinds are disabled. Its teardown heals
  only what it applied to containers, never the node's interface or packet
  filter.
- The pods need nothing: no `NET_ADMIN`, no iptables or tc in the image.
  Restarting after a kill runs `db.start` inside the container through
  `nsenter`, cloned into the container's cgroup, with its init's
  environment, working directory and credentials: user, groups, capability
  sets and `no_new_privs`. The container's seccomp and AppArmor/SELinux
  profiles are not applied to the restarted process. The agent's own binary
  does the switch inside the container, so it must be static (release
  builds and the image are).
- If Kubernetes replaces a container (new ID), Strife looks the ID up again.
- The agent heals everything it applied when it stops (`SIGTERM`).
- The token lets its holder apply any fault to any container on the nodes
  and restart processes in them: keep it, and the `strife-system`
  namespace, to whoever runs Strife.

## Limits

- `clock-skew` is refused: pods share their node's clock.
- Strife must reach the pod IPs. Inside the cluster that is a given; from
  outside, route the pod CIDR (as `kind-etcd.sh` does) or run Strife in a pod
  with a ServiceAccount allowed to `get pods` and `create pods/exec`.
