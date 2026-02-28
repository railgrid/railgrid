# Milestone v1.1 Roadmap — SSH Key Injection (issue #72)

**4 phases** | **24 requirements** | Continues from Phase 6 → starts at Phase 7
GitHub issue: https://github.com/faroshq/kedge/issues/72

| # | Phase | Goal | Requirements | Branch |
|---|-------|------|--------------|--------|
| 7 | API: SSHKeySecretRef | CRD field exists, codegen clean | KEY-01–05 | `feat/ssh-key-secret-ref` |
| 8 | Hub: Secret loading | Hub reads key, uses PublicKeys auth | KEY-06–14 | `feat/ssh-key-hub` |
| 9 | RBAC | Hub SA can read referenced Secrets | KEY-15–17 | `feat/ssh-key-rbac` |
| 10 | Tests | Unit + e2e green | KEY-18–24 | `feat/ssh-key-tests` |

---

## Phase 7: API — SSHKeySecretRef field
**Goal:** `Server.Spec.SSHKeySecretRef` field exists in the CRD, codegen passes, schema is correct.

**Requirements:** KEY-01, KEY-02, KEY-03, KEY-04, KEY-05

**File changes:**
- `apis/kedge/v1alpha1/types_server.go` — add `SSHKeySecretRef *corev1.SecretReference` and `SSHKeySecretDataKey string` to `ServerSpec`
- `apis/kedge/v1alpha1/zz_generated.deepcopy.go` — regenerated
- CRD YAML in `config/crd/` or `deploy/crd/` — regenerated

**Success criteria:**
1. `kubectl apply -f deploy/crd/servers.yaml` succeeds — new fields present in OpenAPI schema
2. `kubectl patch server my-server --type=merge -p '{"spec":{"sshKeySecretRef":{"name":"my-key","namespace":"kedge-system"}}}'` accepted (no validation error)
3. `make generate && make verify-codegen` exits 0
4. `make manifests` exits 0
5. `Server.Spec.SSHKeySecretRef` field documented in CRD schema description (`+kubebuilder:validation:Optional`)

**Branch strategy:** PR to `ssh` — `feat/ssh-key-secret-ref`

**Dependencies:** Phase 1 complete (Server CRD exists)

---

## Phase 8: Hub — Secret loading & newSSHClient
**Goal:** Hub reads `sshKeySecretRef` at session time, fetches the private key from the Secret, and uses key-based auth to the agent sshd.

**Requirements:** KEY-06, KEY-07, KEY-08, KEY-09, KEY-10, KEY-11, KEY-12, KEY-13, KEY-14

**File changes:**
- `pkg/virtual/builder/proxy.go` — add `kubeClient kubernetes.Interface` to `virtualWorkspaces` struct
- `pkg/virtual/builder/build.go` — add `KubeClient kubernetes.Interface` to `VirtualWorkspaceConfig` + wire into `virtualWorkspaces`
- `pkg/virtual/builder/agent_proxy_builder.go`:
  - `buildAgentProxyHandler` — extract `resourceName` and pass to `sshHandler`
  - `sshHandler` signature: add `serverName string` parameter (or restructure call site)
  - Add `loadSSHKeyForServer(ctx, serverName, clusterName) (gossh.Signer, error)` method
  - `newSSHClient` — add `signer gossh.Signer` parameter; use `PublicKeys` when non-nil

**Key design: `loadSSHKeyForServer`**
```go
func (p *virtualWorkspaces) loadSSHKeyForServer(ctx context.Context, serverName, clusterName string) (gossh.Signer, error) {
    if p.kubeClient == nil {
        return nil, nil  // no k8s client configured, skip key lookup
    }
    server, err := p.kedgeClient.Servers().Get(ctx, serverName, metav1.GetOptions{})
    if err != nil || server.Spec.SSHKeySecretRef == nil {
        return nil, nil  // no ref set, use password fallback
    }
    ref := server.Spec.SSHKeySecretRef
    secret, err := p.kubeClient.CoreV1().Secrets(ref.Namespace).Get(ctx, ref.Name, metav1.GetOptions{})
    if err != nil {
        return nil, fmt.Errorf("fetching SSH key secret %s/%s: %w", ref.Namespace, ref.Name, err)
    }
    keyField := server.Spec.SSHKeySecretDataKey
    if keyField == "" {
        keyField = "ssh-privatekey"  // k8s convention
    }
    keyBytes, ok := secret.Data[keyField]
    if !ok || len(keyBytes) == 0 {
        return nil, fmt.Errorf("secret %s/%s has no field %q", ref.Namespace, ref.Name, keyField)
    }
    signer, err := gossh.ParsePrivateKey(keyBytes)
    if err != nil {
        return nil, fmt.Errorf("parsing SSH private key from secret %s/%s: %w", ref.Namespace, ref.Name, err)
    }
    return signer, nil
}
```

**Key design: `newSSHClient` change**
```go
func newSSHClient(_ context.Context, deviceConn net.Conn, sshUser string, signer gossh.Signer, _ klog.Logger) (*gossh.Client, error) {
    var authMethods []gossh.AuthMethod
    if signer != nil {
        authMethods = []gossh.AuthMethod{gossh.PublicKeys(signer)}
    } else {
        authMethods = []gossh.AuthMethod{gossh.Password("")}  // backward-compat
    }
    sshConfig := &gossh.ClientConfig{
        User:            sshUser,
        Auth:            authMethods,
        HostKeyCallback: gossh.InsecureIgnoreHostKey(), // tracked in #64
    }
    ...
}
```

