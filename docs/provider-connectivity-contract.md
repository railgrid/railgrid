# Provider connectivity contract — how providers connect to the platform

This doc pins down the two contracts every provider should follow for *how it
reaches data*: the **UI data path** (contract 1) and the **API access path**
(contract 2). It explains the hub plumbing that enforces them, how the portal
authenticates, which providers conform today, and where the deliberate
exceptions are.

It is the companion to [`providers.md`](./providers.md) (the provider plane
overview), [`provider-scoping.md`](./provider-scoping.md) (Global/Org/Personal
scoping), and [`security.md`](./security.md) (auth setup).

---

## Restore-from-reboot summary

- There are **two** legitimate data paths, not one. UI data flows through the
  hub's **GraphQL gateway** (contract 1); backend/controller code reaches kcp
  either as a **non-privileged provider ServiceAccount via an
  APIExportEndpointSlice** *or* as the **caller using their forwarded bearer
  token**, scoped to the tenant workspace (contract 2).
- The hub exposes providers through **two different proxies** with **different
  token handling**: the UI proxy forwards **no token**, the backend proxy
  forwards the caller's `Authorization` header **as-is**.
- "Token not known to the provider" means the **provider's backend server**.
  The provider's **in-browser micro-frontend** does receive the raw token (it
  runs in the user's browser and uses it to call the GraphQL gateway directly).
- **Standalone providers** (`code`, `infrastructure`, `kuery`, `app-studio`)
  satisfy contract 2 — they hold no admin client. **Built-in providers**
  (`mcp`, `kubernetesedges`, `serveredges`) run inside the hub process and use
  the hub's **admin** kcp config, so they violate contract 2 by construction.
- `kuery` and `app-studio` drive their UI through their own **REST** backends
  rather than GraphQL, so the bearer token reaches their backend — a contract-1
  divergence (defensible: kuery is SQL-backed, app-studio streams chat).

---

## The two contracts

**Contract 1 — UI data path.** The provider's UI micro-frontend reads and
writes data through the hub's central **GraphQL gateway** (`/graphql/{cluster}`),
which executes scoped to the caller's workspace. The provider's **backend
server** is not on the UI data path and does not receive the user's bearer
token for UI purposes.

**Contract 2 — API access path.** The provider's backend reaches the kube/kcp
API **without any admin/root client**. It uses one of two scoped mechanisms:

- **(2a) Controller / sync** — a non-privileged ServiceAccount minted in the
  provider's own workspace (`root:kedge:providers:{name}`), driving a
  multicluster manager off the provider's **APIExportEndpointSlice** virtual
  workspace, bounded by the APIExport's `tenantScoped` permission claims.
- **(2b) Per-request** — the provider drops its own credential and acts **as
  the caller**, using the bearer token forwarded by the hub, scoped to the
  `X-Kedge-Tenant` workspace path.

Both 2a and 2b are admin-free. New providers should pick one (or use 2a for
controllers and 2b for request-driven endpoints, like `code` and
`infrastructure` do) and never construct a kcp-admin / root client.

---

## The hub plumbing (and what it does with the token)

Two proxies back every provider, defined in
[`pkg/hub/providers/proxy.go`](../pkg/hub/providers/proxy.go):

| Proxy | Path | Token handling |
|-------|------|----------------|
| **UI proxy** (`NewUIProxy`, `proxy.go:52`) | `/ui/providers/{name}/*` | Static assets only. Injects `X-Kedge-Base-Path`. **No token forwarded.** First-party providers are served from an embedded FS (`LocalUIAssets`). |
| **Backend proxy** (`NewBackendProxy`, `proxy.go:90`) | `/services/providers/{name}/*` | **Forwards the caller's `Authorization` header as-is**, and additionally injects `X-Kedge-User` + `X-Kedge-Tenant` resolved from the token. Inbound `X-Kedge-*` headers are **always stripped** first (anti-spoofing, `proxy.go:114`). |

The identity injected by the backend proxy is resolved by the
**TenantResolver** ([`pkg/hub/provider_tenant_resolver.go`](../pkg/hub/provider_tenant_resolver.go),
`resolve` at `:104`): caller token → `User` CR → `Organization` →
`Status.WorkspacePath`. It honors the sidebar's `X-Kedge-Org` /
`X-Kedge-Workspace` selection (validated against the user's
`UserMembershipIndex`) and falls back to the user's personal org. Failures are
best-effort: anonymous `/healthz` probes still pass through with no identity
headers, they do not 401.

The **GraphQL gateway** ([`pkg/hub/graphql.go`](../pkg/hub/graphql.go),
`:185`) is the hub-side surface that contract 1 targets. It extracts the bearer
token from the request, puts it in the request context, rewrites
`/graphql/{rest}` → `/clusters/{rest}`, and the gateway builds a per-request
kcp client **authenticated as the caller** — so resolvers run with the user's
own RBAC. The provider's backend server is never in this loop.

> The key consequence: the only way the user's token reaches a provider's
> **backend** is via the backend proxy (`/services/providers/{name}/*`). A
> provider that does all UI data through GraphQL keeps its backend off the
> token path entirely.

