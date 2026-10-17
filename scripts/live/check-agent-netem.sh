#!/usr/bin/env bash
# Proves strife-agent programs netem through netlink with the parameters it
# was asked for, by reading them back from the kernel with tc. (A wrong
# struct layout once turned every loss fault into a queue limit of zero.)
set -euo pipefail
c=${1:-strife-etcd1}
ip=$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$c")
apply() {
  curl -fsS -m 30 -H "Authorization: Bearer $STRIFE_AGENT_TOKEN" -d "$1" "http://$ip:9292/apply"
  echo
}
apply '{"action":"inject","fault_id":900,"kind":"latency","delay":200000000,"jitter":20000000,"loss":0.3}'
shown=$(docker exec "$c" tc qdisc show dev eth0)
echo "kernel: $shown"
apply '{"action":"heal","fault_id":900,"kind":"latency"}'
for want in "limit 1000" "delay 200ms" "20ms" "loss 30%"; do
  case "$shown" in
  *"$want"*) ;;
  *)
    echo "netem lacks '$want'"
    exit 1
    ;;
  esac
done
if docker exec "$c" tc qdisc show dev eth0 | grep -q netem; then
  echo "netem still installed after heal"
  exit 1
fi
echo "agent netem parameters verified"
