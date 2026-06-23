# App Studio Sandbox Runtime

App Studio owns the live development runtime API for projects. It keeps the
Project-level capability contract, the file workspace, sync/restart/log/status
operations, signed preview URLs, and preview proxying in the App Studio backend.

Infrastructure owns the resource composition. The `sandbox-runner` Template
creates a `SandboxRunner` API through KRO and materializes the runtime
namespace, PVC, Deployment, Service, control Secret, and network policy.

The first runtime implementation is intentionally small:

- one infrastructure-backed `SandboxRunner` resource per App Studio Project
- one Kubernetes namespace per runner
- one PVC, Deployment, and Service per runner
- one runner HTTP service in the pod for sync, restart, logs, and preview

For local Tilt development, `sandbox-runner-image` builds and loads the default
runtime image with:

```bash
make load-sandbox-runner-image
```

## Current Security Caveats

This POC runs user-generated code in a Kubernetes pod and should be treated as a
development-only runtime. It does not yet provide a complete untrusted-code
sandbox.

Known limitations:

- no AppArmor or runtime-class hardening is configured
- runner images are not defaulted by App Studio; configure immutable digest
  references before creating `SandboxRunner` resources
- file sync is text-file oriented and skips binary or oversized App Studio files

Before promoting this beyond local/dev use, add explicit runtime isolation,
quota defaults, image provenance controls, and network policy hardening.

## Runtime kubeconfig RBAC

`APP_STUDIO_RUNTIME_KUBECONFIG` must be scoped to only the runtime data-plane
operations App Studio performs after validating deterministic `SandboxRunner`
refs. The credential should not be cluster-admin. A minimal role needs:

```yaml
rules:
  - apiGroups: [""]
    resources: ["secrets", "endpoints"]
    verbs: ["get"]
  - apiGroups: [""]
    resources: ["services/proxy"]
    verbs: ["get", "create"]
  - apiGroups: [""]
    resources: ["namespaces"]
    verbs: ["delete"]
```

## Capability Boundary

App Studio should only depend on these runtime capabilities:

- `sync`
- `restart`
- `logs`
- `status`
- signed project preview proxy

Infrastructure should only publish deterministic runtime refs for the
`SandboxRunner`: namespace, preview Service, control Service, and control Secret.
App Studio validates those refs against the runner name before using the runtime
kubeconfig, so forged status cannot redirect control-plane credentials to
arbitrary Services or Secrets.
