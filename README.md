# copilot-extauth

Envoy ext_authz service so each user reaches GitHub Copilot through Agent Router with **their own** GitHub identity.

```
client --(GitHub token)--> Envoy --ext_authz--> copilot-extauth --exchange--> api.github.com
                             |   <-- dynamic metadata x-aigw-api-key = Copilot token
                             +--> AI Gateway processor (credentialOverride) --Bearer Copilot token--> api.githubcopilot.com
```

Clients send their GitHub token as `x-github-token` or `Authorization: Bearer <token>`. In the default `auto` mode the
service tries to exchange it at `/copilot_internal/v2/token` for a short-lived Copilot token. Tokens from other OAuth
apps (e.g. `gh auth token`) are refused there, so the validated GitHub token itself is forwarded instead;
`api.githubcopilot.com` accepts it directly. Results are cached per token, and the credential is passed as ext_authz
dynamic metadata, never as a client-controllable header.

| Env | Default | Purpose |
|---|---|---|
| `TOKEN_MODE` | `auto` | `exchange` = Copilot token only; `passthrough` = forward the GitHub token (validated via `/user`); `auto` = exchange, else passthrough |
| `ALLOWED_ORG` | _(empty)_ | Require active org membership (token needs `read:org`) |
| `GITHUB_API` | `https://api.github.com` | GHE.com: `https://api.<tenant>.ghe.com` |
| `GITHUB_TOKEN_HEADER` | `x-github-token` | Dedicated header for the token |
| `COPILOT_INTEGRATION_ID` / `EDITOR_VERSION` | `vscode-chat` / `vscode/1.99.0` | Headers Copilot requires |
| `PORT` | `9001` | gRPC port |

## How to use

### 1. Deploy

```bash
docker build -t IMAGE . && docker push IMAGE
# edit the placeholders in k8s.yaml (NAMESPACE, MY-GATEWAY, MY-GATEWAY-CONFIG, COPILOT-BACKEND, IMAGE)
kubectl apply -f k8s.yaml
```

### 2. Envoy configuration

No raw Envoy configuration is needed. Envoy Gateway generates the `envoy.filters.http.ext_authz` filter from Kubernetes
resources and pushes it to Envoy over xDS. What is required (all included in `k8s.yaml`):

1. **`SecurityPolicy` with `extAuth`.** This makes Envoy Gateway insert the ext_authz filter. `targetRefs` (Gateway or
   HTTPRoute) decides which traffic is checked.
2. **A gRPC Service for copilot-extauth.** Set `appProtocol: grpc` on the port so Envoy uses HTTP/2 to the auth server.
3. **A `ReferenceGrant`, only if copilot-extauth runs in a different namespace from the `SecurityPolicy`.** Without
   it, Envoy Gateway rejects the cross-namespace `backendRef`.
4. **`GatewayConfig.extProc.metadataForwardingNamespaces: [envoy.filters.http.ext_authz]`.** This is not needed for
   ext_authz itself. It lets the credential in the ext_authz dynamic metadata reach the Agent Router processor.
   Without it, requests fail with 401, or fall back to the static Secret if `fallbackToConfigured: true`.

Other gateway requirements: Agent Router **v1.2.0+**, because v1.1.0 never forwards the metadata. The Copilot
`AIServiceBackend` needs schema prefix `/`. A `ClientTrafficPolicy` must raise `bufferLimit`, because agent prompts
are 60 KB+.

### 3. Verify the filter is installed

```bash
kubectl -n envoy-gateway-system port-forward <envoy-pod> 19000
curl -s localhost:19000/config_dump | grep -o 'envoy.filters.http.ext_authz[^"]*' | sort -u
```

### 4. Test

```bash
curl https://GATEWAY/v1/chat/completions -H "Authorization: Bearer $(gh auth token)" \
  -H 'content-type: application/json' -d '{"model":"gpt-4.1","messages":[{"role":"user","content":"hi"}]}'
```

Copilot CLI: set `COPILOT_PROVIDER_BASE_URL=https://GATEWAY/v1`, `COPILOT_PROVIDER_API_KEY_COMMAND="gh auth token"`
and `COPILOT_MODEL=claude-sonnet-5`.

## More

Design and Envoy integration details: [`Architecture.md`](Architecture.md).

Local end-to-end test with kind and Copilot CLI: see [`e2e/README.md`](e2e/README.md).

Note: `/copilot_internal/v2/token` is undocumented; `auto` mode degrades to passthrough if it changes.
