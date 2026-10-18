#!/usr/bin/env bash
# Builds strife-agent, loads it into the kind cluster and deploys it as a
# DaemonSet (examples/kubernetes/strife-agent.yaml) with the token in
# $STRIFE_AGENT_TOKEN.
set -euo pipefail
name=${KIND_CLUSTER:-strife}
: "${STRIFE_AGENT_TOKEN:?set STRIFE_AGENT_TOKEN}"
docker build -q -f Dockerfile.agent -t strife-agent:local . >/dev/null
kind load docker-image strife-agent:local --name "$name"
kubectl create namespace strife-system --dry-run=client -o yaml | kubectl apply -f -
kubectl -n strife-system create secret generic strife-agent --from-literal=token="$STRIFE_AGENT_TOKEN" \
  --dry-run=client -o yaml | kubectl apply -f -
sed -e 's#image: ghcr.io/kemalhafizh/strife-agent:latest#image: strife-agent:local#' \
    -e 's#imagePullPolicy: IfNotPresent#imagePullPolicy: Never#' \
    examples/kubernetes/strife-agent.yaml | kubectl apply -f -
kubectl -n strife-system rollout status daemonset/strife-agent --timeout=180s
kubectl -n strife-system logs daemonset/strife-agent | head -5