---

## How the portal authenticates

```
User opens portal
  → LoginPage: "Sign in with SSO" (OIDC/Dex) or paste a static token
  → OIDC: GET /auth/authorize (PKCE verifier in sessionStorage)
        → Dex → GET /auth/callback?code=…
        → hub exchanges code (PKCE), verifies ID token, seeds the User CR
        → hub returns a LoginResponse (idToken, refreshToken, expiresAt, …)
  → Static: POST /auth/token-login with Authorization: Bearer <token>
        → hub constant-time-compares against configured tokens, seeds User CR
  → portal stores it in localStorage["kedge-auth"]
        { idToken, refreshToken, expiresAt, issuerUrl, clientId,
          email, userId, clusterName }
```

Anchors: portal `portal/src/pages/LoginPage.vue`,
`portal/src/auth/token.ts` (storage + offline OIDC refresh),
`portal/src/stores/auth.ts`; hub `pkg/server/auth/handler.go` (OIDC
authorize/callback + `seedUser`), `pkg/server/proxy/proxy.go` (`token-login`,
bearer dispatch at `:248`).

**Attaching the token to data requests.** The portal's GraphQL client
(`portal/src/graphql/client.ts`, `portal/src/composables/useGraphQL.ts`)
injects `Authorization: Bearer <token>` on every operation and routes to
`/graphql/{clusterName}` (the cluster name is parsed from the user's kubeconfig
at login). A 401/403 dispatches a `SESSION_EXPIRED` event → logout.

**Hub-side verification** (`pkg/server/proxy/proxy.go:248`) dispatches by token
shape:

| Token type | Source | Verification |
|------------|--------|--------------|
| OIDC ID token | Dex / external IdP | signature against the IdP JWKS, then `sub` → `User` CR |
| Static bearer token | hub `--static-auth-tokens` | constant-time compare (dev / air-gapped) |
| kcp ServiceAccount token | kcp-minted | signature verified by kcp (provider/agent/inter-service) |

**Passing context to provider micro-frontends.** `ProviderFrame.vue`
(`portal/src/pages/ProviderFrame.vue:151`) sets a **`kedgeContext` property on
the provider's custom element** (not a postMessage handshake — that part of
older docs is stale):

```js
el.kedgeContext = {
  subPath, basePath,            // routing
  token: auth.token,            // <-- the RAW bearer token
  user: auth.user,              // { email, userId }
  tenant: auth.clusterName,     // kcp logical cluster
  orgUUID, workspaceUUID,       // sidebar selection
  theme,                        // light | dark | system
}
```

It re-pushes on theme change, token refresh, and workspace switch. The provider
bundle hydrates a local auth store from it (e.g.
`providers/mcp/portal/src/auth-adapter.ts`) and builds its **own** GraphQL
client against `/graphql/{clusterName}` with the same `Bearer` pattern.

> So the raw token **does** live in the provider's micro-frontend JS — but that
> code runs in the user's browser, same origin, and uses the token only to call
> the hub gateway. Contract 1's "token not known to the provider" is about the
> provider's **server**, which only sees the token if the UI calls
> `/services/providers/{name}/*`.

---

## Contract 1 conformance — UI via GraphQL

| Provider | UI data path | Verdict |
|----------|--------------|---------|
| `code` | GraphQL gateway for all CRUD; one backend probe (`/services/providers/code/oauth/github/config`) | ✅ Conforms |
| `infrastructure` | GraphQL gateway only; backend serves no template/instance REST | ✅ Conforms |
| `mcp` / `kubernetesedges` / `serveredges` | `useGraphQLQuery` / `graphqlMutate` against the gateway | ✅ Conforms |
| `kuery` | **REST** to `/services/providers/kuery/api/{edges,query}` — token reaches backend | ❌ Diverges |
| `app-studio` | **REST** to `/services/providers/app-studio/api/projects/*` — token reaches backend | ❌ Diverges |

`kuery` is backed by its own SQL store (it syncs edge data into SQLite and
answers queries from there) and `app-studio` streams chat/messages — neither
maps cleanly onto GraphQL CRUD, so their REST backends are defensible. But they
*do* hand the user's bearer token to a provider process, which is the contract-1
departure to keep in mind.

MCP endpoints (`/services/.../mcp`) are an **AI-agent** surface, not the human
UI; `code` and `infrastructure` keep a GraphQL-clean UI even though their MCP
servers receive the token by design.

---

## Contract 2 conformance — scoped API access, no admin client

