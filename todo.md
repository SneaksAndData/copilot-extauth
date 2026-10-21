# TODO: hardening before org-wide rollout

Findings from the security review of the architecture. See [`Architecture.md`](Architecture.md) for the design.

## Planned

### 1. Strip the `Authorization` header after authentication
**Risk:** Only `x-github-token` is removed. The client's `Authorization: Bearer <GitHub token>` stays on the request.
The Agent Router processor overwrites it for backends that have a `BackendSecurityPolicy`. A backend on the same
Gateway *without* one (e.g. a self-hosted model) would receive the user's GitHub token.
**Do:** add `authorization` to `OkHttpResponse.HeadersToRemove` in `Check`. The processor reads the credential from
dynamic metadata, not from the header. Add a unit test, then re-run `e2e/smoke-test.sh`.

### 2. Require `ALLOWED_ORG`
**Risk:** With `ALLOWED_ORG` empty, any GitHub user with a Copilot plan can use the gateway, and Copilot Free makes
that anyone with a GitHub account. The gateway becomes an open relay on your infrastructure.
**Do:** refuse to start unless `ALLOWED_ORG` is set, with an explicit opt-out such as `ALLOW_ANY_USER=true` for local
testing. Support a comma-separated list of orgs. Document that the membership check needs the `read:org` scope.
Update `k8s.yaml` and `e2e/manifests.yaml`.

### 3. Abuse resistance: token format check, bounded cache, singleflight
**Risk:** Every unknown token causes 1–2 calls to `api.github.com`. A flood of random tokens can get the gateway's
egress IP rate-limited by GitHub, which blocks all users. The cache (`map` + janitor) is unbounded. Concurrent
first requests for the same token each call GitHub (thundering herd).
**Do:**
- Reject tokens that don't match the known GitHub formats (`gho_`, `ghu_`, `github_pat_`, `ghp_`) before any network
  call.
- Replace the map with a size-bounded LRU (e.g. `hashicorp/golang-lru/v2/expirable`), with separate limits for
  positive and negative entries.
- Wrap `fetch` in `golang.org/x/sync/singleflight`, keyed by the token hash.
- Unit tests: a malformed token makes no HTTP call, the cache evicts at capacity, and N concurrent identical requests
  make 1 GitHub call.

### 4. Audit logging and user identity metadata
**Risk:** Requests can't be attributed to a user, so there is no audit trail and no per-user limits.
**Do:**
- Resolve the GitHub login from `/user` (already called in passthrough mode) and cache it with the credential.
- Log `login`, decision, mode and cache hit/miss for each decision. Never log tokens.
- Also emit `x-user-login` (and the org) as dynamic metadata, so Agent Router or Envoy Gateway can apply per-user
  token rate limits and add it to access logs.
- Return generic deny messages to clients and keep details in server logs.

### 5. Scope the `SecurityPolicy` to the Copilot route
**Risk:** Targeting the whole Gateway forces a GitHub token on every provider behind it, and widens the exposure
from #1.
**Do:** in `k8s.yaml` and `e2e/manifests.yaml`, target the `HTTPRoute` that Agent Router generates for the Copilot
`AIGatewayRoute` (same name) instead of the `Gateway`. Verify with `config_dump` that only that route has ext_authz
enabled.

## Later / to evaluate

- **Per-IP rate limit before ext_authz.** Use an Envoy Gateway `BackendTrafficPolicy` rate limit on the Copilot route.
  This complements #3.
- **Least-privilege client tokens.** Recommend fine-grained PATs with only the "Copilot Requests" permission instead
  of `gh auth token`, which carries `repo`, `workflow` and `read:org`. Verify that the org-membership check still
  works with them.
- **Encryption in transit.** Use mTLS between Envoy and copilot-extauth (mesh or TLS policy) and an HTTPS listener in
  production.
- **ext_authz timeout.** Check what timeout Envoy Gateway configures. A cache miss calls GitHub and can take a few
  hundred ms.
- **Compliance.** Confirm with GitHub or the enterprise account team before an org-wide rollout. Points to raise:
  the `Copilot-Integration-Id: vscode-chat` impersonation, the undocumented `/copilot_internal/v2/token`, and Copilot
  policies (e.g. content exclusion) that may not be enforced in BYOK mode.
- **Offboarding lag.** Document it, or shorten the passthrough TTL: up to 5 min in passthrough, and an issued Copilot
  token in exchange mode stays valid until it expires (~30 min).
- **Scaling.** Run 2+ replicas with an HPA and a PodDisruptionBudget. The cache is per replica, which is acceptable.
