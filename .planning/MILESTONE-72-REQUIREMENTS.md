# Milestone v1.1 Requirements — SSH Key Injection (issue #72)

## Problem

The hub currently authenticates to the agent's sshd using an empty password
(`gossh.Password("")`). This is a tracked placeholder (TODO #54). The feature
adds `Server.Spec.SSHKeySecretRef`, allowing the hub to load an SSH private
key from a Kubernetes Secret and use it for key-based auth to the agent sshd.

## Current Code Reference

`pkg/virtual/builder/agent_proxy_builder.go`, `newSSHClient`:
```go
// TODO(#54): replace with key-based auth loaded from a Secret on the Server resource.
Auth: []gossh.AuthMethod{gossh.Password("")},
```

## Architecture

```
kedge ssh my-server
  → CLI (unchanged: WebSocket to hub)
  → Hub sshHandler
      - looks up Server object by name       (NEW: needs k8s/kcp client)
      - reads spec.sshKeySecretRef            (NEW: new CRD field)
      - fetches Secret from kcp workspace     (NEW: secret lookup)
      - parses private key                    (NEW: gossh.ParsePrivateKey)
  → newSSHClient(conn, sshUser, privateKey)  (CHANGED: added key param)
      - Auth: gossh.PublicKeys(signer)        (CHANGED: was Password(""))
  → agent sshd on host
```

## v1 Requirements

### API — Server CRD extension
- [ ] **KEY-01**: `ServerSpec.SSHKeySecretRef` field added — type `*corev1.SecretReference` (namespace + name)
- [ ] **KEY-02**: `ServerSpec.SSHKeySecretRef` is optional — absence of field leaves current password-auth behaviour unchanged (zero-risk for existing servers)
- [ ] **KEY-03**: CRD `spec.sshKeySecretRef.name` and `spec.sshKeySecretRef.namespace` map to standard `k8s.io/api/core/v1.SecretReference`
- [ ] **KEY-04**: DeepCopy generated (`make generate`) — `zz_generated.deepcopy.go` updated
- [ ] **KEY-05**: CRD YAML regenerated (`make manifests`) — new field appears in CRD OpenAPI schema

### Hub — Secret loading
- [ ] **KEY-06**: `virtualWorkspaces` struct gains an optional `kubeClient kubernetes.Interface` field for reading Secrets
- [ ] **KEY-07**: `VirtualWorkspaceConfig` / `BuildVirtualWorkspaces` accept a `KubeClient kubernetes.Interface` parameter (nil = no key injection, current behaviour preserved)
- [ ] **KEY-08**: `sshHandler` receives `serverName string` and `clusterName string` so it can look up the Server object (currently it only receives the connman key — refactor needed)
- [ ] **KEY-09**: `sshHandler` calls a new `loadSSHKeyForServer(ctx, serverName, clusterName)` helper that: (a) GETs the Server object, (b) returns nil signer if `sshKeySecretRef` is unset, (c) fetches the Secret, (d) parses the private key from `secret.Data["ssh-privatekey"]`, (e) returns `gossh.Signer`
- [ ] **KEY-10**: `newSSHClient` accepts an optional `signer gossh.Signer` parameter — when non-nil, uses `gossh.PublicKeys(signer)` as the sole auth method; when nil, falls back to `gossh.Password("")` (backward-compat)
- [ ] **KEY-11**: Secret field key is configurable per-server via `Server.Spec.SSHKeySecretDataKey` (default `"ssh-privatekey"` matching Kubernetes convention); field is optional string, empty means default

### Hub — Error handling
- [ ] **KEY-12**: If `sshKeySecretRef` is set but Secret is not found: `sshHandler` returns HTTP 502 with logged error (do not fall back silently to password-auth — operator misconfiguration must be visible)
- [ ] **KEY-13**: If Secret is found but key parsing fails: `sshHandler` returns HTTP 502 with logged error
- [ ] **KEY-14**: Secret lookup errors are logged at `klog.V(2)` with server name and secret ref for observability

### RBAC
- [ ] **KEY-15**: Hub ClusterRole (or Role) grants `get` on `secrets` scoped to the namespace(s) holding Server-referenced Secrets
- [ ] **KEY-16**: RBAC manifests added to `deploy/rbac/` (or equivalent) for the hub service account
- [ ] **KEY-17**: Documentation note: Secrets referenced by `sshKeySecretRef` must be in the same kcp workspace/namespace that the hub service account has access to

### Tests — Unit
- [ ] **KEY-18**: Unit test: `loadSSHKeyForServer` returns nil signer when `sshKeySecretRef` is unset (no k8s call made)
- [ ] **KEY-19**: Unit test: `loadSSHKeyForServer` returns valid signer when Secret exists with valid RSA private key
- [ ] **KEY-20**: Unit test: `loadSSHKeyForServer` returns error when Secret is not found
- [ ] **KEY-21**: Unit test: `loadSSHKeyForServer` returns error when Secret data key is missing or key bytes are invalid
- [ ] **KEY-22**: Unit test: `newSSHClient` — when signer nil, Auth is `[Password("")]`; when signer non-nil, Auth is `[PublicKeys(signer)]`

### Tests — E2e
- [ ] **KEY-23**: E2e: register `Server` with `sshKeySecretRef` pointing to a valid Secret containing the test sshd's accepted key → `kedge ssh <name> hostname` returns correct hostname
- [ ] **KEY-24**: E2e: `Server` without `sshKeySecretRef` still works (backward-compat, existing e2e suite must continue to pass)

## Out of Scope (this milestone)

- Passphrase-protected private keys (v2)
- Rotating/refreshing the key without restart (v2 — hub reloads per session so already handled)
- SSH certificate auth (separate issue, requires SSH CA)
- Surfacing key fingerprint in `Server.Status` (separate UX improvement)
- CLI changes — all auth is hub→agent, CLI is unchanged

## Traceability

| REQ-ID | Phase |
|--------|-------|
| KEY-01 – KEY-05 | Phase 7: API — SSHKeySecretRef field |
| KEY-06 – KEY-14 | Phase 8: Hub — secret loading + newSSHClient |
| KEY-15 – KEY-17 | Phase 9: RBAC manifests |
| KEY-18 – KEY-24 | Phase 10: Tests |
