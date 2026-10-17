#!/usr/bin/env bash
# Makes sure the io controller is enabled for each container's cgroup (v2),
# so io.max (Strife's io-stall) has an effect, and prints what freeze and
# io-stall will act on.
set -euo pipefail
for c in "$@"; do
  pid=$(docker inspect -f '{{.State.Pid}}' "$c")
  rel=$(sed -n 's/^0:://p' "/proc/$pid/cgroup")
  cg="/sys/fs/cgroup$rel"
  if ! grep -qw io "$cg/cgroup.controllers"; then
    echo "+io" | sudo tee "$(dirname "$cg")/cgroup.subtree_control" >/dev/null
  fi
  echo "$c: $cg controllers: $(cat "$cg/cgroup.controllers")"
  grep -qw io "$cg/cgroup.controllers"
  test -f "$cg/cgroup.freeze"
done
