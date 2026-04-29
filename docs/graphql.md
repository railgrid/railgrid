# GraphQL Gateway

Kedge ships a GraphQL gateway that exposes kcp virtual workspace APIs as a GraphQL endpoint. It is built on top of [kubernetes-graphql-gateway](https://github.com/platform-mesh/kubernetes-graphql-gateway) and runs as a single binary alongside the hub.

## Architecture

```
kedge-graphql run
  ├── listener  – watches a kcp APIExportEndpointSlice, discovers workspace clusters,
  │               fetches their OpenAPI schemas and publishes them over gRPC
  └── gateway   – subscribes to schemas over gRPC, serves GraphQL at :8080
```

The two components communicate over an in-process gRPC connection (default `localhost:50051`). No schema files are written to disk.

## Starting the gateway (dev mode)

Make sure the hub is running first (`make run-hub-standalone` or similar), then:

```bash
make dev-run-graphql
```

This runs:

```bash
bin/kedge-graphql run \
  --kubeconfig=.kcp/admin.kubeconfig \
  --grpc-addr=localhost:50051 \
  --apiexport-endpoint-slice-name=core.faros.sh \
  --apiexport-endpoint-slice-logicalcluster=root:kedge:providers \
  --workspace-schema-kubeconfig-override=.kcp/admin.kubeconfig \
  --enable-playground \
  --gateway-port=8080
```

Key flags:

| Flag | Default | Description |
|------|---------|-------------|
| `--apiexport-endpoint-slice-name` | `core.faros.sh` | Which APIExportEndpointSlice to watch. Use `kedge.faros.sh` to expose Edge/Placement/VirtualWorkload resources. |
| `--apiexport-endpoint-slice-logicalcluster` | `root:kedge:providers` | Logical cluster path where that endpointslice lives. |
| `--workspace-schema-kubeconfig-override` | `.kcp/admin.kubeconfig` | Kubeconfig the gateway uses to proxy API calls. CA and credentials are extracted from it automatically. |
| `--grpc-addr` | `localhost:50051` | gRPC address shared between listener and gateway. |
| `--gateway-port` | `8080` | Port for the GraphQL HTTP server. |

To expose kedge CRDs (Edges, Placements, VirtualWorkloads) instead of kcp core resources:

```bash
make dev-run-graphql GRAPHQL_APIEXPORT_SLICE=kedge.faros.sh
```

## Playground

The playground URL is per-cluster. When the listener starts it logs the cluster ID:

```
INFO  Registered endpoint  {"cluster": "a08bvxnomis2m558"}
```

Open the playground at:

```
http://localhost:8080/clusters/<cluster-id>
```

Example: `http://localhost:8080/clusters/a08bvxnomis2m558`

Other endpoints:

| Path | Description |
|------|-------------|
| `GET /clusters/{cluster}` | GraphQL playground UI |
| `POST /clusters/{cluster}` | GraphQL query endpoint |
| `GET /healthz` | Health check |
| `GET /readyz` | Readiness check |
| `GET /metrics` | Prometheus metrics |

## Authentication

The gateway uses a two-layer auth model:

- **Discovery** (schema building, `/api`, `/apis` group discovery): uses the admin credentials extracted from `--workspace-schema-kubeconfig-override`. This happens once at startup and is needed to build the GraphQL schema.
- **Resource requests** (all GraphQL queries): the gateway forwards the `Authorization: Bearer <token>` header from the incoming HTTP request directly to kcp. If no token is provided, the gateway returns 401 Unauthorized.

So clients must supply their own bearer token:

```bash
curl -X POST http://localhost:8080/clusters/<cluster-id> \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer <your-kcp-token>" \
  -d '{"query":"{ v1 { Namespaces { items { metadata { name } } } } }"}'
```

To get your token from a kubeconfig:

```bash
kubectl config view --raw -o jsonpath='{.users[0].user.token}'
```

Or if using `kubectl kcp` with a workspace:

```bash
kubectl get --raw /api 2>/dev/null  # triggers token refresh
kubectl config view --raw -o jsonpath='{.users[?(@.name=="<context-user>")].user.token}'
```

In the GraphQL Playground, set the HTTP headers:

```json
{
  "Authorization": "Bearer <your-kcp-token>"
}
```

## Accessing via kubectl

The GraphQL endpoint is proxied through the hub at `/graphql`. This means you can use `kubectl` with your normal kedge kubeconfig to send GraphQL queries.

### Discover the cluster ID

```bash
kubectl get logicalclusters
# NAME                  URL
# a08bvxnomis2m558      https://127.0.0.1:6443/clusters/a08bvxnomis2m558
```

### Send a GraphQL query via kubectl

```bash
kubectl create --raw '/graphql/clusters/<cluster-id>' -f - <<'EOF'
{"query":"{ v1 { Namespaces { items { metadata { name } } } } }"}
EOF
```

Example with a real cluster ID:

```bash
kubectl create --raw '/graphql/clusters/a08bvxnomis2m558' -f - <<'EOF'
{"query":"{ v1 { Namespaces { items { metadata { name } } } } }"}
EOF
```

### Raw GET (no query body — returns schema error)

```bash
kubectl get --raw '/graphql/clusters/a08bvxnomis2m558'
# {"data":null,"errors":[{"message":"Must provide an operation."}]}
```

This confirms the endpoint is reachable. To run a real query use `kubectl create --raw` with a JSON body as shown above.

### Authentication

`kubectl` automatically includes the bearer token from your kubeconfig. The hub proxy forwards it to the GraphQL gateway, which then forwards it to kcp for RBAC enforcement. No extra headers are needed.

## Example queries

### Introspect available API groups

```graphql
{
  __schema {
    queryType {
      fields {
        name
      }
    }
  }
}
```

### List namespaces (core.faros.sh endpointslice)

```graphql
{
  v1 {
    Namespaces {
      items {
        metadata {
          name
          creationTimestamp
        }
      }
    }
  }
}
```

### List kcp APIExports

```graphql
{
  apis_kcp_io {
    v1alpha1 {
      APIExports {
        items {
          metadata {
            name
          }
          spec {
            permissionClaims {
              group
              resource
            }
          }
        }
      }
    }
  }
}
```

### List kcp workspaces

```graphql
{
  tenancy_kcp_io {
    v1alpha1 {
      Workspaces {
        items {
          metadata {
            name
          }
          status {
            phase
            url
          }
        }
      }
    }
  }
}
```

### List Edges (requires `--apiexport-endpoint-slice-name=kedge.faros.sh`)

```graphql
{
  kedge_faros_sh {
    v1alpha1 {
      Edges {
        items {
          metadata {
            name
            namespace
          }
          spec {
            type
          }
          status {
            phase
          }
        }
      }
    }
  }
}
```

### List Placements (requires `--apiexport-endpoint-slice-name=kedge.faros.sh`)

```graphql
{
  kedge_faros_sh {
    v1alpha1 {
      Placements {
        items {
          metadata {
            name
            namespace
          }
          spec {
            edgeSelector {
              matchLabels
            }
          }
          status {
            phase
          }
        }
      }
    }
  }
}
```

### Get a specific resource by name

```graphql
{
  v1 {
    Namespace(name: "default") {
      metadata {
        name
        uid
        creationTimestamp
        labels
        annotations
      }
      status {
        phase
      }
    }
  }
}
```

### Get raw YAML of a resource

Append `Yaml` to any resource type name:

```graphql
{
  v1 {
    NamespaceYaml(name: "default")
  }
}
```

## Naming conventions

The GraphQL schema reflects Kubernetes API group names with dots replaced by underscores:

| API group | GraphQL field |
|-----------|--------------|
| *(core)* | `v1` |
| `kedge.faros.sh` | `kedge_faros_sh` |
| `apis.kcp.io` | `apis_kcp_io` |
| `tenancy.kcp.io` | `tenancy_kcp_io` |

Resource names follow PascalCase. List queries use the plural form (`Edges`), single-resource queries use the singular (`Edge(name: "...")`). Append `Yaml` for the raw YAML representation.

## Makefile variables

| Variable | Default | Description |
|----------|---------|-------------|
| `GRAPHQL_APIEXPORT_SLICE` | `core.faros.sh` | APIExportEndpointSlice to watch |
| `GRAPHQL_APIEXPORT_LOGICAL_CLUSTER` | `root:kedge:providers` | Logical cluster of that endpointslice |
| `GRAPHQL_GRPC_ADDR` | `localhost:50051` | Internal gRPC address |
