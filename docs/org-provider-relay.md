# Organization provider custom subresources

Organization providers submit their generated APIExport, schemas, private CRDs,
and CatalogEntry through the authenticated SDK bootstrap endpoint. The hub
publishes the DataPlaneEndpointSlice; the provider credential cannot mutate the
export, endpoint, CRD conversion, or bootstrap RBAC directly. Backend addresses
remain Kubernetes Service DNS targets in the organization's selected Edge.

A custom subresource follows this route:

1. kcp authenticates and authorizes the caller.
2. The shard sends its stamped identity to the hub's provider relay.
3. The relay checks the registered provider, the consumer workspace's owning
   organization, and its bound APIBinding. It signs the original identity and
   exact method, path, query, destination cluster, Service, and hop count.
4. The hub calls the platform edges provider through kcp as the hub. Edges
   verifies the separate relay envelope and forwards the original identity over
   the selected Edge tunnel. Neither the hub credential nor the envelope reaches
   the organization backend.
5. The SDK's normal custom-subresource Gate reviews parent visibility and reads
   through the provider's export virtual workspace.

The signature lasts at most 30 seconds. Edges gets the verification key from
`/api/provider-relay/public-key` on its configured `RAILGRID_HUB_URL`, using its
configured hub TLS trust. It refuses redirects, bounds the response size, and
refreshes the cached key every 30 seconds. Key rotation can cause a temporary
failure during that cache window. The hub proof root is cached for its process
lifetime, so rotating that root also requires restarting every hub replica. Do
not use insecure hub TLS outside local fixtures.

## Embedded kcp

The embedded shard uses an in-process HTTP transport at the reserved origin
`http://railgrid-provider-relay.internal`. No TCP listener exists for that
origin. The relay endpoint is not registered on the public hub router. Streaming
and upgraded connections use the same in-process transport.

## External kcp

Use a dedicated mTLS listener reachable only by the shard. Configure:

```
--provider-relay-url=https://hub-relay.example.internal:9444
--provider-relay-listen-addr=:9444
--provider-relay-cert-file=/provider-relay/tls.crt
--provider-relay-key-file=/provider-relay/tls.key
--provider-relay-client-ca-file=/provider-relay/client-ca.crt
--provider-relay-allowed-names=kcp-shard
```

The URL must be an HTTPS origin without a path, query, or fragment. The serving
certificate must cover its hostname. Client certificates must chain to the
configured CA and have an explicitly allowed common name. The external kcp shard
must send that certificate using `--shard-client-cert-file` and
`--shard-client-key-file`; `--shard-virtual-workspace-ca-file` must trust the
relay serving certificate (and the other shard virtual-workspace endpoints). Do not place an ingress which terminates client TLS
in front of this listener. Ordinary browser authentication or X-Remote headers
are not accepted as proof of shard identity.

The hub Helm chart exposes this under `hub.providerRelay`: set `enabled`, `url`,
`port`, `secretName`, and `allowedShardNames`. The Secret contains `tls.crt`,
`tls.key`, and `client-ca.crt`. The chart refuses this option with embedded kcp.
The extra Service port is for the dedicated listener; operators should restrict
its network reachability to their shards.

## Local integration fixture

`TestBYOCustomSubresource` in the edges connectivity suite provisions a unique
kind cluster and an in-cluster agent, registers an authored provider, bootstraps
with the restricted credential, and invokes the real quickstart verb through
embedded kcp and the Edge. It also checks original caller identity despite
spoofed input headers, forbidden direct publication, and a foreign-org denial.
The provider process runs on the host, behind a Kubernetes Service Endpoint,
so its controller can use this suite's localhost kcp virtual-workspace URL. This
fixture does not verify a pod-hosted provider's external kcp DNS/TLS setup or the
external shard's mTLS configuration.

The pinned kcp dependency currently duplicates cluster-scoped quota entries
inside admission, causing conflicts and false quota exhaustion. The earlier local
experiment with a modified dependency is not a supported validation path. No
KCP fork or source modification is maintained here. See
[KCP upstream requirements](upstream-kcp-requirements.md) for the release blocker
and unmodified diagnostic. Count quotas also bound object counts rather than
aggregate bytes; operators still need storage and tenant admission budgets.

Build the agent image for the local Docker architecture and the quickstart
binary, then run the isolated fixture:

```
make build-quickstart-provider docker-build-agent VERSION=e2e-byo DOCKER_PLATFORM=linux/arm64
RAILGRID_E2E_BYO_RELAY=true make e2e-edges-connectivity E2E_FLAGS=-test.run=TestBYOCustomSubresource E2E_EDGES_CONN_TIMEOUT=20m
```

The fixture never reuses a kind cluster and passes its own kubeconfig to every
Kubernetes operation. It does not touch existing agents or kind clusters.
The affected upstream quota dependency currently prevents a successful complete
run; a passing run against an altered dependency is not production readiness.
