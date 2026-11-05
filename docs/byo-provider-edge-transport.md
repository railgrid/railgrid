# Edge transport for organization providers

The organization registers a connected Kubernetes Edge before any provider
credential is issued. The Hub records the selection on its own Workspace object.
A provider's backend address must identify a Kubernetes Service on that Edge;
it cannot select a host in Railgrid's private network.

## Custom subresources

1. The caller invokes the normal kcp path:
   `/clusters/{id}/apis/{group}/{version}/{resource}/{name}/{verb}`.
2. kcp authenticates the caller and authorizes the custom subresource.
3. A Hub-owned DataPlaneEndpointSlice routes the request to the provider relay.
   The Hub verifies the organization, current binding/export identity and verb.
4. The relay signs the caller identity and exact destination, method, path and
   query. The platform Edges provider verifies that envelope, then sends the
   request through the selected Edge's tunnel to the customer Service.
5. The customer backend receives the original identity, never the Hub transport
   bearer or relay proof. SDK handlers retain their normal interface.

Embedded kcp dispatches the relay over an in-process HTTP transport at the reserved
origin `http://railgrid-provider-relay.internal`. That origin has no network
listener and is not mounted on the public Hub router. Streams and upgrades use
the same authenticated route.

## External kcp

Deploy the compatible Railgrid kcp serving binary with the organization boundary
hooks. Stock external kcp does not acquire those hooks merely by upgrading the
Hub. Upstream kcp quota-accounting changes are not part of this implementation.

Build it with `make build-railgrid-kcp` (or `make docker-build-kcp`). Run
`railgrid-kcp start` with the shard's normal storage, authentication and TLS
configuration. The command accepts the existing kcp serving flags and installs
the same organization guard as embedded mode before the server starts.
For a standalone root deployment, keep the shard name `root`; the relay client
certificate name is independent of that flag. Mount writable state and set
`--root-directory` to that location.
Give the Hub an admin kubeconfig scoped to the root logical cluster, for example
`https://kcp.example.internal:6443/clusters/root`, rather than a bare shard URL.
Use the normal `kcp-admin` identity (`system:kcp:admin`), rather than a shard's
`system:masters` identity: the latter intentionally skips workspace ownership
metadata required by lifecycle initialization.

Use a dedicated Hub mTLS relay listener reachable by the shards:

```text
--provider-relay-url=https://hub-relay.example.internal:9444
--provider-relay-listen-addr=:9444
--provider-relay-cert-file=/provider-relay/tls.crt
--provider-relay-key-file=/provider-relay/tls.key
--provider-relay-client-ca-file=/provider-relay/client-ca.crt
--provider-relay-allowed-names=kcp-shard
```

The relay URL is an HTTPS origin. Its serving certificate must cover that host;
shard certificates must chain to the configured client CA and have an explicitly
allowed certificate common name. Configure the kcp shard's client certificate,
key and virtual-workspace trust CA accordingly. Do not terminate shard client
TLS at an ordinary ingress before it reaches this listener.

On the shard, configure `--shard-client-cert-file`, `--shard-client-key-file`
and `--shard-virtual-workspace-ca-file`. The trust bundle must cover both the
advertised kcp virtual-workspace endpoints and the Hub relay certificate. Keep
`--shard-external-url` and `--shard-virtual-workspace-url` pointed at the shard's
own reachable HTTPS origin. The Hub relay origin belongs only in the Hub's
`--provider-relay-url`; the bootstrap endpoint uses it for org-provider custom
subresource destinations. It does not serve APIExport virtual workspaces.

The Hub chart's `hub.providerRelay` section configures the dedicated listener and
Secret mount. Ordinary browser credentials and caller-supplied `X-Remote-*`
headers do not authenticate a shard. Both embedded and external transports must
pass the same organization/binding authorization checks.

The signed relay envelope is a short-lived identity delegation, not a complete
request signature. It binds the destination, HTTP method, path, query and
caller identity for 30 seconds, but does not bind the request body and has no
one-time nonce. Protect every network segment that carries the envelope. An
external shard reaches the Hub over the dedicated mTLS listener, and the Hub
uses its configured kcp transport (normally HTTPS) for the next hop. The chart
currently advertises the platform Edges provider through an in-cluster HTTP
Service, so keep that shard-to-Edges segment on trusted private cluster
networking and make the custom-subresource listener unreachable from tenant
workloads; use authenticated TLS if that segment crosses a less-trusted
network. The external shard-to-Hub mTLS listener does not by itself secure the
later Hub-to-kcp-to-Edges path. A party able to capture a live envelope on that
later path could replay it for the same method, path and query with a different
body until it expires. The tunnel consumes the proof before the customer
Service sees the request. This transport assumption lets streaming bodies pass
through without buffering them for a body digest.

## Other backend traffic

MCP, OAuth, UI assets, health and supported non-kube route classes keep the
existing Hub-to-Edge routing. Scoped delegated credentials replace interactive
user bearers before the Edge delivers requests to customer backends. UI asset
grants are for assets only and do not grant arbitrary backend or kcp access.
Org-owned provider responses from ordinary backend routes, granted UI asset
fetches, and Edge agent ingress use the Hub's response policy: upstream
`Set-Cookie`, CORS, browser policy, interim 1xx responses, and trailers are not
forwarded; the Hub forces its CSP and cache policy and sets `nosniff`. The
ordinary `/services/providers/{name}/oauth/*` routes retain OAuth `Location`
redirects, and `/services/providers/{name}/mcp` plus
`/services/providers/{name}/mcp/sse` retain `WWW-Authenticate` metadata
challenges. Granted UI assets also retain content
metadata needed for SRI and range requests, with `Referrer-Policy: no-referrer`.
Agent WebSocket upgrades keep their duplex stream. Custom subresources use a
stricter policy that rejects external redirects and drops authentication
challenges. Platform-owned providers keep their existing response behavior.

The workspace's actual APIBinding selects a provider copy; catalog shadowing
alone cannot switch an existing workspace from a platform to an organization
provider. Keep shared platform-provider listeners private from tenant workloads:
those listeners trust identity headers sent by the shard.

Provider outbound control traffic uses kcp's advertised virtual-workspace URLs.
Every advertised shard endpoint must be reachable from the customer Pods with
working DNS and TLS. Cluster-internal DNS names cannot serve external consumers.
See [BYO providers](byo-providers.md) for installation and accepted MVP gaps.