**Success criteria:**
1. `kedge ssh my-server` with `sshKeySecretRef` set → connects using key-based auth (verify via sshd `AuthorizedKeysFile` matching only the public key)
2. `kedge ssh my-server` without `sshKeySecretRef` → still works (password fallback, existing e2e unaffected)
3. `sshKeySecretRef` referencing a non-existent Secret → CLI receives `HTTP 502`, hub logs error with secret ref
4. Invalid key bytes in Secret → CLI receives `HTTP 502`, hub logs parse error
5. `make build ./...` exits 0

**Branch strategy:** PR to `ssh` — `feat/ssh-key-hub`

**Dependencies:** Phase 7 (CRD field must exist)

---

## Phase 9: RBAC
**Goal:** Hub service account has the necessary RBAC to read Secrets referenced by `sshKeySecretRef`.

**Requirements:** KEY-15, KEY-16, KEY-17

**File changes:**
- `deploy/rbac/hub-role.yaml` (or equivalent) — add `secrets` `get` rule
- `deploy/rbac/hub-rolebinding.yaml` — bind hub SA to role
- `README.md` or `docs/server-mode.md` — note: Secrets must be accessible to hub SA

**Design notes:**
- Scope the role to only the namespace(s) where Secrets are expected (e.g., `kedge-system`), not cluster-wide, to follow least-privilege
- If the codebase uses ClusterRole, add a comment explaining the trade-off
- Consider documenting that operators can further scope using namespace-scoped Roles

**Success criteria:**
1. Hub pod starts with correct RBAC and can `kubectl auth can-i get secrets -n kedge-system --as system:serviceaccount:kedge-system:kedge-hub` = yes
2. Hub pod cannot get secrets in unrelated namespaces (Role, not ClusterRole, preferred)
3. `make lint` exits 0 on RBAC YAML (if linting is configured)
4. Documentation mentions RBAC requirement

**Branch strategy:** PR to `ssh` — `feat/ssh-key-rbac`

**Dependencies:** Phase 8 (hub code references the SA)

---

## Phase 10: Tests
**Goal:** Unit + e2e test coverage for the key injection path. All existing tests continue to pass.

**Requirements:** KEY-18, KEY-19, KEY-20, KEY-21, KEY-22, KEY-23, KEY-24

**New test files:**
- `pkg/virtual/builder/ssh_key_test.go` — unit tests for `loadSSHKeyForServer`
- `pkg/virtual/builder/agent_proxy_builder_test.go` (extend) — unit tests for `newSSHClient` auth method selection
- `test/e2e/cases/ssh_key.go` — e2e case for key injection flow
- `test/e2e/framework/` — helper to generate test RSA keypair and create Secret

**Unit test outline (`ssh_key_test.go`):**
```go
// TestLoadSSHKeyForServer_NoRef — nil sshKeySecretRef → nil signer, no k8s call
// TestLoadSSHKeyForServer_ValidKey — valid Secret with RSA key → valid signer returned
// TestLoadSSHKeyForServer_SecretNotFound — k8s returns NotFound → error returned
// TestLoadSSHKeyForServer_MissingDataKey — Secret found but key field absent → error
// TestLoadSSHKeyForServer_InvalidKeyBytes — key field present but not a valid PEM → error
// TestNewSSHClient_NilSigner — Auth contains Password("")
// TestNewSSHClient_WithSigner — Auth contains PublicKeys, no Password
```

**E2e test outline:**
```go
// TestSSHKeyInjection:
//   1. Generate RSA keypair in test
//   2. Create Secret in kedge-system with private key
//   3. Start test sshd configured to accept only the public key
//   4. Register Server with sshKeySecretRef pointing to the Secret
//   5. Run kedge ssh my-server -- hostname
//   6. Assert output == expected hostname
//   7. Assert existing e2e (no sshKeySecretRef) still passes
```

**Success criteria:**
1. `make test` exits 0 — all unit tests pass including new ones
2. E2e case `TestSSHKeyInjection` passes in CI
3. Existing SSH e2e tests continue to pass (no regression)
4. `make lint` exits 0
5. Test coverage for `loadSSHKeyForServer` ≥ 80% (all error paths exercised)

**Branch strategy:** PR to `ssh` — `feat/ssh-key-tests`

**Dependencies:** Phase 8 (hub code must be complete to test)

---

## Implementation Order

```
ssh (feature branch — all PRs target here)
  ├── feat/ssh-key-secret-ref   (Phase 7 — CRD field, codegen)
  ├── feat/ssh-key-hub          (Phase 8 — hub loads key, uses PublicKeys)
  ├── feat/ssh-key-rbac         (Phase 9 — RBAC for hub SA)
  └── feat/ssh-key-tests        (Phase 10 — unit + e2e)
```

Phases 7 → 8 → 9 are sequential (each depends on previous).
Phase 10 can start in parallel with Phase 9 (unit tests don't need RBAC).

---

## Risk Register

| Risk | Likelihood | Impact | Mitigation |
|------|------------|--------|------------|
| kcp/kubeconfig — hub may not have a standard k8s client for kcp workspaces | Medium | High | Check how existing RBAC reconciler accesses kcp; reuse same client pattern |
| `sshHandler` refactor (adding serverName param) breaks existing call sites | Low | Medium | Only one call site in `buildAgentProxyHandler`; straightforward refactor |
| Secret namespace mismatch (Server in workspace, Secret in different ns) | Medium | Medium | Document clearly; consider validation webhook (v2) |
| Passphrase-protected keys fail silently | Low | Low | `gossh.ParsePrivateKey` returns error; handled by KEY-13; document limitation |
| Existing e2e uses `Password("")` — may need sshd `PermitEmptyPasswords yes` | Low | High | Verify test sshd config; document that new e2e case uses `AuthorizedKeysFile` |

---

*Created: 2026-02-25 — issue #72 implementation plan*
