# `cloud-credentials` Secret convention

The infrastructure provider brokers application templates from a
central kro cluster into kedge tenant workspaces. When you provision a
template, the provider reads a Secret named **`cloud-credentials`**
from your tenant workspace's `default` namespace and bridges it into a
per-instance Secret in the central cluster (named
`cloud-credentials-<instance>`) so the kro RGD template can mount it.

This document pins the per-cloud key names. **RGD authors writing
templates MUST reference these exact key names** — the broker passes
the Secret through verbatim, so a mismatch silently fails.

## Why this exists

There is no platform-level convention for tenant cloud credentials
prior to this provider. We define one here so multiple providers (kro,
future Crossplane-style ones, etc.) can share a single Secret without
re-prompting the user.

## AWS

| Key | Required | Notes |
|---|---|---|
| `aws_access_key_id` | yes | |
| `aws_secret_access_key` | yes | |
| `aws_session_token` | no | for STS / SSO short-lived creds |
| `aws_region` | yes | default region for the provisioned resources |

```sh
kubectl --context kedge-<tenant-slug> create secret generic cloud-credentials \
  --from-literal=aws_access_key_id=AKIA... \
  --from-literal=aws_secret_access_key=SECRET... \
  --from-literal=aws_region=us-east-1
```

## GCP

| Key | Required | Notes |
|---|---|---|
| `gcp_service_account_json` | yes | the full JSON key file, as a single value |
| `gcp_project_id` | yes | default project for the provisioned resources |
| `gcp_region` | no | default region |

```sh
kubectl create secret generic cloud-credentials \
  --from-file=gcp_service_account_json=./sa.json \
  --from-literal=gcp_project_id=my-project \
  --from-literal=gcp_region=us-central1
```

## Azure

| Key | Required | Notes |
|---|---|---|
| `azure_tenant_id` | yes | |
| `azure_client_id` | yes | service principal app ID |
| `azure_client_secret` | yes | |
| `azure_subscription_id` | yes | |

## Raw Kubernetes

Some templates target an external Kubernetes cluster directly (e.g. an
on-prem cluster). Use:

| Key | Required | Notes |
|---|---|---|
| `kubeconfig` | yes | the full kubeconfig YAML; the RGD references it as a kubectl context |

## Rotation

Rotate by updating the Secret in your tenant workspace. Newly-provisioned
instances pick up the rotated creds on the next provision. Already-
provisioned instances continue to use the credentials captured into
their `cloud-credentials-<instance>` sidecar Secret at create time —
the provider deliberately does NOT auto-refresh those, because RGD
templates may have cached the credentials further downstream.

## Why not store credentials in central kro directly?

We could, but then every tenant's credentials would land in one
Kubernetes cluster the platform admin operates. Keeping them in each
tenant's kcp workspace means:

- Tenants own the lifecycle (rotation, revocation).
- The platform admin never sees them in cleartext.
- Multi-tenancy is enforced by kcp's permission-claim model rather
  than namespace-label conventions in a shared cluster.
