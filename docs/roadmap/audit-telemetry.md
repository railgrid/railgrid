# Audit telemetry: kcp audit events as platform signals

Status: **NOT IMPLEMENTED.** Proposal written 13 September 2026; no phase has
started. Nothing in this document describes shipped behaviour: kcp auditing is
not enabled in either install mode, there is no `pkg/audit` package, no
`cmd/faros-audit` binary, no `--kcp-audit-*` hub flags, no `audit` chart
values and no `AuditSubscription` API. Section 2 describes the current state
and is accurate as of the date above; everything from section 3 on is a plan.
When a phase lands, update this line and move the document out of
`docs/roadmap/`.

Companion to [install-embedded-kcp.md](../install-embedded-kcp.md),
[install-external-kcp.md](../install-external-kcp.md),
[organizations.md](../organizations.md) and
[hub-proxy-workspace-access.md](../hub-proxy-workspace-access.md).

## 1. The target

Every state change in a faros hub already passes through the kcp API server
as a request with a verified identity, a workspace, a verb and a result. kcp
can emit that as a Kubernetes audit event. Today we throw it away.

The target is a **small, stateless service that ingests kcp audit events and
turns them into signals**: a Discord or Slack message when something
interesting happens, a signed JSON POST to any HTTP endpoint, and a handful of
Prometheus counters. Concretely, an operator running a hub should be able to
turn it on with a few chart values and, within a minute, see in a channel:

```
🆕 workspace "demo" created in org Acme (e3f1…) by alice@example.com
🔌 provider "infrastructure" enabled in Acme/demo by alice@example.com
🚫 12 forbidden requests in 5m from bob@example.com in Acme/prod (list secrets)
🖥️ edge "home" registered in Acme/demo by system:serviceaccount:…:edges-provider
```

The measure is the same one the rest of the hub meets: one config file, no
new database, no new CRDs in phase 1, nothing that can block kcp when the
sink is down, and a rule set that is useful on day one without tuning.

What this is **not**: a compliance audit trail, a SIEM, or a query API over
history. Events are forwarded and forgotten. Anyone who needs retention points
the generic webhook sink at their own store (Loki, ClickHouse, S3 via a
collector). Section 8 lists the non-goals explicitly.

## 2. Where we are (13 September 2026)

### 2.1 kcp auditing is available but not enabled

- **Embedded kcp.** `pkg/hub/kcp/embedded.go` builds `serveroptions.NewOptions`
  and sets secure serving, TLS, static tokens, OIDC, batteries and embedded
  etcd. It never touches `kcpOpts.GenericControlPlane.Audit`, so no audit
  backend is configured. The k8s `AuditOptions` struct is there and supports
  the log backend (file or stdout) and the webhook backend (kubeconfig-shaped
  config file), in `batch`, `blocking` or `blocking-strict` mode.
- **External kcp via kcp-operator.** `RootShard` and `Shard` already carry
  `spec.audit.policy.configMap` and `spec.audit.webhook.configSecretName`
  plus the batch, backoff, mode and truncation knobs (kcp-operator
  `sdk/apis/operator/v1alpha1/shard_types.go`). Our install script
  `hack/install/06-kcp-shards.sh` sets neither.
- kcp's own flag allowlist (`pkg/server/options/flags.go`) exposes every
  `audit-log-*` and `audit-webhook-*` flag, so nothing upstream needs to
  change.

### 2.2 What a kcp audit event carries that a vanilla k8s event does not

kcp annotates every event before it reaches the backend
(`pkg/server/filters/filters.go:WithAuditEventClusterAnnotation`):

| Annotation | Value | Use |
|---|---|---|
| `kcp.io/cluster` | logical cluster ID (`Name`) | join key for everything else |
| `tenancy.kcp.io/workspace` | same as above (legacy key) | ignore |
| `kcp.io/path` | canonical path, e.g. `root:faros:tenants:<orgUUID>:<wsUUID>` | org + workspace resolution |

The authorization chain adds decision annotations on failures and on every
step when audit logging is on
(`pkg/authorization/decorator.go:AddAuditLogging`), keyed
`request.auth.kcp.io/<step>-decision` and `…-reason`, with steps such as
`01-requiredgroups`, `02-content`, `03-systemcrd`, `04-maxpermissionpolicy`,
`05-local`, `05-global`, `05-bootstrap`. So the audit event says *which*
authorizer denied a request and why, without any faros-side work.

### 2.3 Who shows up as the `user`

Because the hub proxy forwards OIDC bearer tokens unchanged and kcp verifies
them natively (`pkg/server/proxy/proxy.go`, no `Impersonate-*` headers), a
human appears in the audit event exactly as kcp authenticated them:

| Caller | `user.username` | `user.groups` |
|---|---|---|
| Portal / CLI user | `faros:<email>` (OIDC prefix set in `embedded.go`) | `faros:<group>…`, `system:authenticated` |
| Static-token user (dev) | `faros:static:<16-hex>` | `system:authenticated` |
| Provider (hub-provisioned SA) | `system:serviceaccount:<ns>:<name>` in the provider workspace | `system:serviceaccounts`, … |
| Hub itself, controllers | the hub's kcp identity (admin kubeconfig / bootstrap user) | `system:masters` or bootstrap groups |

That is enough to classify events into *human*, *provider*, *platform* and
*unknown* without any lookups.

### 2.4 Mapping a path to a tenant

`pkg/kcppaths` fixes the layout: `root:faros:tenants:<orgUUID>` is an org,
`root:faros:tenants:<orgUUID>:<wsUUID>` a workspace, `root:faros:providers:*`
and `root:faros:system:*` are platform. `Organization` CRs
(`apis/tenancy/v1alpha1/types_organization.go`) live in kcp and carry
`spec.displayName`; child workspaces are kcp `Workspace` objects whose
`metadata.name` is the UUID and whose human label is the
`tenants.faros.sh/display-name` annotation the hub patches on at creation
(`pkg/hub/kcp/bootstrap.go:WorkspaceDisplayNameAnnotation`). A single
informer on each is enough to render `Acme/demo` instead of two UUIDs.

### 2.5 What already exists that this should not duplicate

- The agents provider has `Connection` types for Discord, Slack, Telegram and
  SMTP (`providers/agents/apis/v1alpha1/types_connection.go`). Those are
  tenant-scoped, provider-side and carry OAuth state. A platform sink needs
  none of that: a Discord *incoming webhook* URL is one string. Do not reuse
  the agents connection model; do reuse its message-formatting conventions
  where they fit.
- The agents provider keeps its own tool-call audit log in Postgres. That is
  application-level and stays where it is. This plan covers the API layer.
- The hub has no Prometheus or OpenTelemetry wiring today (`grep -rn
  prometheus pkg/hub` is empty). The audit sink is the first place metrics
  land; keep the dependency surface to `prometheus/client_golang`.

## 3. Design

### 3.1 Shape

```
                     audit webhook (batch mode)
 ┌──────────────┐  POST audit.k8s.io/v1 EventList   ┌──────────────────┐
 │ kcp shard(s) │ ────────────────────────────────▶ │ faros audit sink │
 └──────────────┘                                   │  receive         │
                                                    │  enrich          │
   embedded: loopback into the hub process          │  match rules     │
   external: Deployment faros-audit, N replicas     │  aggregate       │
                                                    │  fan out         │
                                                    └───┬───┬───┬───┬──┘
                                                        │   │   │   │
                                                  Discord Slack HTTP  /metrics
                                                                      + stdout
```

One Go package, `pkg/audit`, with two hosts:

- **Embedded install.** The hub registers the receiver on its existing HTTPS
  listener at `/internal/audit/v1/events`, guarded by a bearer token the hub
  generates at start and writes into the kubeconfig-shaped webhook config it
  hands to kcp. kcp posts to `https://127.0.0.1:<hub port>/…`. No new pod, no
  new port, nothing on the network.
- **External install.** `cmd/faros-audit` is the same receiver as a
  Deployment behind a ClusterIP Service. The chart renders a Secret with the
  webhook kubeconfig and the install script points `RootShard`/`Shard`
  `spec.audit.webhook.configSecretName` at it. Each shard posts
  independently; events are per-shard by construction so there is nothing to
  deduplicate.

### 3.2 Audit policy

The policy is deliberately small and ships as a file in the chart
(`deploy/charts/faros-hub/files/audit-policy.yaml`), mounted for embedded kcp
and put in a ConfigMap for the operator. First version:

```yaml
apiVersion: audit.k8s.io/v1
kind: Policy
omitStages: [RequestReceived]           # one event per request, at completion
rules:
  # Drop the firehose: reads by the platform itself and by providers.
  - level: None
    userGroups: [system:masters, system:serviceaccounts]
    verbs: [get, list, watch]
  # Drop informer noise and health probes for everyone.
  - level: None
    verbs: [watch]
  - level: None
    nonResourceURLs: ["/healthz*", "/readyz*", "/livez*", "/metrics", "/openapi*", "/api", "/apis", "/version"]
  # Everything else: Metadata only. No request or response bodies ever.
  - level: Metadata
```