| Provider | Mechanism | Verdict |
|----------|-----------|---------|
| `code` | (2a) `apiexport.New(...)` multicluster mgr off the endpointslice + (2b) caller-token tenant client for MCP | ✅ Conforms |
| `infrastructure` | init/serve split: admin kubeconfig only in one-shot `init`, then a **minted SA** + endpointslice for serve; (2b) caller-token factory for MCP; KRO writes go to a separate runtime cluster | ✅ Conforms |
| `kuery` | (2a) minted provider SA + APIExportEndpointSlice for edge discovery; `[get,list,watch] edges`, `tenantScoped` | ✅ Conforms |
| `app-studio` | (2b) clears the provider credential, builds the tenant client from the forwarded caller token; `tenantScoped` secret claims | ✅ Conforms |
| `mcp` | **hub admin config** (`deps.KCPConfig`) to read MCPServer + list edges; per-MCPServer SA bound to **`cluster-admin`** | ❌ Violates by design |
| `kubernetesedges` | edge list via hub admin config; edge data path forwards the caller token | ❌ Violates by design |
| `serveredges` | edge list via hub admin config; SSH scoped to caller identity | ❌ Violates by design |

### How the hub provisions a conforming (2a) provider

[`pkg/hub/providers/provision.go`](../pkg/hub/providers/provision.go):

1. `EnsureProviderWorkspace` creates `root:kedge:providers:{name}`.
2. `EnsureProviderSA` creates `system:serviceaccount:default:provider`, granted
   cluster-admin **only inside its own workspace** — its single privilege.
3. `MintProviderKubeconfig` mints a long-lived SA-token kubeconfig pointing at
   `{hub}/clusters/root:kedge:providers:{name}`, delivered to the provider as
   the `kedge-provider-kubeconfig` Secret.
4. `ApplyAPIExport` registers the provider's permission claims; `ApplyBindGrant`
   lets `system:authenticated` tenants bind the export.

The provider then builds a multicluster manager off its APIExportEndpointSlice
(e.g. `providers/code/controller_manager.go`,
`providers/infrastructure/install/endpointslice.go`). The (2b) per-request
factory (`providers/*/tenant/client.go`) strips the provider's own client cert,
keeps only the CA, and authenticates with the **caller's** bearer token against
`{host}/clusters/{tenantPath}`.

---

## Known divergences (and why)

1. **Built-ins are privileged by construction.** `mcp`, `kubernetesedges`, and
   `serveredges` compile into the hub binary and read kcp through the hub's
   admin `rest.Config` (`providers/mcp/virtual/builder.go:86` uses
   `deps.KCPConfig`). The per-MCPServer ServiceAccount is bound to
   `cluster-admin` with an explicit `TODO(scope-down)`
   (`providers/mcp/controllers/controller.go:265`). They cannot satisfy
   contract 2 while running in-process with the hub. The fix is either to give
   them their own provisioned workspace + scoped SA, or to gate every
   cross-tenant read behind the caller's identity (a SAR or a caller-scoped
   dynamic client built from the request token, not `deps.KCPConfig`).

2. **`kuery` / `app-studio` UI is REST, not GraphQL.** Contract-2-clean, but
   the token reaches their backend. Acceptable given their data models; flagged
   so it's a conscious choice, not drift.

---

## Checklist for a new provider

- [ ] UI reads/writes go through `/graphql/{cluster}` (contract 1). Only add a
      `/services/providers/{name}/*` backend for things GraphQL genuinely can't
      do (streaming, a non-kcp store, an OAuth callback) — and know the token
      reaches it when you do.
- [ ] No kcp-admin / root client anywhere in the provider (contract 2).
- [ ] Controllers run as the **minted provider SA** off the
      **APIExportEndpointSlice** (2a); declare `tenantScoped` permission claims
      for exactly the resources/verbs you need.
- [ ] Request-driven endpoints act as the **caller** via the forwarded token
      (2b): build the tenant client from `Authorization` + `X-Kedge-Tenant`,
      drop the provider's own credential.
- [ ] Never trust inbound `X-Kedge-*` headers in the provider — the backend
      proxy strips and re-injects them; treat them as hub-asserted only.

---

## Code anchors

| Concern | Anchor |
|---------|--------|
| UI proxy (no token) | `pkg/hub/providers/proxy.go:52` |
| Backend proxy (forwards token + injects identity) | `pkg/hub/providers/proxy.go:90` |
| Tenant resolution (token → workspace path) | `pkg/hub/provider_tenant_resolver.go:104` |
| GraphQL gateway (caller-scoped) | `pkg/hub/graphql.go:185` |
| Provider provisioning (workspace, SA, kubeconfig) | `pkg/hub/providers/provision.go` |
| Portal login / token storage | `portal/src/pages/LoginPage.vue`, `portal/src/auth/token.ts` |
| Portal GraphQL client | `portal/src/graphql/client.ts`, `portal/src/composables/useGraphQL.ts` |
| `kedgeContext` push to micro-frontend | `portal/src/pages/ProviderFrame.vue:151` |
| Hub bearer dispatch / verification | `pkg/server/proxy/proxy.go:248` |
| (2a) endpointslice multicluster mgr | `providers/code/controller_manager.go` |
| (2b) caller-token tenant factory | `providers/*/tenant/client.go` |
| Built-in admin-config read | `providers/mcp/virtual/builder.go:86` |
| Built-in cluster-admin SA (TODO scope-down) | `providers/mcp/controllers/controller.go:265` |
