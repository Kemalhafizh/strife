#!/usr/bin/env bash
# Proves that the mechanism behind Strife's io-stall works on this host:
# with io.max throttled to ~0 on the container's cgroup, a synchronous write
# inside the container blocks; healed, it completes at once.
set -euo pipefail
c=${1:?usage: $0 <container>}
pid=$(docker inspect -f '{{.State.Pid}}' "$c")
cg="/sys/fs/cgroup$(sed -n 's/^0:://p' "/proc/$pid/cgroup")"
root=$(docker info -f '{{.DockerRootDir}}')

resolve_majmin() {
  local target=$1 majmin hex d maj min fstype opts opt upper src clean_src uuid dfile
  local -a opt_list
  for _ in 1 2 3 4 5; do
    majmin=$(findmnt -n -o MAJ:MIN --target "$target" 2>/dev/null | tail -1 | tr -d '[:space:]' || true)
    if [ -z "$majmin" ]; then
      hex=$(stat -Lc '%D' "$target" 2>/dev/null || true)
      if [ -n "$hex" ]; then
        d=$((16#$hex))
        maj=$(( ((d >> 8) & 0xfff) | ((d >> 32) & ~0xfff) ))
        min=$(( (d & 0xff) | ((d >> 12) & ~0xff) ))
        majmin="$maj:$min"
      fi
    fi
    case "$majmin" in
      0:*) ;;
      [0-9]*:[0-9]*)
        printf '%s\n' "$majmin"
        return 0
        ;;
    esac
    fstype=$(findmnt -n -o FSTYPE --target "$target" 2>/dev/null | tail -1 | tr -d '[:space:]' || true)
    if [ "$fstype" = "overlay" ]; then
      opts=$(findmnt -n -o OPTIONS --target "$target" 2>/dev/null | tail -1 || true)
      upper=""
      IFS=',' read -r -a opt_list <<< "$opts"
      for opt in "${opt_list[@]}"; do
        case "$opt" in
          upperdir=*) upper=${opt#upperdir=} ;;
        esac
      done
      if [ -n "$upper" ] && [ -e "$upper" ]; then
        target=$upper
        continue
      fi
    fi
    src=$(findmnt -n -o SOURCE --target "$target" 2>/dev/null | tail -1 | tr -d '[:space:]' || true)
    clean_src=${src%%\[*\]}
    if [ -n "$clean_src" ] && [ -b "$clean_src" ]; then
      majmin=$(lsblk -dno MAJ:MIN "$clean_src" 2>/dev/null | head -1 | tr -d '[:space:]' || true)
      if [ -n "$majmin" ]; then
        printf '%s\n' "$majmin"
        return 0
      fi
    fi
    if [ "$fstype" = "btrfs" ]; then
      uuid=$(findmnt -n -o UUID --target "$target" 2>/dev/null | tail -1 | tr -d '[:space:]' || true)
      if [ -n "$uuid" ]; then
        for dfile in /sys/fs/btrfs/"$uuid"/devices/*/dev; do
          if [ -r "$dfile" ]; then
            tr -d '[:space:]' < "$dfile"
            printf '\n'
            return 0
          fi
        done
      fi
    fi
    break
  done
  return 1
}

if ! majmin=$(resolve_majmin "$root") || [ -z "$majmin" ]; then
  echo "check-io-stall: cannot discover block device for $root" >&2
  findmnt --target "$root" >&2 || true
  exit 1
fi
sys="/sys/dev/block/$majmin"
if [ -e "$sys/partition" ]; then
  disk_dir=$(dirname "$(readlink -f "$sys")")
elif [ -e "$sys" ]; then
  disk_dir=$(readlink -f "$sys")
else
  echo "check-io-stall: block device $majmin for $root not found in /sys/dev/block" >&2
  findmnt --target "$root" >&2 || true
  exit 1
fi
dev=$(tr -d '[:space:]' < "$disk_dir/dev")
disk=$(basename "$disk_dir")
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
if [ "$code" != 124 ]; then
  echo "check-io-stall: expected timeout (124), got $code (dev=$dev, cg=$cg)" >&2
  findmnt --target "$root" >&2 || true
  cat "$cg/io.stat" >&2 || true
  exit 1
fi
write 30 # healed: completes again