Metadata level means user, verb, resource, name, namespace, response code and
annotations. It never includes object bodies, so Secrets contents, app source
and agent prompts cannot leak through this path. The policy is the only place
that could change that, so it is reviewed like code.

Reads by humans are kept at Metadata because "who listed secrets in prod"
is one of the most useful signals. The rule engine (3.4) decides what is
forwarded; the policy decides what is *generated*, and generating a Metadata
event is cheap.

### 3.3 Enrichment

For every event the sink derives, once, a flat struct the rules and sinks
consume:

| Field | Source |
|---|---|
| `cluster`, `path` | `kcp.io/cluster`, `kcp.io/path` annotations |
| `orgUUID`, `wsUUID`, `scope` | `kcppaths` on `path`: `tenant-workspace`, `tenant-org`, `platform-providers`, `platform-system`, `root`, `other` |
| `orgName`, `wsName` | informer cache on `Organization` and `Workspace`; UUID if unknown |
| `actorKind` | `human` (`faros:` prefix, not `faros:static:`), `static`, `provider` (SA in `root:faros:providers:*` or `…:providers` org workspace), `platform` (`system:masters`, hub identity), `system` (`system:*` other), `unknown` |
| `actor` | username, with an optional hashing mode (3.6) |
| `verb`, `group`, `resource`, `subresource`, `name`, `namespace` | `objectRef` and `verb` |
| `code`, `denied` | `responseStatus.code`; `denied` when code is 401 or 403 |
| `deniedBy`, `deniedReason` | first `request.auth.kcp.io/*-decision: forbidden` annotation and its reason |
| `sourceIP` | first of `sourceIPs` |

Enrichment does no network calls on the hot path. The two informers are the
only kcp reads.

### 3.4 Rules

Rules are a YAML list, one file, loaded at start and on SIGHUP. A rule is a
match plus a list of sink names plus optional aggregation:

```yaml
rules:
  - name: workspace-created
    match: { verb: create, group: tenancy.kcp.io, resource: workspaces, code: 201 }
    sinks: [ops-discord]
    message: "🆕 workspace {{.name}} created in {{.orgName}} by {{.actor}}"

  - name: provider-enabled
    match: { verb: create, group: apis.kcp.io, resource: apibindings, actorKind: human }
    sinks: [ops-discord]
    message: "🔌 {{.name}} bound in {{.orgName}}/{{.wsName}} by {{.actor}}"

  - name: forbidden-storm
    match: { denied: true, actorKind: [human, static] }
    aggregate: { window: 5m, by: [actor, orgUUID, wsUUID], minCount: 5 }
    sinks: [ops-discord, security-webhook]
    message: "🚫 {{.count}} forbidden requests in {{.window}} from {{.actor}} in {{.orgName}}/{{.wsName}} ({{.sample.verb}} {{.sample.resource}})"

  - name: platform-workspace-touched-by-human
    match: { scope: [platform-system, platform-providers], actorKind: human, verb: [create, update, patch, delete] }
    sinks: [security-webhook]

  - name: everything
    match: {}
    sinks: [stdout, metrics]
```

Match semantics are exact-or-set on every enriched field; absence means any.
There is no expression language in phase 2. If a real need for CEL appears,
add it then; the fields are already flat enough.

`aggregate` folds matching events into one message per key per window and
emits when the window closes, or immediately when `minCount` is crossed the
first time. This is what keeps a misbehaving informer from posting 400
Discord messages.

### 3.5 Sinks

| Sink | Config | Delivery |
|---|---|---|
| `discord` | webhook URL | one embed per message, 30/min per URL rate limit respected client-side, 429 honoured |
| `slack` | incoming webhook URL | Block Kit text, same rate limiting |
| `http` | URL, optional HMAC secret, headers | JSON body `{rule, message, event}` with the enriched event, `X-Faros-Signature: sha256=<hmac>`, retries with backoff, bounded queue |
| `stdout` | none | one JSON line per event, for `kubectl logs` and log collectors |
| `metrics` | none | Prometheus counters on `/metrics` (3.7) |

Every sink has a bounded in-memory queue. When it fills, the sink drops and
increments `faros_audit_sink_dropped_total{sink}`. kcp is never back-pressured:
the webhook is configured in `batch` mode and the receiver returns 200 as
soon as the batch is decoded and queued.

Secrets (webhook URLs, HMAC keys) come from a Secret mounted as env vars and
are referenced from the rules file by name, so the rules file itself can be a
ConfigMap.

### 3.6 Privacy and tenancy

- Metadata level only; see 3.2.
- `requestURI` is stripped of query strings before it leaves the sink; the
  `path` annotation is what rules use, not the URI.
