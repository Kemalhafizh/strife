# Example: 3-node etcd under Strife

Requirements: Linux docker host (container IPs must be routable from the host),
Docker Compose v2, and Strife built from the repository root.

```sh
cd examples/etcd
docker compose up -d --build

# See exactly what will be injected, without touching anything:
strife plan --config strife.json

# Run the test (2 minutes) and write the audit report to ./strife-out/
strife run --config strife.json

docker compose down -v
```

## Adding cgroup faults (`freeze`, `io-stall`)

These act on the container's cgroup from the docker host, so Strife must run
as root on that host. Look up each container's cgroup directory and add it to
the node, plus the block device holding docker's data for `io-stall`:

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

and in `nemesis`: `"faults": [..., "freeze", "io-stall"], "io_device": "8:0"`.

`clock-skew` is refused with the docker transport because containers share
the host clock; use the `ssh` transport against VMs for it.
