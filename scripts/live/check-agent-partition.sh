#!/usr/bin/env bash
# Proves a partition applied by strife-agent really cuts traffic, through
# nf_tables (not the iptables fallback), and that healing restores it.
set -euo pipefail
a=${1:-strife-etcd1}
b=${2:-strife-etcd2}
ip_of() { docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$1"; }
ia=$(ip_of "$a")
ib=$(ip_of "$b")
apply() {
  curl -fsS -m 30 -H "Authorization: Bearer $STRIFE_AGENT_TOKEN" -d "$1" "http://$ia:9292/apply"
}
probe() { docker exec "$a" curl -fsS -m 2 -o /dev/null "http://$ib:2379/version"; }

probe || { echo "$a cannot reach $b before the partition"; exit 1; }
resp=$(apply "{\"action\":\"inject\",\"fault_id\":901,\"kind\":\"partition\",\"target_ips\":[\"$ib\"]}")
echo "inject: $resp"
case "$resp" in *'"method":"filter:nftables"'*) ;; *) echo "partition not applied through nftables"; exit 1 ;; esac
if probe; then
  echo "$a still reaches $b while partitioned"
  apply '{"action":"heal","fault_id":901,"kind":"partition"}' >/dev/null
  exit 1
fi
# Show the rule from the kernel's side, through the host's nft in the
# container's network namespace (the container image has no nft).
pid=$(docker inspect -f '{{.State.Pid}}' "$a")
sudo=""; [ "$(id -u)" = 0 ] || sudo="sudo -n"
if command -v nft >/dev/null && command -v nsenter >/dev/null; then
  $sudo nsenter -t "$pid" -n nft list table ip strife || true
fi
resp=$(apply '{"action":"heal","fault_id":901,"kind":"partition"}')
echo "heal: $resp"
probe || { echo "$a cannot reach $b after healing"; exit 1; }
echo "agent partition verified: traffic cut and restored"