- `actor` can be forwarded verbatim (default for an operator-owned Discord)
  or as `sha256(actor)[:12]` (`actorHashing: true`), for sinks that leave the
  operator's control.
- Phase 3 org-scoped subscriptions only ever see events whose `orgUUID`
  matches the subscription's org. Platform-scope events are never delivered
  to tenants.

### 3.7 Metrics

`faros_audit_events_total{scope, actorKind, verb, group, resource, code}` is
the baseline. Org labels are off by default because org count is unbounded;
`metrics.orgLabels: allowlist` with an explicit list of org UUIDs is the
escape hatch for "how active is customer X". Plus per-sink `delivered_total`,
`dropped_total`, `errors_total`, `queue_depth`, and receiver
`batches_total`, `batch_size` histogram, `decode_errors_total`.

This is the cheapest usage telemetry the platform can get: which orgs are
active, which providers are being bound, which resources are hot, without
touching any provider.

### 3.8 What does not flow through kcp

Hub REST handlers under `/api/orgs/...` (membership changes, provider
enable, BYO registration) act on kcp with the hub's own identity after doing
their own authorization. In the audit event the actor is the hub, not the
person. Two options; the plan takes the first:

1. **Synthetic events.** The hub emits an `audit.Event` into the same
   pipeline from the REST layer via `pkg/audit.Emit(ctx, ...)`, with
   `actor` from `TenantContext`, `actorKind: human` and an extra annotation
   `faros.sh/via: rest`. Same struct, same rules, same sinks. Costs one call
   per handler that mutates state; there are a bounded number of those.
2. Impersonation headers from the hub REST layer to kcp so kcp records the
   person. Cleaner on paper, but it changes the hub's authorization model and
   is out of scope for a telemetry feature.

The same `Emit` path covers the proxy's own 401s (token rejected before kcp
ever sees the request), which kcp cannot audit.

Delegated provider tokens (a provider acting for a user) show up as the
provider SA. That is correct for this feature; attributing them to a person is
the open design question tracked in the provider hub-access plan, not here.

## 4. Configuration

### 4.1 Off by default, everywhere except Tilt

Auditing is opt-in at every layer. The hub binary does nothing unless a
policy file is passed, the chart ships `audit.enabled: false`, the install
scripts under `hack/install/` do not set it, and a local hub started any way
other than Tilt (`go run ./cmd/faros-hub`, the e2e harness, `make e2e-*`)
sees no audit configuration at all. There is no "on in dev, off in prod"
toggle inferred from `--dev-mode` or similar; the only signal is explicit
configuration.

The one place it is on is the **Tilt dev loop**, and there only with the
**log sink**: the hub `local_resource` in the `Tiltfile` passes the policy
file and a rules file whose single rule routes everything to `stdout`. No
chat sinks, no `http` sink, no `/metrics` scraping, no secrets. The point is
that `tilt logs hub` shows one JSON line per tenant write while someone is
developing a provider, and nothing leaves the machine. Anyone who wants a
Discord sink in Tilt adds it to their own untracked `tilt_config.json`; the
committed Tiltfile never references a webhook URL.

### 4.2 Chart values

Embedded mode, with auditing turned on by the operator:

```yaml
audit:
  enabled: true                            # chart default is false
  policy: files/audit-policy.yaml          # override with policyConfigMap
  rules: files/audit-rules.yaml            # override with rulesConfigMap
  sinks:
    ops-discord:
      type: discord
      urlSecretRef: { name: faros-audit-sinks, key: ops-discord }
    security-webhook:
      type: http
      url: https://collector.example.com/faros
      hmacSecretRef: { name: faros-audit-sinks, key: security-hmac }
  actorHashing: false
  metrics:
    orgLabels: none                        # none | allowlist
```

External mode adds `audit.standalone.replicas` and renders the webhook
kubeconfig Secret that `06-kcp-shards.sh` wires into the shards.

Hub flags (embedded): `--kcp-audit-policy-file`, `--audit-rules-file`,
`--audit-sinks-file`. All three default to empty. When the policy flag is
empty, kcp is started exactly as today and the receiver is not registered.

Tilt (the only place this is on without an operator asking for it):

```
./bin/faros-hub ... \
  --kcp-audit-policy-file=deploy/charts/faros-hub/files/audit-policy.yaml \
  --audit-rules-file=hack/tilt/audit-rules-log-only.yaml
```

where `audit-rules-log-only.yaml` is one rule, `match: {}`, `sinks: [stdout]`,
and no sinks file is passed.

## 5. Default rule set

Shipped in the chart and used only when an operator sets `audit.enabled: true`
and does not override `audit.rules`. Tilt does not use this set; it uses the
log-only rule from 4.1.

