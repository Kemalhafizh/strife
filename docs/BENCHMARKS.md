# Benchmarks and validation

Every number in Strife's documentation comes from this page, and every number
on this page comes with the command that produced it, the machine it ran on
and the date. Numbers without a reproduction do not belong in the README.

Measured on 2026-10-17 and 2026-10-18 at commits `e5527d8`, `0249500`,
`1dc5577`, `32ef8a8`, `69d5930` and `89a26a7` (CI, named per table) and on the working
tree of the same branch (local). Shared
CI runners are noisy: treat a single run as a sample, not a specification.
Where two runs differ, both are shown.

## 1. Fault-injection latency

**Question:** when Strife plans a fault for instant *t*, how late does it take
effect?

**Method:** `strife bench inject` applies one fault kind at a time to one node
(rotating through the cluster), at a fixed period, against a 3-node etcd
cluster in Docker, and records for each transition the time from the planned
instant until every involved node confirmed the change. 50 cycles per row
(inject and heal are separate rows), one transition every 300 ms.
For `strife-agent`, "agent-side" is the time inside the agent from receiving
the request until the kernel had applied the change (slowest node): for a
pause or kill, until `/proc` shows the process stopped or exited.

```sh
# .github/workflows/live.yml, job "agent"
bin/strife bench inject --config examples/etcd/strife-agent.json \
  --transports docker,agent --faults partition,pause,kill,latency,loss \
  --cycles 50 --period 300ms --markdown bench-inject.md --json bench-inject.json
```

