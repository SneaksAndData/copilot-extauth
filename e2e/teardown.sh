#!/usr/bin/env bash
# Removes the e2e stack. Pass --all to also uninstall Agent Router and Envoy Gateway.
set -uo pipefail
cd "$(dirname "$0")"; source ./env.sh
k delete -f manifests.yaml --ignore-not-found
if [ "${1:-}" = "--all" ]; then
  h uninstall aieg aieg-crd -n envoy-ai-gateway-system
  h uninstall eg -n envoy-gateway-system
  k delete ns envoy-ai-gateway-system envoy-gateway-system --ignore-not-found
fi
