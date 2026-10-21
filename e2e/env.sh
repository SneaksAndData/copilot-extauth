# Shared settings. Always target the kind cluster explicitly, never the current context.
export KIND_CLUSTER="${KIND_CLUSTER:-kind}"
export KCTX="${KCTX:-kind-${KIND_CLUSTER}}"
export EG_VERSION="${EG_VERSION:-v1.9.2}"
export AIGW_VERSION="${AIGW_VERSION:-v1.2.0}"
export LOCAL_PORT="${LOCAL_PORT:-8080}"
export NS=copilot-e2e
k() { kubectl --context "$KCTX" "$@"; }
h() { helm --kube-context "$KCTX" "$@"; }
gw_svc() { k get svc -n envoy-gateway-system \
  -l gateway.envoyproxy.io/owning-gateway-name=copilot-e2e,gateway.envoyproxy.io/owning-gateway-namespace=$NS \
  -o jsonpath='{.items[0].metadata.name}'; }
