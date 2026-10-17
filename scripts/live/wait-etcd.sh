#!/usr/bin/env bash
# Waits until all three members of the example etcd cluster report healthy.
set -euo pipefail
for _ in $(seq 1 60); do
  ok=0
  for ip in 11 12 13; do
    curl -fsS -m 2 "http://172.30.0.$ip:2379/health" >/dev/null && ok=$((ok + 1))
  done
  [ "$ok" = 3 ] && exit 0
  sleep 2
done
docker compose -f examples/etcd/docker-compose.yml logs
exit 1
