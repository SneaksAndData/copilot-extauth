# copilot-extauth
This is an Envoy extauth filter that enables testing AgentRouter with GitHub Copilot backend.

# Local e2e: Copilot CLI → Agent Router (kind) → GitHub Copilot

Each request is authenticated with **your own** GitHub token (`gh auth token`). There is no shared Copilot secret: the
static key in the `BackendSecurityPolicy` is a placeholder, and `fallbackToConfigured: false` makes the gateway
reject a request without a per-user credential.

```
copilot CLI (BYOK) --Bearer gh token--> localhost:8080 (port-forward) --> Envoy
   Envoy --ext_authz--> copilot-extauth --validates/exchanges--> api.github.com
   Envoy --AI Gateway ext_proc, credentialOverride--> api.githubcopilot.com/chat/completions
```

## Prerequisites

`kind` cluster named `kind` (context `kind-kind`), `docker`, `helm`, `kubectl`, `gh` (logged in, Copilot seat), `copilot`.
All scripts target `kind-kind` explicitly (`KIND_CLUSTER=<name>` to change), never your current kube context.

## Run

```bash
./setup.sh          # Envoy Gateway v1.9.2 + Agent Router v1.2.0 + copilot-extauth + Copilot route (idempotent)
./port-forward.sh   # keep running in a separate terminal: http://localhost:8080
./smoke-test.sh     # curl checks: 401/403 without a valid token, 200 plain/stream/large
./copilot.sh        # interactive Copilot CLI routed through the gateway
./copilot.sh -p "Reply with pong"            # one-shot
MODEL=claude-opus-5.5 ./copilot.sh           # other model
./teardown.sh [--all]                        # --all also uninstalls Agent Router + Envoy Gateway
```

`copilot.sh` sets the BYOK variables. Use them yourself to point any Copilot CLI session at the gateway:

```bash
export COPILOT_PROVIDER_BASE_URL=http://localhost:8080/v1
export COPILOT_PROVIDER_API_KEY_COMMAND="gh auth token"
export COPILOT_MODEL=claude-sonnet-5
copilot
```

After code changes, re-run `./setup.sh`; it rebuilds the image, loads it into kind and restarts the deployment.

## Gotchas found while building this

| Symptom | Cause | Fix (already applied) |
|---|---|---|
| `Programmed=False AddressNotAssigned` | kind has no LoadBalancer | wait on `Accepted`, use port-forward |
| 404 from Copilot | default OpenAI prefix `v1` → `/v1/chat/completions` | `schema.prefix: "/"` |
| 413 `request_payload_too_large` ("prompt too long" in CLI) | Envoy 32KiB default buffer; CLI prompts are ~60KB+ | `ClientTrafficPolicy` `bufferLimit: 50Mi` |
| 400 `invalid_reasoning_effort` | CLI sends `reasoning_effort`; `gpt-4.1` rejects it | use a reasoning model, e.g. `claude-sonnet-5` |
| GPT-5.x models fail | they are `/responses`-only on Copilot | use Claude models over `/chat/completions` |
| 403 on `/copilot_internal/v2/token` | `gh` OAuth tokens can't be exchanged | `TOKEN_MODE=auto` falls back to passthrough |

Debug: `kubectl --context kind-kind logs -n copilot-e2e deploy/copilot-extauth` and
`kubectl --context kind-kind logs -n envoy-gateway-system -l gateway.envoyproxy.io/owning-gateway-name=copilot-e2e -c envoy`.
