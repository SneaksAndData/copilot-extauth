#!/usr/bin/env bash
# Installs Envoy Gateway + Agent Router into the kind cluster and deploys the Copilot e2e stack.
set -euo pipefail
cd "$(dirname "$0")"; source ./env.sh
echo ">> context: $KCTX"; k cluster-info >/dev/null

echo ">> Envoy Gateway $EG_VERSION"
h upgrade -i eg oci://docker.io/envoyproxy/gateway-helm --version "$EG_VERSION" \
  -n envoy-gateway-system --create-namespace -f envoy-gateway-values.yaml --wait --timeout 5m

echo ">> Agent Router $AIGW_VERSION"
h upgrade -i aieg-crd oci://docker.io/envoyproxy/ai-gateway-crds-helm --version "$AIGW_VERSION" \
  -n envoy-ai-gateway-system --create-namespace --wait
h upgrade -i aieg oci://docker.io/envoyproxy/ai-gateway-helm --version "$AIGW_VERSION" \
  -n envoy-ai-gateway-system --create-namespace --wait --timeout 5m

echo ">> build + load copilot-extauth"
docker build -q -t copilot-extauth:e2e .. >/dev/null
kind load docker-image copilot-extauth:e2e --name "$KIND_CLUSTER"

echo ">> apply manifests"
k apply -f manifests.yaml
k rollout restart deploy/copilot-extauth -n "$NS" >/dev/null   # pick up a rebuilt image
k rollout status deploy/copilot-extauth -n "$NS" --timeout 2m
# kind has no LoadBalancer, so the Gateway never gets an address (Programmed=False); port-forward instead.
k wait gateway/copilot-e2e -n "$NS" --for=condition=Accepted --timeout 3m
for i in $(seq 1 60); do [ -n "$(gw_svc)" ] && break; sleep 2; done
k wait pods -n envoy-gateway-system -l gateway.envoyproxy.io/owning-gateway-name=copilot-e2e \
  --for=condition=Ready --timeout 3m
echo ">> ready. Next: ./port-forward.sh (keep running), then ./smoke-test.sh and ./copilot.sh"
