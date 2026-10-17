# strife-agent: per-node fault daemon

`strife-agent` is a small, lightweight daemon that runs directly inside target
nodes (containers, VMs, or bare-metal machines) to apply faults with direct
syscalls instead of a shell per command.

---

## Why strife-agent?

In distributed systems testing, timing precision matters. A fault scheduled at
`t = 1.050s` must hit the kernel as close to that millisecond boundary as
possible.

Conventional tools inject faults by shelling out (`docker exec container iptables ...`
or `ssh user@node tc ...`): every transition pays for a process spawn, the
Docker or SSH round trip and a shell. Measured with `strife bench inject` on
a GitHub-hosted runner against a 3-node etcd cluster (50 transitions per row;
full table, method and caveats in [`docs/BENCHMARKS.md`](../../docs/BENCHMARKS.md#1-fault-injection-latency)):

| Fault | `docker exec` P50 / P95 | `strife-agent` P50 / P95 | Inside the agent (P50) |
|---|---|---|---|
| `pause` | 43.7 / 45.1 ms | **1.36 / 1.90 ms** | 372 µs |
| `kill` | 44.4 / 46.1 ms | **1.58 / 1.99 ms** | 495 µs |
| `partition` | 57.9 / 61.3 ms | **4.02 / 4.29 ms** | 2.61 ms |
| `latency` | 42.8 / 44.1 ms | 48.1 / 59.5 ms ¹ | **159 µs** |

¹ The agent's reply leaves through the interface it just delayed, so the
confirmation is late; the netem qdisc was in place 159 µs after the request
arrived. Partitions still run the `iptables` binary inside the agent; signals,
netem and cgroup writes are direct syscalls.

Signals and netem changes land ~30× sooner than through `docker exec`, which
matters when a fault has to hit a narrow window such as a leader-lease expiry
or a failover.

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
export STRIFE_AGENT_TOKEN=$(openssl rand -hex 16)   # every request must carry it
docker compose up -d --build
```

The agent refuses to listen on a non-loopback address without a token
(`-insecure-no-token` overrides that for throwaway labs), and compares tokens
in constant time.

### 2. Verify Agent Health & Capabilities

```sh
curl -s -H "Authorization: Bearer $STRIFE_AGENT_TOKEN" http://localhost:9291/health | jq
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

During the run Strife logs, for every transition, how long after its planned
instant every involved node confirmed it, then a summary; `report.json` keeps
the same `landed` and `latency` fields per transition:
```
   8.91s inject pause [1] (landed 1.4ms after plan)
  17.51s heal pause [1] (landed 1.5ms after plan)
...
fault timing: 14 transitions landed p50=1.6ms p95=4.3ms max=7.9ms after plan
```

To measure a cluster of your own:
```sh
strife bench inject --config strife.json --transports docker,agent --faults partition,pause,kill --cycles 50
```

### 4. Cleanup

```sh
docker compose down -v
```
