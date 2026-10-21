#!/usr/bin/env bash
# Exposes the Agent Router gateway on http://localhost:$LOCAL_PORT.
set -euo pipefail
cd "$(dirname "$0")"; source ./env.sh
svc=$(gw_svc); [ -n "$svc" ] || { echo "gateway service not found; run ./setup.sh"; exit 1; }
echo "Agent Router -> http://localhost:$LOCAL_PORT  (Ctrl+C to stop)"
exec kubectl --context "$KCTX" -n envoy-gateway-system port-forward "svc/$svc" "$LOCAL_PORT:80"
