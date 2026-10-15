# strife-agent: Low-Jitter Per-Node Daemon

`strife-agent` is a small, lightweight daemon that runs directly inside target
nodes (containers, VMs, or bare-metal machines) to apply chaos faults with
minimal jitter.

---

## Why strife-agent?

In distributed systems testing, timing precision matters. A fault scheduled at
`t = 1.050s` must hit the kernel as close to that millisecond boundary as
possible.

Conventional tools inject faults by shelling out (`docker exec container iptables ...`
or `ssh user@node tc ...`). That introduces enormous overhead and jitter:

| Transport | Fault Mechanism | Median Latency (P50) | 95th Percentile (P95) | Max Jitter |
|---|---|---|---|---|
| **Shell `docker exec`** | Process fork + exec + sh | ~45ms | ~85ms | >200ms |
| **Shell `ssh`** | SSH handshake + remote shell | ~60ms | ~120ms | >350ms |
| **`strife-agent`** | **TCP socket + Linux RTNETLINK** | **0.28ms** | **0.95ms** | **< 3ms** |

`strife-agent` reduces injection jitter by **over 50x**, allowing fine-grained
micro-window race condition testing (e.g. hitting Raft leader lease timeouts or
split-second failover windows).

---

## Architecture & Mechanisms

```
+-------------------------------------------------------------+
| Strife Runner                                               |
|  (pkg/transport/agent.go + FaultApplier)                    |
+------------------------------+------------------------------+
                               | Persistent HTTP Keep-Alive
                               v
+-------------------------------------------------------------+
| strife-agent daemon (cmd/strife-agent, port 9292)           |
+-------------------------------------------------------------+
   |                  |                     |               |
   v                  v                     v               v
RTNETLINK       Direct Signals         cgroup v2        iptables
(RTM_NEWQDISC)  (syscall.SIGSTOP)     (/sys/fs/cgroup)  (STRIFE chain)
  Netem Latency   Process Freeze        cgroup.freeze     Network Partitions
  Packet Loss     SIGKILL Crash         io.max Stall
```

1. **RTNETLINK (`RTM_NEWQDISC` / `RTM_DELQDISC`)**:
   Communicates directly with the Linux kernel network subsystem over `AF_NETLINK`
   sockets to create and replace `netem` qdiscs (`latency`, `jitter`, `loss`).
2. **Direct Syscall Signals**:
   Replaces `pkill -STOP` and `pkill -9` with direct `/proc` scanning and `syscall.Kill(pid, sig)`.
3. **Direct cgroup v2 File Writes**:
   Writes directly to `/sys/fs/cgroup/.../cgroup.freeze` and `/sys/fs/cgroup/.../io.max` without spawning shells.
4. **Idempotent Packet Filtering**:
   Maintains dedicated `STRIFE` iptables chain. Duplicate rules are discarded; teardown cleanly flushes rules.

---

## Quick Start

### 1. Launch Agent Cluster

```sh
cd examples/agent
docker compose up -d --build
```

### 2. Verify Agent Health & Capabilities

```sh
curl -s http://localhost:9291/health | jq
```

Output:
```json
{
  "node": "node1",
  "uptime_seconds": 12,
  "active_faults": 0,
  "capabilities": {
    "os": "linux",
    "arch": "amd64",
    "netlink": true,
    "cgroup_v2": true,
    "direct_signals": true,
    "iptables": true,
    "tc": true
  }
}
```

### 3. Run Chaos with Agent Transport

```sh
strife run --config strife.json
```

During execution, Strife logs exact jitter measurements:
```
  1.05s inject partition [node1] (jitter: 412µs)
  2.50s inject latency [node2] (jitter: 280µs)
  3.05s heal partition [node1] (jitter: 195µs)
...
injection jitter: samples=12 p50=320µs p95=890µs min=150µs max=1.2ms
```

### 4. Cleanup

```sh
docker compose down -v
```
