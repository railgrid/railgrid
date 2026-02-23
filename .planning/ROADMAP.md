# Roadmap — SSH via Hub (feature branch: `ssh`)

**6 phases** | **30 requirements** | Branch: `ssh` → PRs target `ssh` (not `main`)

| # | Phase | Goal | Issues | Requirements |
|---|-------|------|--------|--------------|
| 1 | Server CRD & API | `Server` resource type exists, reconcilers running | #49 | API-01–07 |
| 2 | Agent Server Mode | `kedge-agent --mode=server` connects, heartbeats, systemd unit | #50 | AGT-01–07 |
| 3 | SSH Tunnel Core | Hub accepts SSH, agent forwards to localhost:22, end-to-end pipe works | #51 #52 | HUB-01–08, FWD-01–06 |
| 4 | Auth Integration | OIDC identity → SSH username mapping, username in stream header | #53 | AUTH-01–04 |
| 5 | CLI UX | `kedge ssh` command, ProxyCommand mode, known_hosts | #54 | CLI-01–05 |
| 6 | E2e & Polish | Full e2e suite green, unit tests, docs | #54 | TEST-01–05 |

---

## Phase 1: Server CRD & API
**Goal:** `Server` resource type exists in kedge API, lifecycle + RBAC reconcilers running, codegen complete.

**Requirements:** API-01, API-02, API-03, API-04, API-05, API-06, API-07

**Success criteria:**
1. `kubectl get servers` works against hub kcp workspace
2. Creating a `Server` resource triggers RBAC reconciler — SA token + kubeconfig secret appear in `kedge-system`
3. Setting `server.status.lastHeartbeatTime` to 6min ago causes `ServerLifecycleReconciler` to set phase=Disconnected
4. Unit tests for both reconcilers pass
5. `make generate` / `make verify-codegen` clean

**Dependencies:** None

**Branch strategy:** PR to `ssh` branch — `feat/server-crd`

---

## Phase 2: Agent Server Mode
**Goal:** `kedge-agent --mode=server` binary runs on a VM, connects to hub, registers as `Server`, sends heartbeats. Systemd unit provided.

**Requirements:** AGT-01, AGT-02, AGT-03, AGT-04, AGT-05, AGT-06, AGT-07

**Success criteria:**
1. Running `kedge-agent --mode=server --hub-url=... --token=... --server-name=...` registers a `Server` object in the hub
2. `Server.Status.Phase` = Connected while agent is running
3. Stopping agent → after 5min, phase = Disconnected
4. `Server.Status.HostKeyFingerprint` populated with host's actual SSH key fingerprint
5. `kedge agent-server install` writes valid systemd unit file
6. Agent binary has no k8s client-go import (verify with `go mod graph`)

**Dependencies:** Phase 1 (Server CRD must exist)

**Branch strategy:** PR to `ssh` branch — `feat/agent-server-mode`

---

## Phase 3: SSH Tunnel Core
**Goal:** Hub accepts an SSH TCP connection and transparently pipes it to the agent which forwards to localhost:22. Raw SSH handshake works end-to-end (no auth layer yet — use static token).

**Requirements:** HUB-01, HUB-02, HUB-03, HUB-05, HUB-06, HUB-07, HUB-08, FWD-01, FWD-02, FWD-03, FWD-05, FWD-06

**Success criteria:**
1. `ssh -o ProxyCommand="kedge ssh --proxy-stdio my-server" user@my-server` opens a shell
2. Hub `--ssh-proxy-addr` flag works (default `:2222`)
3. Disconnecting cleanly (exit) tears down both sides without goroutine leak
4. Hub proxy handles concurrent SSH connections to the same server
5. Unit tests for hub SSH handler with mock revdial stream pass

**Dependencies:** Phase 2 (agent must be connectable)

**Branch strategy:** PRs to `ssh` branch — `feat/hub-ssh-proxy` + `feat/agent-ssh-forwarder` (can parallelize)

---

## Phase 4: Auth Integration
**Goal:** OIDC identity maps to SSH username. Username flows from hub → stream metadata → agent → localhost:22 connection.

**Requirements:** HUB-04, AUTH-01, AUTH-02, AUTH-03, AUTH-04

**Success criteria:**
1. User authenticated via OIDC keyboard-interactive in SSH client
2. Hub extracts `preferred_username` from OIDC token, passes in stream header
3. `Server.Spec.SSHUser = "ubuntu"` overrides OIDC username for that server
4. Wrong OIDC token → SSH connection rejected at hub (not forwarded)
5. Unit tests for claim mapping with multiple claim types

**Dependencies:** Phase 3 (tunnel core must work)

**Branch strategy:** PR to `ssh` branch — `feat/ssh-auth`

---

## Phase 5: CLI UX
**Goal:** `kedge ssh my-server` just works. Host key verification. SSH config snippet generation.

**Requirements:** CLI-01, CLI-02, CLI-03, CLI-04, CLI-05

**Success criteria:**
1. `kedge ssh my-server` opens a shell (no manual ProxyCommand setup)
2. `kedge ssh my-server -- -L 8080:localhost:8080` works (SSH flags passthrough)
3. Host key from `Server.Status.HostKeyFingerprint` verified against `~/.kedge/known_hosts`
4. Changed host key → warning (not silent accept)
5. `kedge ssh --print-proxy-command my-server` outputs valid `~/.ssh/config` snippet

**Dependencies:** Phase 4 (auth must work)

**Branch strategy:** PR to `ssh` branch — `feat/kedge-ssh-cli`

---

## Phase 6: E2e & Polish
**Goal:** Full e2e test suite green. All unit tests pass. README section added.

**Requirements:** TEST-01, TEST-02, TEST-03, TEST-04, TEST-05

**Success criteria:**
1. `test/e2e/suites/server/` suite passes in CI (kind + systemd agent in container)
2. E2e validates OIDC auth → `kedge ssh` → `hostname` output correct
3. All unit tests pass (`make test`)
4. `make lint` clean
5. README has "Server Mode" section with quickstart

**Dependencies:** Phase 5

**Branch strategy:** PR to `ssh` branch — `feat/server-e2e`

---

## Branch Strategy

```
main (untouched)
  └── ssh (feature branch — all PRs target here)
        ├── feat/server-crd          (Phase 1)
        ├── feat/agent-server-mode   (Phase 2)
        ├── feat/hub-ssh-proxy       (Phase 3a — parallel)
        ├── feat/agent-ssh-forwarder (Phase 3b — parallel)
        ├── feat/ssh-auth            (Phase 4)
        ├── feat/kedge-ssh-cli       (Phase 5)
        └── feat/server-e2e          (Phase 6)
```

When the feature is stable and tested: single PR from `ssh` → `main`.
