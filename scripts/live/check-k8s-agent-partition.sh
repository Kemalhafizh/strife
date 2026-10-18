#!/usr/bin/env bash
# Proves a partition the strife-agent DaemonSet applies to one pod really
# cuts that pod's traffic, through nf_tables in the pod's network namespace,
# and leaves the other pods on the same node alone; then that healing
# restores it.
set -euo pipefail
ns=strife
a=etcd-0 b=etcd-1 c=etcd-2
field() { kubectl -n "$ns" get pod "$1" -o jsonpath="$2"; }
cid=$(field "$a" '{.status.containerStatuses[?(@.name=="etcd")].containerID}')
agent=$(field "$a" '{.status.hostIP}')
ib=$(field "$b" '{.status.podIP}')
apply() {
  curl -fsS -m 30 -H "Authorization: Bearer $STRIFE_AGENT_TOKEN" -d "$1" "http://$agent:9292/apply"
}
probe() { kubectl -n "$ns" exec "$1" -c etcd -- curl -fsS -m 2 -o /dev/null "http://$ib:2379/version"; }
heal() { apply "{\"action\":\"heal\",\"fault_id\":901,\"kind\":\"partition\",\"target\":{\"container_id\":\"$cid\"}}"; }

probe "$a" || { echo "$a cannot reach $b before the partition"; exit 1; }
resp=$(apply "{\"action\":\"inject\",\"fault_id\":901,\"kind\":\"partition\",\"target_ips\":[\"$ib\"],\"target\":{\"container_id\":\"$cid\"}}")
echo "inject: $resp"
case "$resp" in *'"method":"filter:nftables"'*) ;; *) echo "partition not applied through nftables"; heal >/dev/null || true; exit 1 ;; esac
if probe "$a"; then
  echo "$a still reaches $b while partitioned"
  heal >/dev/null
  exit 1
fi
if ! probe "$c"; then
  echo "$c lost $b too: the partition leaked out of $a's network namespace"
  heal >/dev/null
  exit 1
fi
# Refused means an HTTP error carrying the agent's reason, not merely a body
# that happens to hold "500" (a success's nanosecond timestamps can).
out=$(mktemp)
code=$(curl -sS -m 30 -o "$out" -w '%{http_code}' -H "Authorization: Bearer $STRIFE_AGENT_TOKEN" \
  -d '{"action":"inject","fault_id":902,"kind":"partition","target_ips":["10.9.9.9"]}' "http://$agent:9292/apply")
resp=$(cat "$out"); rm -f "$out"
if [ "$code" -ge 400 ] && grep -q 'target container' <<<"$resp"; then
  echo "untargeted fault refused: ok ($code)"
else
  echo "the DaemonSet agent accepted a fault on the node itself: $code $resp"
  heal >/dev/null
  exit 1
fi
echo "heal: $(heal)"
probe "$a" || { echo "$a cannot reach $b after healing"; exit 1; }
echo "DaemonSet agent partition verified: only $a cut off from $b, then restored"
