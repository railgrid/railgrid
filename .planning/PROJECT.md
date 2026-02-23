# SSH via Hub WebSocket Reverse Tunnel

## What This Is

Add a second mode of operation to kedge alongside the existing k8s/Site model: **server mode**. A `kedge-agent` running as a systemd service on a bare-metal or VM maintains a persistent reverse WebSocket tunnel to the hub. Users can SSH to any registered server through the hub using standard openssh, with auth via OIDC — no VPN, no firewall rules, no open ports on the server.

## Problem

Kedge currently only manages k8s cluster sites. Bare-metal servers, VMs, and systemd-managed nodes have no kedge equivalent. Engineers still need direct SSH access (VPN, bastion, open ports) to reach these machines.

## Solution

```
User
  → SSH client (standard openssh)
  → Hub SSH proxy (new — accepts TCP, authenticates via OIDC)
  → WebSocket reverse tunnel (existing revdial infrastructure)
  → Agent (systemd mode on server — new)
  → localhost:22 (existing sshd on host)
```

The reverse tunnel is already built. This feature adds the SSH protocol layer on top.

## Core Value

**Zero-config SSH to any server, through the hub, using existing credentials.**

## Key Decisions

| Decision | Rationale | Outcome |
|----------|-----------|---------|
| Proxy to existing sshd (not embedded) | Simpler, no change to host auth (PAM, authorized_keys stays) | Use existing sshd |
| Feature branch `ssh`, not main | Experimental — test the architecture before merging to main | Branch: `ssh` |
| New `Server` CRD alongside `Site` | Servers ≠ k8s clusters — different lifecycle, status, capabilities | Separate resource type |
| OIDC identity → SSH username via claim mapping | Users already have OIDC identity; no separate SSH key management | `preferred_username` claim default |
| Per-server `spec.sshUser` override | Cloud VMs have fixed users (ubuntu, ec2-user) — needs override | Optional field on Server spec |
| `kedge ssh <name>` via ProxyCommand | Wraps openssh — no SSH client fork needed | ProxyCommand pattern |

## Constraints

- Go codebase (`golang.org/x/crypto/ssh` for SSH handling)
- Must not touch `main` branch — all work merged to `ssh` feature branch
- Agent systemd mode must not import k8s libraries (keep binary lean)
- Hub SSH proxy must not terminate SSH — transparent TCP proxy only
- v1: proxy to existing sshd only (no embedded sshd, no SSH CA, no port forwarding)

## Out of Scope (v1)

- Embedded sshd (no dependency on host sshd)
- SSH certificate CA (hub as CA, short-lived certs)
- SSH port forwarding / SFTP / SCP
- Web terminal
- Multi-hop / jump hosts
- Windows servers

---
*Last updated: 2026-02-22 after initialization*