**Host:** GitHub-hosted `ubuntu-24.04`, 4 vCPU AMD EPYC 7763, Linux
6.17.0-1022-azure, Go 1.23.12. Run
[39056797048](https://github.com/Kemalhafizh/strife/actions/runs/39056797048)
at commit `1dc5577`: partitions through nf_tables, and pause and kill
confirmed in `/proc`.

| Transport | Fault | Edge | P50 | P95 | Max | Agent-side P50 / P95 | Errors |
|---|---|---|---|---|---|---|---|
| docker exec | partition | inject | 72.6 ms | 76.4 ms | 80.1 ms | – | 0 / 50 |
| docker exec | partition | heal | 73.2 ms | 78.1 ms | 92.2 ms | – | 0 / 50 |
| docker exec | pause | inject | 49.9 ms | 53.1 ms | 57.5 ms | – | 0 / 50 |
| docker exec | kill | inject | 51.5 ms | 54.0 ms | 54.6 ms | – | 0 / 50 |
| docker exec | latency | inject | 49.5 ms | 53.7 ms | 62.4 ms | – | 0 / 50 |
| docker exec | loss | inject | 48.8 ms | 51.0 ms | 51.5 ms | – | 0 / 50 |
| strife-agent | partition | inject | 1.22 ms | 1.77 ms | 1.96 ms | 147 µs / 294 µs | 0 / 50 |
| strife-agent | partition | heal | 1.29 ms | 1.80 ms | 1.91 ms | 144 µs / 224 µs | 0 / 50 |
| strife-agent | pause | inject | 1.64 ms | 2.96 ms | 3.49 ms | 546 µs / 1.64 ms | 0 / 50 |
| strife-agent | pause | heal | 1.71 ms | 2.18 ms | 2.19 ms | 520 µs / 717 µs | 0 / 50 |
| strife-agent | kill | inject | 1.67 ms | 4.14 ms | 4.48 ms | 664 µs / 2.85 ms | 0 / 50 |
| strife-agent | kill | heal | 1.50 ms | 1.99 ms | 2.01 ms | 350 µs / 437 µs | 0 / 50 |
| strife-agent | latency | inject | 49.0 ms ¹ | 59.3 ms ¹ | 61.7 ms ¹ | 171 µs / 232 µs | 0 / 50 |
| strife-agent | latency | heal | 1.22 ms | 1.72 ms | 1.80 ms | 201 µs / 267 µs | 0 / 50 |
| strife-agent | loss | inject | 1.31 ms | 219 ms ¹ | 419 ms ¹ | 169 µs / 212 µs | 0 / 50 |
| strife-agent | loss | heal | 1.30 ms | 1.82 ms | 119 ms ¹ | 202 µs / 266 µs | 0 / 50 |

¹ The agent's confirmation leaves through the interface it has just impaired:
with 50 ms ± 10 ms of netem delay the reply itself is delayed by ~50 ms, and
with 20 % loss it sometimes needs a TCP retransmission. The fault took effect
when the kernel call returned (agent-side column); only the confirmation was
late. The other Docker heal rows (omitted) are within 1 ms of their inject
rows.

**Reading it:**

- `docker exec` costs ~49–73 ms per transition on this runner, almost all of
  it in the Docker CLI and the exec round trip, with a tight spread
  (σ ≈ 1–3 ms).
- `strife-agent` lands every fault in ~1.2–1.7 ms at P50 and under 5 ms at
  the maximum: ~30–60× sooner than `docker exec`. Inside the agent the
  kernel work is ~145 µs for partitions and 170–200 µs for netem changes.
  Pause and kill take 350–660 µs because the agent waits until `/proc`
  shows each process stopped or exited (a signal is delivered when its
  target is next scheduled, not when `kill(2)` returns).
- Partitions go through nf_tables over netlink, one batch per change, with
  no binary to fork. The same job on earlier commits, P50 end to end and
  inside the agent:

  | Run | Commit | Runner | Partition inject | Partition heal |
  |---|---|---|---|---|
  | [39039844498](https://github.com/Kemalhafizh/strife/actions/runs/39039844498) | `e5527d8`, `iptables` | EPYC 9V74 | 4.02 ms (2.61 ms) | 7.44 ms (5.99 ms) |
  | [39054819049](https://github.com/Kemalhafizh/strife/actions/runs/39054819049) | `0249500`, nf_tables | EPYC 9V45 | 1.12 ms (149 µs) | 0.99 ms (139 µs) |
  | 39056797048 (above) | `1dc5577`, nf_tables | EPYC 7763 | 1.22 ms (147 µs) | 1.29 ms (144 µs) |

  Inside the agent, nf_tables is ~18× (inject) and ~40× (heal) faster than
  forking `iptables`.
- Inserting a rule costs the kernel ~40–60 µs; deleting one costs a
  synchronize-RCU grace period (~14 ms). So a heal inserts an `accept` for
  the address at the head of the chain instead of deleting its `drop`, and
  the chain is rebuilt to its minimal form after 250 ms without a change (or
  at 128 rules). `go test ./pkg/agent -bench PartitionBackends` (root,
  private network namespace) measures one inject and heal cycle against a
  live loopback socket: nftables 43 µs / 39 µs, `iptables` 5.8 ms / 18.1 ms.
- `scripts/live/check-agent-partition.sh` checks that a partition applied
  through the agent really cuts the traffic between two etcd containers
  (and that the agent reports `filter:nftables`), then that the heal restores
  it.
- The ordering is stable across runners; the absolute values move with them
  (Docker partitions took 50 ms on the EPYC 9V45 and 73 ms on this 7763).

**cgroup faults** (`freeze`, `io-stall`) run on the Docker host, not in the
container, so they do not go through `docker exec`. Job `cgroup`, run
39039844498, AMD EPYC 7763, 20 cycles every 500 ms:

| Fault | Edge | P50 | P95 | Max | Errors |
|---|---|---|---|---|---|
| freeze | inject | 9.56 ms | 10.1 ms | 10.3 ms | 0 / 20 |
| freeze | heal | 9.63 ms | 10.3 ms | 10.5 ms | 0 / 20 |
| io-stall | inject | 8.33 ms | 8.74 ms | 8.95 ms | 0 / 20 |
| io-stall | heal | 8.30 ms | 9.78 ms | 10.3 ms | 0 / 20 |

`freeze` is confirmed only once `cgroup.events` reports `frozen 1`, not when
the write to `cgroup.freeze` returns. The same job checks that `io.max` really
stalls a container's writes (`scripts/live/check-io-stall.sh`).

**cgroup faults on Kubernetes**, through the node helper DaemonSet
(`kubectl exec` into the privileged helper pod on the target pod's node).
Job `kubernetes`, run 39054819049, a single-node kind cluster, 10 cycles
every 500 ms:

```sh
bin/strife bench inject --config examples/kubernetes/strife-host.json \
  --transports kubernetes --faults freeze,io-stall --cycles 10 --period 500ms
```

| Fault | Edge | P50 | P95 | Max | Errors |
|---|---|---|---|---|---|
| freeze | inject | 91.5 ms | 215 ms | 215 ms | 0 / 10 |
| freeze | heal | 89.9 ms | 116 ms | 116 ms | 0 / 10 |
| io-stall | inject | 95.2 ms | 111 ms | 111 ms | 0 / 10 |
| io-stall | heal | 89.4 ms | 93.4 ms | 93.4 ms | 0 / 10 |

Almost all of the ~90 ms is `kubectl exec` (API server, kubelet, CRI exec);
the cgroup write itself is the same ~10 ms as on Docker. The same job then
runs etcd for 45 s under freeze and io-stall and requires a linearizable
verdict (14,297 operations, LINEARIZABLE). The strife-agent DaemonSet below
brings these to ~1.5 ms.

**Kubernetes through the strife-agent DaemonSet** (`kubernetes-agent`)
against `kubectl exec` (`kubernetes`), on the same kind cluster and the same
3-pod etcd StatefulSet. One agent per node applies each fault inside the
target pod: in its network namespace, to its processes, on its cgroup.

```sh
# .github/workflows/live.yml, job "kubernetes"
bin/strife bench inject --config examples/kubernetes/strife-agent.json \
  --transports kubernetes,kubernetes-agent --faults partition,kill,pause,latency,loss \
  --cycles 20 --period 500ms
bin/strife bench inject --config examples/kubernetes/strife-agent.json \
  --transports kubernetes-agent --faults freeze,io-stall --cycles 20 --period 500ms
```

Run [39109099995](https://github.com/Kemalhafizh/strife/actions/runs/39109099995)
at commit `69d5930`, a single-node kind cluster on AMD EPYC 7763, 20 cycles
every 500 ms (the `kubectl exec` host-fault rows: the node helper above, 10
cycles in the same run):

| Fault | Edge | `kubectl exec` P50 / P95 | DaemonSet agent P50 / P95 | Inside the agent, P50 |
|---|---|---|---|---|
| partition | inject | 229 ms / 236 ms | **1.49 ms** / 2.32 ms ¹ | 414 µs |
| partition | heal | 224 ms / 236 ms | **1.13 ms** / 1.92 ms | 421 µs |
| kill | inject | 138 ms / 144 ms | **2.75 ms** / 3.08 ms | 1.46 ms |
| kill | heal (restart) | 140 ms / 150 ms | **4.49 ms** / 6.25 ms | 3.51 ms |
| pause | inject | 133 ms / 142 ms | **2.78 ms** / 3.83 ms | 1.71 ms |
| pause | heal | 139 ms / 143 ms | **2.83 ms** / 3.31 ms | 1.61 ms |
| latency | inject | 134 ms / 137 ms | **1.38 ms** / 1.81 ms | 297 µs |
| latency | heal | 133 ms / 139 ms | **1.29 ms** / 1.91 ms | 326 µs |
| loss | inject | 134 ms / 139 ms | **1.25 ms** / 1.72 ms | 304 µs |
| loss | heal | 140 ms / 145 ms | **1.26 ms** / 1.76 ms | 308 µs |
| freeze | inject | 135 ms / 328 ms | **1.86 ms** / 197 ms ¹ | 328 µs |
| freeze | heal | 135 ms / 154 ms | **1.48 ms** / 1.96 ms | 356 µs |
| io-stall | inject | 131 ms / 151 ms | **1.33 ms** / 1.62 ms | 312 µs |
| io-stall | heal | 138 ms / 146 ms | **1.31 ms** / 1.80 ms | 320 µs |

No transition failed (0 errors in every row).

¹ One slow transition per benchmark run: 329 ms for a partition, ~200 ms for
a freeze, the first of each run. Before its first fault the transport had
not yet looked up the pods' nodes and container IDs (two `kubectl` calls per
pod, cached afterwards). It now does them during setup: in run
[39123114580](https://github.com/Kemalhafizh/strife/actions/runs/39123114580)
(commit `89a26a7`, EPYC 9V45) the slowest partition inject took 3.88 ms and
the slowest freeze inject 2.6 ms.

**Reading it:**

- At P50 the DaemonSet agent lands network and cgroup faults 73–198×
  sooner than `kubectl exec` (1.1–1.9 ms), signals ~50× sooner (2.8 ms) and
  the restart after a kill 31× sooner (4.5 ms).
- Unlike the Docker agent above, the latency and loss rows show no late
  confirmations: the agent replies over the node's network, not through the
  pod interface it has just impaired.
- Pause and kill wait until `/proc` shows every process of the container
  stopped or exited (1.5–1.7 ms inside the agent).
- The restart after a kill takes 4.5 ms because it runs as the container
  would. The agent's own binary enters the container's namespaces, takes on
  its init's user, groups, capability sets and `no_new_privs`, reports back,
  and only then starts `db.start`. Restarting as root, as at commit
  `32ef8a8` (run [39084896919](https://github.com/Kemalhafizh/strife/actions/runs/39084896919)),
  took 1.68 ms.
- `kubectl exec` partitions take ~230 ms against ~135 ms for the other
  faults: a partition changes every node, and the slowest of the three
  parallel execs decides.

The same job runs etcd for 60 s through the DaemonSet with seed 710, whose
schedule draws all seven kinds: packet loss, a kill (the database restarted
inside its pod), latency, pause, io-stall, two partitions and a freeze. Run
[39123114580](https://github.com/Kemalhafizh/strife/actions/runs/39123114580):
14,221 operations, LINEARIZABLE, its 16 transitions confirmed at P50 1.15 ms,
maximum 3.47 ms. (Seed 42, used before, drew only pause, packet loss and
io-stall: 19,269 operations, LINEARIZABLE.)
`scripts/live/check-k8s-agent-partition.sh` checks that a partition through
the agent cuts `etcd-0` from `etcd-1` and from nothing else, that a fault
naming no pod is refused, and that the heal restores the traffic.

**Not measured:** the `ssh` transport (it shells out per command, so expect
the same order as `docker exec` plus a network round trip), and `clock-skew`.

## 2. Checker throughput

`go test -bench` on the working tree, one core per check (`Parallelism: 1`).
Host: 4 vCPU Intel Xeon @ 2.10 GHz, Linux 6.18, Go 1.24.7.

```sh
go test ./pkg/checker -run '^$' -bench 'Linearizable|Indeterminate|Violation' -benchtime 3x
go test ./pkg/txn     -run '^$' -bench CheckAppend -benchtime 3x
```

**Linearizability (WGL search with memoisation), one register, every
operation on the same key** (the worst case for P-compositionality). The
search tries candidates earliest deadline first and only one per class of
interchangeable operations (same input and output), and splitting a history
by key copies each operation once. "Before" is commit `640c9bc` (call-order
search), measured on the same host in the same session. Each figure is the
median of three runs; single runs vary by up to ±25 % on this host.

| History | Concurrent processes | Indeterminate writes | Throughput | Steps / op | Before |
|---|---|---|---|---|---|
| 1,000 ops | 5 | 0 % | 746 k ops/s | 1.2 | 549 k ops/s |
| 10,000 ops | 5 | 0 % | 708 k ops/s | 1.1 | 375 k ops/s |
| 100,000 ops | 5 | 0 % | 575 k ops/s | 1.1 | 368 k ops/s |
| 1,000 ops | 20 | 0 % | 352 k ops/s | 5.4 | 92 k ops/s |
| 10,000 ops | 20 | 0 % | 244 k ops/s | 6.5 | 95 k ops/s |
| 100,000 ops | 20 | 0 % | 219 k ops/s | 7.7 | 106 k ops/s |
| 1,000 ops | 50 | 0 % | 310 k ops/s | 4.3 | 63 k ops/s |
| 10,000 ops | 50 | 0 % | 110 k ops/s | 23 | **unknown** (50 M steps) |
| 100,000 ops | 50 | 0 % | 18 k ops/s | 110 | **unknown** |
| 10,000 ops | 10 | 1 % | 614 k ops/s | 1.9 | 337 k ops/s |
| 10,000 ops | 10 | 5 % | 437 k ops/s | 3.5 | 324 k ops/s |

Every row now reaches a verdict within the default budget (`MaxSteps` = 50
million per key); before, 50 processes racing on one key ran out and
answered `unknown`. Histories with 20 processes check 2–4× faster, and the
easy ones (5 processes) 1.4–1.9× faster: most of their time went into
copying operations while splitting the history by key, which the profile
showed at 85 % of a 100,000-operation check (392 MB allocated, now
100 MB). An exhausted budget still answers `unknown` (exit code 3), never
`linearizable`.

**Proving a violation:** a stale read is diagnosed directly, without
exhausting the search: 2.8 M ops/s at 1,000 ops, 1.6 M ops/s at 100,000
ops (0.86 M and 0.53 M before), with or without indeterminate writes. The
pure search, for comparison, refutes at ~57–63 k ops/s (30–37 k before).

**List-append (Elle-style dependency graph), strict serializability, 10 keys,
transactions of up to 8 micro-ops:** 41 k txns/s at 1,000 transactions,
57 k txns/s at 10,000, 48 k txns/s at 50,000.

## 3. Checker correctness

Cross-checks against brute force, on small random histories where every
serial order can be enumerated:

```sh
STRIFE_APPEND_ITERS=200000 go test ./pkg/txn -run 'AppendCompleteness' -v
go test ./pkg/txn -run 'AppendAgainstBruteForce|RegisterTxnAgainstBruteForce' -v
```

| Checker | Histories | Valid histories flagged | Invalid histories caught |
|---|---|---|---|
| list-append, strict serializable | 200,000 (150,030 valid) | **0** | 49,877 / 49,970 (**99.81 %**) |
| register transactions, serializable and strict | 40,000 | **0** | (soundness only) |
| linearizability search, kv / counter / queue / set | 100,000 per model | **0** disagreements with brute force, either way | |

The linearizability rows run with `STRIFE_BRUTE_ITERS=100000 go test
./pkg/checker -run Interchangeable` and guard the class reduction above:
dropping the output from the class key makes the queue cross-check fail.

Soundness (never flagging a history that has a valid order) is required and
enforced by the tests. Completeness cannot be total for list-append: an
anomaly can hide behind an indeterminate transaction, or a corrupted read can
remain explainable by another version order. The 93 misses are of that kind.

Continuous fuzzing (`ci.yml`, job `fuzz`) runs `FuzzCheckAppendSound`,
`FuzzReadJSONL`, `FuzzParseWasm` and the wire-protocol decoders
(`FuzzPostgresMessages`, `FuzzMySQLPackets`, `FuzzKafkaRecords`, `FuzzRESP`).
It found a stack-underflow panic in the Wasm interpreter and an integer
overflow in the MySQL packet decoder, both fixed; their inputs are kept as
regression corpus under `pkg/*/testdata/fuzz`. Every decoder bounds what it
allocates by what actually arrives, not by lengths announced on the wire.

## 4. Bug detection in the simulator

Each row sweeps 200 seeds of a 5-node cluster for 10 simulated seconds
(`--seeds 1..200`). A system with a known bug is only ever reported as
violating; correct Raft never is.

```sh
strife sim --system raft-stale-reads  --faults partition           --seeds 1..200
strife sim --system raft-lease        --faults clock-skew,partition --seeds 1..200
strife sim --system raft-unsafe-fsync --faults kill,io-stall        --seeds 1..200
strife sim --system raft --faults partition,latency,loss,kill,pause,freeze,io-stall,clock-skew --seeds 1..200
```

| System | Bug | Faults | Violating seeds |
|---|---|---|---|
| `raft` | none | all eight kinds | **0 / 200** |
| `raft-stale-reads` | deposed leader serves local reads | partition | 75 / 200 (37.5 %) |
| `raft-lease` | lease timed with the wall clock | clock-skew, partition | 10 / 200 (5 %); 0 / 200 without clock-skew |
| `raft-unsafe-fsync` | acks before `fsync` | kill, io-stall | 7 / 200 (3.5 %); 0 / 200 under partition alone |

The sweeps take 17–34 s each on the 4-vCPU host above.

## 5. Live clusters

Every push runs `.github/workflows/live.yml`. Each row is an assertion: a run
that does not produce the expected verdict fails the job.

| Job | System | Faults | Checked as | Expected and observed |
|---|---|---|---|---|
| etcd | etcd 3.5, 3 nodes | partition, kill, pause, latency ², loss ² | linearizable (kv) | pass |
| etcd | same, transactions | same | strict serializable (list-append) | pass |
| agent | etcd via strife-agent, partitions through nf_tables | partition, kill, pause | linearizable | pass |
| agent | two etcd containers | one partition, then its heal | traffic cut, then restored | pass |
| cgroup | etcd | freeze, io-stall | linearizable | pass |
| kubernetes | etcd StatefulSet in kind | partition, kill, pause, latency ², loss ² | linearizable | pass |
| kubernetes | same, through the node helper | freeze, io-stall ³ | linearizable | pass |
| kubernetes | same, through the strife-agent DaemonSet | all seven kinds ², ³ (seed 710 draws each, a kill included) | linearizable | pass |
| kubernetes | two etcd pods, through the DaemonSet agent | one partition, then its heal; a fault naming no pod | only that traffic cut, then restored; refused | pass |
| valkey | Valkey 8, primary + 2 async replicas | partition, pause | linearizable | **violation** (stale replica reads) |
| valkey | Valkey 8, single primary, `appendfsync always` | kill -9 | linearizable | pass |
| postgres | PostgreSQL 17 primary, SERIALIZABLE | kill -9, pause | strict serializable | pass, except once: **G2** among three racing commits ⁴ |
| postgres | PostgreSQL 17, REPEATABLE READ | kill -9, pause | snapshot isolation | pass |
| postgres | same history | – | serializable | **violation** (G2, write skew) |
| postgres | async standbys | partition | strict serializable | **violation** (G-realtime); `shrink --live` reduces it to zero faults |
| mysql | MySQL 8.4, SERIALIZABLE | freeze | strict serializable | pass |
| mysql | MySQL 8.4, REPEATABLE READ | none | snapshot isolation | **violation** (internal, G-single) |
| tidb | TiDB 8.5, optimistic | freeze | snapshot isolation | pass |
| tidb | TiDB 8.5, pessimistic | none | snapshot isolation | **violation** (internal, G-single) |
| kafka | Kafka 3.9.1 and 4.1.0 KRaft | partition, kill -9, pause | strict serializable (list-append) | no lost or reordered records; stale reads (G-realtime) tolerated |

² When the runner kernel has `sch_netem` (it has on every run so far).
³ When the kind node's cgroup delegates `io.max` (it has on every run so far);
`scripts/live/kind-host-faults.sh` drops io-stall otherwise.

⁴ Run [39056639380](https://github.com/Kemalhafizh/strife/actions/runs/39056639380)
(PostgreSQL 17.11): three transactions that completed within 0.2 ms of each
other formed an anti-dependency cycle, read directly from their results, with
no fault active. The same statements replayed one at a time by three `psql`
sessions are aborted as they should be, so it takes the commits racing.
pgsql-bugs has [2026 reports](https://www.postgresql.org/message-id/165342c0-0c75-461e-b334-b997639ad48d%40aphyr.com)
of SSI false negatives (`ON CONFLICT`, savepoints) with fixes in review;
whether this one is among them is not established. The history is kept as
[`testdata/anomalies/postgres-17.11-serializable-g2.jsonl`](../testdata/anomalies/README.md)
and checked in `go test ./pkg/txn`.

Runs [39054819049](https://github.com/Kemalhafizh/strife/actions/runs/39054819049)
(commit `0249500`), [39056797048](https://github.com/Kemalhafizh/strife/actions/runs/39056797048)
(commit `1dc5577`), [39109099995](https://github.com/Kemalhafizh/strife/actions/runs/39109099995)
(commit `69d5930`) and [39123114580](https://github.com/Kemalhafizh/strife/actions/runs/39123114580)
(commit `89a26a7`) passed all ten jobs.
