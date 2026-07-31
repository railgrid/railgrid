# Giving agents web access

A kedge agent starts out unable to see the web. It can reason and it can call
whatever tools its Connections give it, but "look this up" and "open that page"
need a backend. This document covers the two infrastructure templates that
provide them, and how to wire each to an agent.

| Need | Template | Agent Connection |
|---|---|---|
| Find pages — queries, ranked links, snippets | `searxng` | `websearch`, `config.provider: searxng` |
| Drive a page — JavaScript, logins, clicks, screenshots | `browser` | `mcp` |

Both are ordinary infrastructure instances: a tenant provisions their own,
isolated from every other tenant's.

## Why self-host these

**Search.** The obvious alternative is a hosted search API (Brave, Serper,
Tavily). Those work — the `websearch` Connection speaks Brave out of the box —
but they are metered, per-query, per-tenant billing you have to set up before an
agent can do anything, and a rate limit an agent will hit while iterating.
[SearXNG](https://docs.searxng.org/) is a metasearch front-end: it forwards a
query to public engines and normalizes the answers. No API key, no per-query
cost, no vendor account.

The alternative on the other side is scraping a search engine directly. Don't:
it breaks whenever the result markup changes, it violates the engines' terms,
and it gets your egress IP blocked in about a day. SearXNG already carries the
per-engine parsers and maintains them upstream.

**Browsing.** `web_fetch` retrieves a URL and hands the model the text. That is
the right tool for most pages and it costs one HTTP request. It cannot help with
a page that renders its content in JavaScript, a page behind a login, or a task
that needs clicking through a flow. For those you need a real browser, and
[Playwright MCP](https://github.com/microsoft/playwright-mcp) is the browser
Anthropic-style tool-calling was designed against: it exposes navigate, click,
type, snapshot and screenshot as MCP tools, driving a headless Chromium.

## Provisioning

Both templates take two inputs that matter: `name` and `size`. There is no
credential to create and no order to respect — provision the instance and name
it in a Connection, in whichever order suits you.

Both declare `exposure: optional`, which is what the portal and the MCP tools
read to tell you whether to expect a URL. By default there is none: an instance
is reached over the platform's internal path. [Opting into a public
hostname](#optional-public-exposure) is a separate, gated decision.

### SearXNG

```
provision(template="searxng", values={"name": "search", "size": "small"})
```

`size` picks the SearXNG container's memory bucket — small 256Mi, medium 512Mi,
large 1Gi. A query fans out to many engines concurrently, so this bounds
concurrency more than it bounds a working set; a single agent is fine on small.

### Browser

```
provision(template="browser", values={"name": "browser", "size": "small"})
```

`size` sets both the Chromium memory limit and the matching `/dev/shm` tmpfs —
small 1Gi/256Mi, medium 2Gi/512Mi, large 4Gi/1Gi. Chromium puts renderer shared
memory in `/dev/shm`, and the container default of 64Mi is the classic cause of
tabs dying with "Target closed"; the template mounts a memory-backed `emptyDir`
sized to the bucket instead. Those tmpfs pages count against the container's
memory limit, which is why the two scale together.

Neither instance publishes a URL. `status` carries `ready`, `runtimeNamespace`
and `serviceRef` — the last two are what the data plane resolves through, not
anything you paste anywhere.

## Wiring to an agent

### SearXNG → a `websearch` Connection

```yaml
apiVersion: agents.kedge.faros.sh/v1alpha1
kind: Connection
metadata:
  name: search
spec:
  type: websearch
  config:
    provider: searxng
    instance: search        # the searxng instance's name — that is the whole binding
```

No `baseURL`, no Secret. Reference the Connection from a toolset or agent and
the built-in `web_search` tool is backed by your instance.

Under the hood the client calls the instance's `proxy` verb on the
infrastructure provider's data plane:

```
GET <hub>/services/providers/infrastructure/dataplane/clusters/<cluster>/searxngs/<instance>/proxy/search?q=…&format=json
Authorization: Bearer <the calling user's token>
```

The bearer token is the **caller's own**, not a credential belonging to the
instance — the data plane re-reads the instance as that caller and refuses if
they have no permission on it. Nothing is minted, stored or rotated.

The response is parsed as `{"results": [{"title", "url", "content"}]}` and
capped at 5 hits per query — the agent is expected to follow up with `web_fetch`
on whichever look relevant rather than reading a long list into context.

`config.instanceResource` overrides the instance resource name (default
`searxngs`) if you run a fork of the template under a different CRD.

An external SearXNG still works the old way — set `baseURL` (and a token in the
Connection Secret if it is gated) and leave `instance` unset.

### Browser → an `mcp` Connection

```yaml
apiVersion: agents.kedge.faros.sh/v1alpha1
kind: Connection
metadata:
  name: browser
spec:
  type: mcp
  config:
    instance: browser       # the browser instance's name
```

Same shape as search, for the same reason: you name the instance, the provider
composes the data-plane URL and authenticates as you. A cluster ID never appears
in anything you author. `config.instanceResource` overrides the resource name
(default `browsers`).

The verb root **is** the MCP endpoint — the template pins `/mcp` as the
endpoint's `upstreamPath`, so nothing is appended. Playwright's tools then
appear to the model as `browser__browser_navigate`, `browser__browser_click`,
`browser__browser_type`, `browser__browser_snapshot`, and the rest of the
upstream set (the prefix is the Connection name).

An `mcp` Connection with a `baseURL` and no `instance` is unchanged: that is how
you reach an MCP server outside the platform.

### Background and scheduled runs: the agent's own identity

The data plane authorizes per caller, and a scheduled, heartbeat, wakeup or
inbound-channel run has no human to act as. Each agent therefore gets a
**ServiceAccount of its own** in the tenant workspace, and background runs call
the data plane as that identity. Nothing to configure: the provider creates it
on first use.

What gets created, once per agent, in the `default` namespace:

| Object | Name | Purpose |
|---|---|---|
| ServiceAccount | `kedge-agent-<agent>` | the identity |
| Secret | `kedge-agent-<agent>-token` | its token, populated by kcp's token controller |
| ClusterRole + binding | `kedge-agent-<agent>` | `get`/`list` on `infrastructure.kedge.faros.sh` |

Things worth knowing before you rely on it:

- **The grant is workspace-wide, not connection-scoped.** The agent's identity
  can read *any* infrastructure instance in its workspace, not only the ones its
  Connections name. That keeps the Role stable as connections change, at the
  cost of a wider blast radius if the token is read out of the workspace.
- **It is a standing credential.** kcp has no TokenRequest API, so this is a
  long-lived ("legacy") ServiceAccount token that does not expire. Revoking it
  means deleting the ServiceAccount; the provider caches tokens for 30 minutes,
  so a revocation takes effect within that window.
- **Read-only.** The Role grants `get` and `list` and nothing else, over one API
  group. An agent identity cannot read Secrets or mutate anything.
- **Failure is not fatal to the run.** If the identity cannot be provisioned,
  the run proceeds without instance-backed tools rather than being lost, and the
  provider logs `identity unavailable`. The tool then reports that it has no
  identity, instead of composing a call that 401s two hops away.

A Brave-backed `websearch` Connection needs none of this — its credential is in
the Connection Secret, which every run can read.

### Local development (Tilt)

This works in Tilt with no port-forward and no special casing, which is most of
why the design changed. The agents provider runs on your host, talks to the hub
over HTTPS like it does in production, and the hub reaches the instance through
the runtime cluster's API server. No gateway, no host-to-cluster bridge, no
published hostname that resolves to `127.0.0.1` and goes nowhere.

The one dev-only concession is TLS: the local hub's certificate is self-signed,
so the data-plane client honours the provider's existing insecure-hub flag. It
is the same flag the rest of the provider's hub traffic uses — not a new escape
hatch for this path.

One gotcha survives the rewrite: a search backend is not usable until the
Connection is **wired to an agent** (Config → Tools). `web_search` only exists
for an agent that was granted a websearch connection; otherwise the agent will
tell you it cannot make web requests, having no idea the backend exists. The
assisted-setup flow does this wiring for you; the manual path does not.

## Security model

Neither instance is published by default. Unless an instance opts into
[public exposure](#optional-public-exposure), there is no HTTPRoute and no
hostname, and the only way in is the instance's `proxy` verb on the
infrastructure provider's data plane, which the provider serves by calling
`services/proxy` on the runtime cluster's API server. Authorization is the
caller's own `get` permission on the instance object; the resolver confines
every Service reference to the instance's backend-owned runtime namespace, so a
forged or mutated status cannot redirect the proxy elsewhere. The full rationale
and the alternatives weighed are in
[platform-internal-networking.md](platform-internal-networking.md).

**Stated plainly, because it is a real change:** neither SearXNG nor Playwright
MCP has any authentication of its own, and this repo installs no
NetworkPolicies. Anything already running on the runtime cluster that can reach
the Service can use these instances unauthenticated — including, when exposed,
by bypassing oauth2-proxy and addressing the app's Service directly from
another pod. The gate defends the hostname, not the Service. What changed is the size of
that set — "workloads on the runtime cluster" instead of "the internet" — and
that the sanctioned path is RBAC-gated per caller rather than gated by one
shared token every holder of which is equivalent. For the browser this matters
more than for search: it is a browser driving with the cluster's egress IP and
whatever logins its live session happens to hold.

### Optional public exposure

Sometimes a human wants to open SearXNG in a browser, and the internal path
cannot serve that — it needs a caller that can present a kcp bearer token. So
exposure is available, off by default, and gated:

```
provision(template="searxng", values={
  "name": "search",
  "expose": {"enabled": True},
  "oidc": {"mode": "byo", "issuerURL": "https://id.example.com", "clientID": "searxng"},
})
```

That publishes the instance on a hostname behind **oauth2-proxy**, using the
same machinery the `application` template has always used — including the OIDC
client-secret bridge, so nothing new was added to reach it. Put the client
secret in your `cloud-credentials` Secret under `oidc_client_secret`.

Three properties are deliberate:

- **There is no ungated mode.** `application` offers `oidc.mode: none` for
  demos; these two do not. Their schema enum contains only `byo`, and the
  controller refuses a hand-edited instance that asks for `none` while exposed
  (condition `GateRequired`). SearXNG and Playwright MCP have no authentication
  of their own, so an ungated hostname is not a weaker configuration — it is an
  open metasearch proxy or a remotely-driven browser.
- **Exposure is indivisible.** The HTTPRoute, oauth2-proxy, its Service and its
  cookie-secret Job all hang off the same `${schema.spec.expose.enabled}`
  condition, so there is no state where the route exists and the gate does not.
  A test pins this.
- **The internal path is unaffected.** Turning exposure on adds a second, gated
  way in for humans; agents keep using the data plane, and the two share no
  credential. `status.url` is populated only when exposed — an empty string
  otherwise, rather than a URL that is only sometimes real.

Turning it off again removes the route and the gate together.

What the internal path removed, and why none of it is missed:

- **Unconditional publication** — every instance got a hostname because the
  provider had no other way to reach it. Exposure is now opt-in, and for a
  purpose (a human with a browser) rather than as a workaround.
- **The nginx bearer sidecar** — existed only to keep the internet out of that
  hostname. The app now binds `0.0.0.0`; the Service points straight at it, and
  oauth2-proxy fronts it when exposed.
- **The token bridge** — copied the agents Connection's Secret into the
  instance's runtime namespace so the sidecar could mount it. Deleted with its
  finalizer and its `spec.tokenSecretRef` / `spec.tokenSecretName` inputs.
- **The shared bearer token itself** — one credential the tenant authored, the
  portal minted, the controller copied and both sides compared. Nothing mints
  it now and there is nothing to rotate.

`searxng` keeps its `pwgen` Job for one narrow purpose: SearXNG's own
`server.secret_key`, which signs its preference cookies and image-proxy URLs. It
never leaves the instance and gates no access.

## Known limits

### Search results skew, and sometimes come back empty

These instances run in a datacenter. **Google, Bing and several other engines
block or captcha datacenter egress IPs.** They show up in the JSON response's
`unresponsive_engines` and contribute nothing, so results skew toward the
engines that tolerate cloud egress — DuckDuckGo, Brave, Startpage, Mojeek,
Wikipedia/Wikidata, and the specialist ones.

Practical consequences:

- Expect fewer and differently-ranked hits than the same query typed into a
  browser. This is not a misconfiguration.
- An empty `results` array usually means engine blocking, not "no such page".
  Re-word the query, or target a specific engine with a `!bang` prefix
  (`q=!duckduckgo kubernetes operators`), before concluding a topic has no
  coverage.
- `number_of_results` is usually `0`. Most engines don't report a total, so it
  is not a result count — use the length of `results`.

The template's SearXNG config is deliberately minimal: it starts from
`use_default_settings: true` and overrides only what it must. Starting from the
defaults matters — a hand-written `settings.yml` that omits that line replaces
the shipped configuration wholesale, and SearXNG then refuses to start on the
first missing required key. The overrides are:

- `search.formats: [html, json]` — the JSON API is **off by default**. Without
  it every `format=json` request answers 403, which is the single most common
  "my SearXNG API doesn't work" cause.
- `server.limiter: false` — the limiter is a bot-abuse defence for *public*
  instances. It profiles User-Agent and Accept headers and rate-limits anything
  that doesn't look like a browser, which is precisely what an API client is,
  and it needs a Valkey/Redis backend this template doesn't run. The instance is
  not public, so the limiter would only break the contract.
- `server.secret_key` comes from the Secret via `SEARXNG_SECRET`, not from the
  world-readable ConfigMap.

There is no `engines` input. SearXNG's engine list is a list of maps in
`settings.yml`, not a comma string, and freezing an engine set at provision time
is worse than choosing per query. Callers select engines per request instead —
`categories=`, `engines=`, or a `!bang` prefix — which is finer-grained and
survives upstream engine churn.

### The browser is slow, heavy, and stateful

- **Cost and latency.** A browser round-trip is a page load, a render, and an
  accessibility dump. It is far slower and far more memory-hungry than
  `web_fetch`. Use it only when the page genuinely needs JavaScript execution, a
  session or login, or interaction — not for plain page reads, and not for
  finding pages (that's `web_search`).
- **One instance is one Chromium.** The page you navigated to stays open,
  cookies and logins persist between tool calls, and every tool acts on whatever
  the current page is. Two agents pointed at the same instance will fight over
  the same tab. Provision one instance per agent that needs concurrent
  browsing — they're cheap to create. The Deployment is pinned to one replica
  for the same reason, doubly so now: `services/proxy` resolves to a random
  ready Endpoint, so a second replica would scatter one MCP session across two
  browsers.
- **Nothing is persisted.** A pod restart drops open tabs, cookies and logins.
  Treat a session as disposable and re-establish state rather than assuming it
  survived. The Deployment uses the `Recreate` strategy for the same reason: a
  rolling update would hand some calls to a cold Chromium that never saw the
  first navigation.
- **Headless only.** The upstream image's entrypoint hard-codes `--headless`
  and, per upstream, supports headless Chromium only — there is no display to
  render into. The templates therefore expose no `headless` toggle rather than
  an input that couldn't change anything. `--no-sandbox` is also baked into that
  entrypoint and is required: Chromium's setuid sandbox needs privileges the pod
  doesn't have. Not being reachable from outside the runtime cluster is what
  contains it instead.

## Related

- [Agents provider architecture](agents-provider-architecture.md)
- [Infrastructure architecture](infrastructure-architecture.md)
- [Platform-internal networking](platform-internal-networking.md)
- [SearXNG search API](https://docs.searxng.org/dev/search_api.html)
- [Playwright MCP](https://github.com/microsoft/playwright-mcp)