| Rule | Signal | Aggregated |
|---|---|---|
| org created / deleted | `Organization` create/delete in `root:faros:tenants` | no |
| workspace created / deleted | `Workspace` create/delete under an org | no |
| member added / removed | synthetic event from the membership REST handlers | no |
| provider enabled / disabled | synthetic event from the enable/disable REST handlers, plus `APIBinding` create by a human | no |
| edge registered | edge CR create in a tenant workspace | no |
| forbidden storm | ≥5 denied requests per actor+workspace in 5m | yes |
| auth failure storm | ≥20 proxy 401s per source IP in 5m (synthetic) | yes |
| human writes in platform workspaces | any mutating verb by `actorKind: human` in `platform-*` scope | no |
| secret reads by humans | `get`/`list` on `secrets` by `actorKind: human` | 15m window per actor |

Everything goes to `stdout` and `metrics`; only the rows above go to chat
sinks.

## 6. Phases

**Phase 0: make auditing possible.** Add `--kcp-audit-policy-file` and
`--kcp-audit-log-path` to the hub, both empty by default; set
`kcpOpts.GenericControlPlane.Audit` from them in `embedded.go` only when the
policy flag is set. Add the policy file to the chart behind
`audit.enabled: false`. Add an opt-in `AUDIT=1` to `06-kcp-shards.sh` that
sets `spec.audit.policy`; the default run leaves it unset. Pass the policy
flag and `--kcp-audit-log-path=-` from the Tiltfile so `tilt logs hub` shows
raw events. Verify in e2e, with the flags passed explicitly for that test
only, that a workspace create produces an event with the three kcp
annotations and the `kcp.io/path` we expect. No new service yet.

**Phase 1: the sink, in-process.** `pkg/audit` with receiver, enrichment,
`stdout` and `metrics` sinks, informer-backed name resolution, the loopback
webhook wiring for embedded kcp, bearer guard, `/metrics` on the hub. Unit
tests on enrichment against captured real events. Tilt switches from the raw
kcp log to the sink with the log-only rule (4.1); every other entry point
stays off.

**Phase 2: rules and chat.** Rule loader, matcher, aggregation, `discord`,
`slack` and `http` sinks with rate limiting and bounded queues, the default
rule set, synthetic events from the REST layer (3.8). `cmd/faros-audit`
standalone binary and the external-install wiring. e2e: create a workspace
through the CLI, assert a fake HTTP sink receives a `workspace-created`
message with the right org name. Docs page moves out of `roadmap/`.

**Phase 3: org-scoped subscriptions.** An org admin registers their own
sink: `POST /api/orgs/{org}/audit-subscriptions` with a webhook URL and a
subset of the rule catalogue, stored as an `AuditSubscription` object in the
org workspace, reconciled into the running rule set. Events are filtered to
the org. Portal page under org settings. This is the point where the feature
becomes a tenant-facing product ("get a Slack message when someone deploys
to prod"), so it gets its own short design once phase 2 is real.

**Phase 4 (only if asked): history.** Not planned. If a customer needs
"show me what happened last Tuesday", the answer is the `http` sink into
their own store, and possibly a kuery-side reader over it. Do not add a
database to this service.

## 7. Risks

- **Volume.** A busy hub with many informers produces thousands of events
  per minute; the policy drops `watch` and platform reads, and batch mode
  plus bounded queues make the sink shed load rather than kcp. Measure in
  phase 1 on console-dev before turning on chat sinks anywhere real.
- **Cardinality.** Org labels on metrics are opt-in by allowlist for this
  reason.
- **Chat spam.** Aggregation and client-side rate limiting are in phase 2
  for this reason; a rule without `aggregate` that matches reads is a
  configuration bug, and the loader warns about it.
- **Path annotation gaps.** `kcp.io/path` is empty when the
  `LogicalCluster` is not in the informer yet (first seconds after a
  workspace is created). Enrichment falls back to `kcp.io/cluster` and the
  rules match on `orgUUID` being unknown as `scope: other`; the workspace
  create event itself is scoped by its *parent* path, which is always known.
- **Multi-shard blind spots.** The front-proxy does not audit; anything it
  rejects before forwarding is invisible. Acceptable, and the same synthetic
  path used for the hub proxy can be applied there later if needed.

## 8. Non-goals

- Compliance-grade retention, tamper evidence, or completeness guarantees.
- Request or response bodies at any level.
- An expression language for rules.
- Replacing the agents provider's tool-call log or App Studio run records.
- Auditing edge clusters. Their API servers are not ours; the edge providers
  can adopt the same `http` sink contract if they ever want to feed events in.
