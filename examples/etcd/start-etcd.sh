#!/bin/sh
# Starts etcd in the background. Used at boot and by Strife's kill nemesis
# to restart the process after SIGKILL. Data survives restarts.
# The variables are deliberately not called ETCD_*: etcd reads every ETCD_*
# variable as a flag and refuses to start when one shadows a command-line flag.
# STRIFE_PEER_URL overrides the advertised peer URL (Kubernetes uses stable
# per-pod DNS names, because pod IPs are only known once the pod runs).
set -eu
IP=$(hostname -i | awk '{print $1}')
nohup etcd \
  --name "${STRIFE_NODE:?}" \
  --data-dir /var/lib/etcd \
  --listen-peer-urls http://0.0.0.0:2380 \
  --listen-client-urls http://0.0.0.0:2379 \
  --advertise-client-urls "http://${IP}:2379" \
  --initial-advertise-peer-urls "${STRIFE_PEER_URL:-http://${IP}:2380}" \
  --initial-cluster "${STRIFE_CLUSTER:?}" \
  --initial-cluster-state new \
  --initial-cluster-token strife \
  >>/var/log/etcd.log 2>&1 &
