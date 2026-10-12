#!/bin/sh
# Starts valkey-server in the background. Used at boot and by Strife's kill
# nemesis to restart the server after SIGKILL. With STRIFE_PRIMARY set, the
# node replicates from that host (asynchronous replication).
set -eu
mkdir -p /data
set -- --port 6379 --bind 0.0.0.0 --protected-mode no \
  --dir /data --appendonly yes --appendfsync "${STRIFE_FSYNC:-everysec}" \
  --daemonize yes --logfile /var/log/valkey.log
if [ -n "${STRIFE_PRIMARY:-}" ]; then
  set -- "$@" --replicaof "$STRIFE_PRIMARY" 6379
fi
exec valkey-server "$@"
