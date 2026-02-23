# Requirements — SSH via Hub (v1)

## v1 Requirements

### API & Resources (issue #49)
- [ ] **API-01**: `Server` CRD exists in `kedge.faros.sh/v1alpha1` with spec and status fields
- [ ] **API-02**: `Server.Status.Phase` reflects Connected / Disconnected / Unknown
- [ ] **API-03**: `Server.Status.LastHeartbeatTime` updated by agent heartbeat loop
- [ ] **API-04**: `Server.Status.HostKeyFingerprint` set by agent on first connect
- [ ] **API-05**: `Server.Spec.SSHUser` optional field for per-server username override
- [ ] **API-06**: `ServerLifecycleReconciler` marks stale servers Disconnected (mirrors LifecycleReconciler)
- [ ] **API-07**: `ServerRBACReconciler` provisions SA token + kubeconfig secret for agent

### Agent Server Mode (issue #50)
- [ ] **AGT-01**: `kedge-agent --mode=server` runs without k8s dependency
- [ ] **AGT-02**: Agent connects to hub, registers/creates `Server` resource
- [ ] **AGT-03**: Agent sends heartbeats to hub on 30s interval
- [ ] **AGT-04**: Agent reads host SSH key fingerprint and updates `Server.Status`
- [ ] **AGT-05**: Agent listens for inbound SSH tunnel streams from hub via revdial
- [ ] **AGT-06**: Systemd unit file template provided (`deploy/systemd/kedge-agent.service`)
- [ ] **AGT-07**: `kedge agent-server install` CLI command writes unit file and enables service

### Hub SSH Proxy (issue #51)
- [ ] **HUB-01**: Hub listens on configurable SSH proxy port (`--ssh-proxy-addr`)
- [ ] **HUB-02**: Hub accepts incoming SSH TCP connections
- [ ] **HUB-03**: Hub authenticates connections via static token (in SSH password field)
- [ ] **HUB-04**: Hub authenticates connections via OIDC (keyboard-interactive)
- [ ] **HUB-05**: Hub looks up target `Server` by name from SSH username/target field
- [ ] **HUB-06**: Hub opens stream to target agent via existing revdial reverse tunnel
- [ ] **HUB-07**: Hub pipes SSH TCP stream bidirectionally through tunnel (transparent proxy)
- [ ] **HUB-08**: Hub tears down cleanly when either side closes

### Agent SSH Forwarder (issue #52)
- [ ] **FWD-01**: Agent accepts inbound streams from hub revdial listener
- [ ] **FWD-02**: Agent dials `localhost:22` on receiving a stream
- [ ] **FWD-03**: Agent pipes stream to localhost:22 bidirectionally
- [ ] **FWD-04**: Agent reads SSH stream metadata header (username from hub)
- [ ] **FWD-05**: Configurable target addr (default `localhost:22`)
- [ ] **FWD-06**: Clean teardown when either side closes

### Auth — OIDC Identity Mapping (issue #53)
- [ ] **AUTH-01**: Hub maps OIDC `preferred_username` claim to SSH username by default
- [ ] **AUTH-02**: `--ssh-username-claim` flag on hub to configure which OIDC claim to use
- [ ] **AUTH-03**: `Server.Spec.SSHUser` overrides OIDC claim mapping for a specific server
- [ ] **AUTH-04**: Resolved username included in stream metadata header to agent

### CLI & UX (issue #54)
- [ ] **CLI-01**: `kedge ssh <server-name>` command exists
- [ ] **CLI-02**: `kedge ssh --proxy-stdio <server-name>` mode for ProxyCommand usage
- [ ] **CLI-03**: Host key fetched from `Server.Status.HostKeyFingerprint`, written to `~/.kedge/known_hosts`
- [ ] **CLI-04**: `kedge ssh --print-proxy-command <name>` outputs SSH config snippet
- [ ] **CLI-05**: Hub URL and token read from current kedge kubeconfig context

### Testing (issue #54)
- [ ] **TEST-01**: Unit tests for Server CRD reconcilers (lifecycle, RBAC)
- [ ] **TEST-02**: Unit tests for hub SSH proxy handler (mock revdial, mock SSH conn)
- [ ] **TEST-03**: Unit tests for OIDC claim mapping logic
- [ ] **TEST-04**: E2e suite `test/e2e/suites/server/` — hub + server agent + `kedge ssh hostname`
- [ ] **TEST-05**: E2e validates auth flow (OIDC via Dex → SSH session)

## v2 (Deferred)

- SSH CA (hub issues short-lived host + user certs)
- Port forwarding / SFTP / SCP
- Web terminal
- Windows server support
- Embedded sshd (no dependency on host sshd)
- Multi-hop / jump host support

## Out of Scope

- Replacing existing sshd on the host — v1 proxies to it, not replaces it
- Managing SSH authorized_keys — handled by host sshd/PAM
- Kubernetes-based SSH (use existing Site+proxy for k8s pods)

## Traceability

| REQ-ID | Phase |
|--------|-------|
| API-01 – API-07 | Phase 1 |
| AGT-01 – AGT-07 | Phase 2 |
| HUB-01 – HUB-08 | Phase 3 |
| FWD-01 – FWD-06 | Phase 3 |
| AUTH-01 – AUTH-04 | Phase 4 |
| CLI-01 – CLI-05 | Phase 5 |
| TEST-01 – TEST-05 | Phase 6 |
