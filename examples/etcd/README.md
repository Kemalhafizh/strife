# Example: 3-node etcd under Strife

Requirements: Linux docker host (container IPs must be routable from the host),
Docker Compose v2, and Strife built from the repository root. CI runs these
scenarios on every push (`.github/workflows/live.yml`).

```sh
cd examples/etcd
docker compose up -d --build

# See exactly what will be injected, without touching anything:
strife plan --config strife.json

# Run the key-value linearizability test (2 minutes) and write reports to ./strife-out/
strife run --config strife.json

# Run list-append transactions checked at strict serializability
strife run --config strife-append.json

docker compose down -v
```

`latency` and `loss` need the `sch_netem` kernel module on the Docker host
(`sudo modprobe sch_netem`). Without it Strife reports the failed fault
commands and exits non-zero; drop those faults with
`strife run --config strife.json --faults partition,kill,pause`.

## Configs in this directory

| Config / File | Model & Transport | Faults | Exercised by CI (`live.yml`) |
|---|---|---|---|
| `strife.json` | `kv` over `docker` | `partition`, `kill`, `pause`, `latency`, `loss` | `etcd` job (linearizable) |
| `strife-append.json` | `append` (`strict-serializable`) over `docker` | `partition`, `kill`, `pause`, `latency`, `loss` | `etcd` job (strict serializable) |
| `strife-agent.json` + `docker-compose.agent.yml` | `kv` over `agent` (`strife-agent` port 9292) | `partition`, `kill`, `pause` | `agent` job + `strife bench inject` |
| `strife-cgroup.json` | `kv` over `docker` (`cgroup: auto`, `io_device: auto`, `sudo: true`) | `freeze`, `io-stall` | `cgroup` job |

## Using `strife-agent` (`docker-compose.agent.yml`)

To run the low-latency daemon inside each etcd container instead of `docker exec`:

```sh
go build -o bin/strife ./cmd/strife
CGO_ENABLED=0 go build -o bin/strife-agent ./cmd/strife-agent
export STRIFE_AGENT_TOKEN=$(openssl rand -hex 16)

cd examples/etcd
docker compose -f docker-compose.yml -f docker-compose.agent.yml up -d --build
../../bin/strife run --config strife-agent.json
docker compose -f docker-compose.yml -f docker-compose.agent.yml down -v
```

## Adding cgroup faults (`freeze`, `io-stall`)

These act on each container's cgroup v2 from the Docker host, so Strife must
run as root (or with `"sudo": true` in `transport`). `strife-cgroup.json` uses
`"cgroup": "auto"` and `"io_device": "auto"` to discover them at run time. To
inspect and pin them manually:

```sh
for c in strife-etcd1 strife-etcd2 strife-etcd3; do
  echo "$c /sys/fs/cgroup/system.slice/docker-$(docker inspect -f '{{.Id}}' $c).scope"
done
lsblk -no MAJ:MIN "$(df --output=source /var/lib/docker | tail -1)"
```

```json
{ "name": "n1", "ip": "172.30.0.11", "container": "strife-etcd1",
  "cgroup": "/sys/fs/cgroup/system.slice/docker-<id>.scope" }
```

and in `nemesis`: `"faults": ["freeze", "io-stall"], "io_device": "8:0"`.

`clock-skew` is refused with the docker transport because containers share
the host clock; use the `ssh` or `agent` transport against isolated VMs for it.
