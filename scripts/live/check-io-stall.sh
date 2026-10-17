#!/usr/bin/env bash
# Proves that the mechanism behind Strife's io-stall works on this host:
# with io.max throttled to ~0 on the container's cgroup, a synchronous write
# inside the container blocks; healed, it completes at once.
set -euo pipefail
c=$1
pid=$(docker inspect -f '{{.State.Pid}}' "$c")
cg="/sys/fs/cgroup$(sed -n 's/^0:://p' "/proc/$pid/cgroup")"
src=$(findmnt -n -o SOURCE --target "$(docker info -f '{{.DockerRootDir}}')")
disk=$(lsblk -no PKNAME "$src" | head -1)
[ -n "$disk" ] || disk=$(basename "$src")
dev=$(lsblk -dno MAJ:MIN "/dev/$disk" | tr -d ' ')
echo "container $c, cgroup $cg, data disk /dev/$disk ($dev)"
write() { timeout "$1" docker exec "$c" dd if=/dev/zero of=/tmp/strife-io-probe bs=64k count=16 oflag=dsync status=none; }
write 20 # healthy: completes
echo "$dev rbps=1 wbps=1 riops=1 wiops=1" | sudo tee "$cg/io.max" >/dev/null
set +e
start=$(date +%s)
write 5
code=$?
set -e
echo "$dev rbps=max wbps=max riops=max wiops=max" | sudo tee "$cg/io.max" >/dev/null
echo "stalled write: exit $code after $(($(date +%s) - start))s"
test "$code" = 124 # timed out: the write was stalled
write 30 # healed: completes again
