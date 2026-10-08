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
  "refreshPath": "/clusters/2hx82dl9ncmepp5l/apis/edges.railgrid.ai/v1alpha1/linuxservers/edge-1/agent-token",
  "sshCredentialsPath": "/clusters/2hx82dl9ncmepp5l/apis/edges.railgrid.ai/v1alpha1/linuxservers/edge-1/ssh-credentials"
}
```

Each path is a declared verb on the edge — a kcp custom subresource on the
edges APIExport, reached on the hub's kcp front door like any kube path; kcp
authorizes the agent's own identity for it and forwards to the provider.
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
declared data-plane verb, `{resource}/agent-token`, a kcp custom subresource
gated like every other:

1. **kcp** authorizes the agent's identity for `{resource}/agent-token` on
   this edge's name (RBAC on the coordinate, verbs `*`) and forwards the
   request with the identity stamped.
2. **The gate** runs a `SubjectAccessReview` for `get` on the agent's own edge
   on that identity's behalf, then reads the edge as the provider.

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

## Restarting with both a join token and a saved credential

> Where to look when an agent re-enrols unexpectedly: for a pod, the
> `credential.json` key of its `railgrid-agent-<edge>-kubeconfig` Secret, since
> the file under `~/.railgrid` is gone on every restart; for a host agent, the
> file. The agent logs which of the two it adopted (`source=file` or
> `source=secret`), and logs an unreadable file rather than silently falling
> back to the join token.

The agent persists each issued credential at
`~/.railgrid/agent-<edge>.credential.json` so a restart does not need a new
join token. In Kubernetes mode it also writes the bundle into its own
`railgrid-agent-<edge>-kubeconfig` Secret, under the `credential.json` key,
because a pod's filesystem does not survive a restart — and reads it back from
there whenever the file is absent. A server or macOS agent has a real
filesystem and uses the file alone.

On start it adopts whichever copy it found only when it is for **this** target:
the hub base URL matches `--hub-url`, the cluster ID matches `--cluster` when
one was given, and the credential has not expired. Anything else is logged and
ignored, and the agent enrols with the join token it was started with. This is
what an agent of the same name pointed at a rebuilt hub, or at an edge that was
deleted and recreated, would otherwise trip over: it would present the dead
credential forever.

If the provider still answers a connect with **401** while the agent holds both
a saved credential and a join token, the next attempt presents the other one,
and they alternate until one is accepted. A successful enrolment overwrites the
saved copies. A valid saved credential loses nothing from this: the join token is
cleared on the first successful join, so it is refused and the credential is
tried again on the following attempt.

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

## A managed runner needs no credential

Worth stating, because it used to need two and the machinery for that was
substantial.

An edge that hosts a coding harness (see [edge-harness.md](./edge-harness.md))
supervises a runner and publishes it as a `Service`. Neither half of that
involves a tenant credential any more:

- **The harness credential is gone from the host.** It used to be a tenant
  Secret named by an `Addon`, read by the provider on the agent's behalf through
  a declared `addon-credentials` verb and written to a `0600` file on the
  machine. Now the CALLER sends its own identity with every `runner/v1` start
  and resume, held in memory for the length of one attempt. Nothing on the
  machine authenticates to a model provider, so there is nothing to deliver,
  rotate or revoke here.
- **The runner's own bearer never leaves the host.** The agent generates it and
  injects it into requests arriving for its own loopback runner ports, so the
  published `Service` carries `auth: none` and no Secret is written into the
  tenant workspace at all.

The `addon-credentials` verb, its subresource shell, and the `secrets` half of
the provider's claim that existed to serve it are all deleted. The SSH
credentials path above is unchanged and is now the only credential the agent
sends the provider.

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
| `get` on `{resource}` | this edge only (the gate's visibility review) |
| `*` on `{resource}/{agent-token,k8s,mcp,proxy,ssh,ssh-credentials}` | this edge only (kcp's RBAC on the verb subresource; `*` because kcp maps the HTTP method onto the verb) |
| `get,update,patch` on `{resource}/status` | this edge only |
| `list,watch` on `{resource}` | kind-wide — RBAC cannot name-scope a collection request |
| `get,list,watch,update,patch` on `placements(/status)` | workload plane |
| `get,list,watch` on `workloads(/status)` | read-only |


No core group. No wildcards. No unnamed write outside the two collection reads.
