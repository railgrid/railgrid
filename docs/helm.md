# Deploying Kedge Hub with Helm

This guide walks through deploying kedge-hub into a local [kind](https://kind.sigs.k8s.io/) cluster.

The kedge-hub chart deploys **kcp + kedge-hub** only. The identity provider (e.g., Dex) is deployed separately — see [idp.md](idp.md).

## Prerequisites

- [kind](https://kind.sigs.k8s.io/docs/user/quick-start/#installation)
- [kubectl](https://kubernetes.io/docs/tasks/tools/)
- [Helm](https://helm.sh/docs/intro/install/) v3+
- [Docker](https://docs.docker.com/get-docker/)
- A running OIDC identity provider (see [idp.md](idp.md))

## 1. Create a kind cluster

```bash
kind create cluster --name kedge
```

Verify it's running:

```bash
kubectl cluster-info --context kind-kedge
```

## 2. Build and load the hub image

The hub image needs to be built locally and loaded into the kind cluster (kind doesn't pull from registries by default).

```bash
make docker-build-hub
kind load docker-image ghcr.io/faroshq/kedge-hub:$(git describe --tags --always --dirty 2>/dev/null || echo dev) --name kedge
```

The kcp image is pulled from its public registry (`ghcr.io/kcp-dev/kcp`), so no extra loading is needed.

## 3. Create a values file

Create a `values-kind.yaml` with the required settings:

```yaml
hub:
  hubExternalURL: "https://hub.faros.sh"
  devMode: false

idp:
  issuerURL: "https://idp.faros.sh"
  clientID: "kedge"
  clientSecret: "<your-idp-client-secret>"
```

> **Note:** `hub.devMode: true` disables TLS verification for the OIDC issuer, which is necessary when the identity provider uses a self-signed certificate.

## 4. Install the chart

```bash
helm upgrade --install kedge deploy/charts/kedge-hub/ \
  -f hack/example/values-kind.yaml \
  --namespace kedge-system \
  --create-namespace
```

Note: If TLS certificate is loading too long via Cert-Manager (common on kind + macOS):
```bash
kubectl -n cert-manager patch deployment cert-manager --type=json \
  -p='[{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--dns01-recursive-nameservers=1.1.1.1:53,8.8.8.8:53"},{"op":"add","path":"/spec/template/spec/containers/0/args/-","value":"--dns01-recursive-nameservers-only"}]'
```

## 5. Wait for pods to be ready

```bash
kubectl -n kedge-system get pods -w
```

You should see one workload:
- `kedge-kedge-hub-0` — StatefulSet pod running kcp + hub containers

Wait until it's `Running` and all containers are ready. The hub container waits for kcp to generate `admin.kubeconfig` before starting, so it may take 30-60 seconds.

```bash
kubectl -n kedge-system wait --for=condition=ready pod -l app.kubernetes.io/name=kedge-hub --timeout=120s
```

## 6. Port-forward the hub

```bash
kubectl -n kedge-system port-forward svc/kedge-kedge-hub 8443:8443
```

## 7. Log in

```bash
kedge login --hub-url https://localhost:8443 --insecure-skip-tls-verify
```

This opens a browser for the OIDC login flow.

## Checking logs

```bash
# kcp container
kubectl -n kedge-system logs kedge-kedge-hub-0 -c kcp

# hub container
kubectl -n kedge-system logs kedge-kedge-hub-0 -c hub
```

## Upgrading

After making changes to values:

```bash
helm upgrade kedge deploy/charts/kedge-hub/ \
  -f values-kind.yaml \
  --namespace kedge-system
```

The TLS secret has `helm.sh/resource-policy: keep`, so it survives upgrades without being regenerated.

## Uninstalling

```bash
helm uninstall kedge --namespace kedge-system
```

This does **not** delete the PVCs (kcp data) or the kept TLS secret. To fully clean up:

```bash
kubectl -n kedge-system delete pvc --all
kubectl -n kedge-system delete secret kedge-kedge-hub-tls
kubectl delete namespace kedge-system
```

To also remove the kind cluster:

```bash
kind delete cluster --name kedge
```

## Production deployment

The biggest challenge when moving beyond local development is making the hub and identity provider reachable over the public internet. Both need stable, publicly accessible URLs because:

- The OIDC login flow redirects the user's browser to the identity provider, which must be reachable from outside the cluster.
- The hub's external URL is embedded in generated kubeconfigs that `kedge` CLI and agents use.
- Agents running on remote edge clusters need to reach the hub's tunnel endpoint.

See the companion guides for production-oriented setups:

- [Ingress with Cloudflare Tunnel](ingress.md) — expose the hub via a Cloudflare Tunnel without needing a public IP or LoadBalancer.
- [Identity Provider (Dex)](idp.md) — deploy Dex using the upstream Helm chart with persistent storage and a public ingress.

## Values reference

| Key | Description | Default |
|-----|-------------|---------|
| `hub.hubExternalURL` | **(required)** External URL of the hub for kubeconfig generation and OIDC callbacks | `""` |
| `hub.listenAddr` | Hub listen address | `":8443"` |
| `hub.devMode` | Skip TLS verification for OIDC issuer | `false` |
| `idp.issuerURL` | **(required)** OIDC identity provider issuer URL | `""` |
| `idp.clientID` | OIDC client ID (must match IDP client config) | `"kedge"` |
| `idp.clientSecret` | **(required)** OIDC client secret (must match IDP client config) | `""` |
| `hub.tls.existingSecret` | Name of existing TLS Secret (skips self-signed generation) | `""` |
| `hub.tls.selfSigned.enabled` | Generate a self-signed TLS certificate | `true` |
| `hub.tls.selfSigned.dnsNames` | Extra DNS SANs for the self-signed cert | `[]` |
| `hub.tls.selfSigned.ipAddresses` | IP SANs for the self-signed cert | `["127.0.0.1"]` |
| `hub.tls.certManager.enabled` | Use cert-manager to issue TLS certificate | `false` |
| `hub.tls.certManager.issuerRef.name` | cert-manager Issuer/ClusterIssuer name | `""` |
| `hub.tls.certManager.issuerRef.kind` | Issuer kind | `"ClusterIssuer"` |
| `hub.tls.certManager.dnsNames` | Additional DNS SANs for the certificate | `[]` |
| `persistence.size` | kcp data PVC size | `10Gi` |
| `persistence.storageClass` | Storage class for kcp PVC | `""` |
| `kcp.featureGates` | kcp feature gates | `"WorkspaceMounts=true"` |
| `kcp.extraArgs` | Additional kcp CLI arguments | `[]` |
| `service.type` | Service type for hub | `ClusterIP` |
| `ingress.enabled` | Enable Ingress for hub | `false` |
