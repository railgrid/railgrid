# Edge agent credentials — enrolment, rotation, revocation

How an edge agent gets a credential, keeps it, and loses it. Companion to
[provider-connectivity-contract.md](./provider-connectivity-contract.md)
§"Scoped identities" and
[roadmap/provider-contract-remediation.md](./roadmap/provider-contract-remediation.md)
§5.7.

## What changed, and why

An agent used to hold a **kubeconfig with a legacy
`kubernetes.io/service-account-token`**. The edges provider minted it: a
ServiceAccount, a shared ClusterRole, a per-edge binding and a token Secret,
all written into the tenant workspace with the provider's own claimed
credentials. The token never expired, nothing collected it, and the rules were
create-if-absent — so a grant could only ever widen. That is review finding
**M7**, and the agent additionally held `get/create` on Namespaces and
`get/create/update` on **Secrets** so it could write its own SSH credentials:
the broadest grant an edge agent had, on a credential with no end date.

A provider does not mint identities. It asks the hub. What an agent holds now
is a **hub-minted scoped identity**: TokenRequest-minted, TTL'd, recorded in a
workspace no tenant can reach, its rules reconciled rather than merely created,
and garbage-collected when the edge stops existing.

## The enrolment bundle

`X-Railgrid-Agent-Kubeconfig` is gone. On a **join-token connect** the provider
mints the identity and returns `X-Railgrid-Agent-Credential` on the WebSocket
upgrade response: base64 of

```json
{
  "token": "…", "tokenType": "Bearer", "expiresAt": "2026-09-20T18:04:11Z",
  "hubURL": "https://hub.example.com", "caCertData": "…",
  "provider": "edges", "clusterID": "2hx82dl9ncmepp5l",
  "resource": "linuxservers", "name": "edge-1",
  "refreshPath": "/services/providers/edges/dataplane/clusters/2hx82dl9ncmepp5l/linuxservers/edge-1/agent-token",
  "sshCredentialsPath": "/services/providers/edges/dataplane/clusters/2hx82dl9ncmepp5l/linuxservers/edge-1/ssh-credentials"
}
```

The **hub URL and the addressing tuple travel with the token**, and the routes
are rendered by the provider that serves them. Nothing about where this
provider lives is compiled into the agent, so a provider that moves — a renamed
root, a self-hosted copy under another name — is followed without rebuilding
agents (contract 3, rule 5).

The join token is cleared **only if the bundle was actually delivered**. A mint
that fails leaves it valid so the next attempt can enrol, rather than stranding
the edge.

## Rotation

`POST {hubURL}{refreshPath}` with the credential in hand. It is an ordinary
declared data-plane verb, `{resource}/agent-token`, gated like every other:

1. **Gate 1** — a real GET of the agent's own edge, as the agent.
2. **Gate 2** — an SSAR for `create` on `{resource}/agent-token`, name-scoped.

Exactly one identity in the workspace satisfies both: this edge's. The provider
then re-mints through `identityclient` with its **own** credential, because the
hub identity service authenticates *providers* and an agent is not one. The
provider is standing in for it, behind the agent's own proof of identity.

`Ensure` is idempotent on the owner tuple, so a refresh reuses the same hub-side
ServiceAccount and only the token is new — a rotation, not an accumulation.

The agent refreshes **on the reconnect path at 80% of the TTL**, not from a
timer: an agent that has stopped reconnecting has stopped needing a credential,
and its identity should be allowed to lapse. A failed refresh is not fatal —
there is a fifth of the TTL left to retry.

## The TTL is also a power-off budget

The refresh is authorized *by the credential being refreshed*. An agent whose
token has fully expired therefore has nothing left to authenticate with.

The provider asks for the hub's **24-hour cap**, deliberately: a shorter TTL is
not more secure here, it is only more brittle. An agent powered off for longer
than the remaining TTL must be **re-enrolled** — set
`edges.railgrid.ai/regenerate-join-token` on the edge and restart the agent with
the new join token.

This is a real operational consequence and it is the intended trade: the
alternative is the credential that never expires, which is what this replaced.

## SSH credentials

A LinuxServer agent no longer writes the tenant Secret. It POSTs to the declared,
gated verb `linuxservers/{name}/ssh-credentials`:

```json
{ "username": "ubuntu", "password": "…", "privateKey": "…", "hostKey": "…" }
```

Both gates run as the agent; the **provider** then writes the `railgrid-system`
namespace and the `<edge>-ssh-credentials` Secret and patches
`status.sshCredentials`. **Nothing in the request names a destination** — the
namespace, the Secret name and the status field are all derived from the edge
the caller just proved it is, so an agent that lies about its credentials only
ever lies about its own.

That is why the agent holds no core-group access at all any more, and why the
provider keeps its `secrets`/`namespaces` claims: the write did not disappear,
it moved to the side of the boundary that can be held accountable for it.

## Revocation

- **On delete** — the RBAC reconciler holds the finalizer
  `edges.railgrid.ai/scoped-identity` and calls `Release` before letting the
  edge go, so the ServiceAccount and every outstanding token die immediately.
- **The hub's sweep is the backstop** — it re-probes each record at its own TTL
  and collects an orphan within one TTL of the last refresh.
- **Recreating an edge under the same name** gets a *different* identity: the
  hub hashes the owner tuple including the **UID** into the ServiceAccount name.

## What the agent's identity may do

All clause A — every rule on `edges.railgrid.ai`, the group this provider
exports, so the hub admits the request whole:

| Rule | Scope |
|---|---|
| `get` on `{resource}` | this edge only (gate 1) |
| `create` on `{resource}/{agent-token,k8s,mcp,proxy,ssh,ssh-credentials}` | this edge only (gate 2) |
| `get,update,patch` on `{resource}/status` | this edge only |
| `list,watch` on `{resource}` | kind-wide — RBAC cannot name-scope a collection request |
| `get,list,watch,update,patch` on `placements(/status)` | workload plane |
| `get,list,watch` on `workloads(/status)`, `addons` | read-only |
| `get,update,patch` on `addons/status` | an agent reports on an add-on, never creates one |

No core group. No wildcards. No unnamed write outside the two collection reads.
