# faros

faros is an open-source control plane for platform teams.

Providers publish Kubernetes-style APIs, versioned actions and MCP tools into isolated tenant workspaces. Users, teams and organizations reach them through one portal, one CLI, one API and one MCP endpoint, and every call is authorized as the caller by the same RBAC. Edges extend the control plane to clusters and servers behind NAT through outbound tunnels, so the same workspace that holds an application also reaches the cluster it runs on.

> **Status: alpha.** faros is at v0.1.x, every API is `v1alpha1`, and it is developed by a small team. There is no hosted service; you run the hub yourself. Expect breaking changes between minor versions until the APIs stabilize.

## What faros gives you

- **Tenancy.** Organizations, teams and users each get an isolated workspace with its own API surface. Membership and roles are first-party APIs. Authenticate with any OIDC provider or, for a single user, a static token.
- **Providers.** Helm-installed extensions that bring an APIExport, controllers, a backend, a portal micro-frontend, MCP tools and actions. Tenants enable a provider in a workspace and get its APIs bound there. Organizations can also register providers they run themselves, reached over an edge ([BYO providers](docs/byo-providers.md)).
- **Provider actions.** Versioned verbs on resources, granted through the workspace's RBAC, callable by people and by agents ([design](docs/provider-actions.md)).
- **One MCP endpoint per workspace.** Tools from every enabled provider and every connected edge, aggregated into one Model Context Protocol server. Each tool call runs as the calling user ([architecture](docs/mcp-architecture.md)).
- **Edges.** Kubernetes clusters and Linux servers join through an agent that dials out. The hub proxies `kubectl`, SSH and selected in-cluster services to them.
- **A portal.** One web UI that hosts each provider's micro-frontend under the tenant's identity.

## Providers in this repository

| Provider | What it does |
|---|---|
| [edges](providers/edges) | Connectivity: `KubernetesCluster` and `LinuxServer` edges, the agent tunnel, `Service` connectors, per-edge MCP |
| [infrastructure](providers/infrastructure) | Brokers [kro](https://github.com/kro-run/kro) application templates into tenant workspaces and runs them on a runtime cluster |
| [app-studio](providers/app-studio) | Persistent AI project workspaces with a chat assistant, sandboxed development instances and publishing, on the tenant's own model credentials |
| [agents](providers/agents) | Long-running personal agents with scheduled runs, tool use, approvals, budgets and memory, reachable from Slack, Telegram, Discord and email |
| [code](providers/code) | Source repositories, deploy keys and collaborators as workspace resources, on GitHub today |
| [databricks](providers/databricks) | Databricks connections, warehouses and tables as workspace resources, with a `query_table` action |
| [kuery](providers/kuery) | Fleet-wide object search and relationship traversal across a workspace's connected clusters |
| [quickstart](providers/quickstart) | A minimal reference provider that exercises the whole plugin surface |

Provider directories are mirrored read-only to `faroshq/provider-*` repositories. Open changes here.

## How it fits together

```
                 people · CLI · portal · AI agents (MCP)
                                 │
                        ┌────────▼────────┐
                        │    faros hub    │   workspaces, OIDC, RBAC,
                        │                 │   provider registry, proxies
                        └──┬─────┬─────┬──┘
           provider APIs   │     │     │   outbound tunnels
      ┌────────────────────┘     │     └────────────────────┐
┌─────▼──────┐          ┌────────▼───────┐           ┌──────▼──────┐
│  platform  │          │   org-owned    │           │    edges    │
│  providers │          │   providers    │           │ clusters &  │
│ (in-cluster│          │ (your cluster, │           │   servers   │
│  with hub) │          │  over an edge) │           │ behind NAT  │
└────────────┘          └────────────────┘           └─────────────┘
```

The hub is the only component that needs to be reachable. Providers register with the hub and serve their APIs inside their own workspace; agents on edges connect outward. Traffic between the hub and everything else is HTTP/1.1 and WebSockets, so any reverse proxy, ingress or tunnel in front of the hub works.

## Install

### Hub

```bash
helm install faros-hub oci://ghcr.io/faroshq/charts/faros-hub \
  --namespace faros --create-namespace \
  --set hub.hubExternalURL=https://faros.example.com
```

That is a complete single hub; its control-plane store runs inside the same release. For TLS, OIDC, ingress and the provider hardening flags, see [Helm deployment](https://faroshq.github.io/faros/helm.html). Larger installs can run the control-plane store as separate shards: [single hub](https://faroshq.github.io/faros/install-embedded-kcp.html), [multi-shard](https://faroshq.github.io/faros/install-external-kcp.html).

To try faros on a laptop, the CLI can create a local hub in a kind cluster:

```bash
faros dev init
```

### CLI

Download a binary from the [releases page](https://github.com/faroshq/faros/releases), or:

```bash
# krew
kubectl krew index add faros https://github.com/faroshq/krew-index.git
kubectl krew install faros/faros

# from source
go install github.com/faroshq/faros/cmd/faros@latest
```

## Quickstart

### 1. Log in and pick a workspace

```bash
faros login --hub-url https://faros.example.com          # OIDC in the browser
faros login --hub-url https://faros.example.com --token <static-token>
faros use                                                 # choose organization and workspace
```

`--hub-url` can also come from `FAROS_HUB_URL`.

### 2. Connect a cluster

```bash
faros edge create my-cluster --type kubernetes
faros edge join-command my-cluster        # prints the agent install command with a one-time token
```

Run the printed command on the target cluster. Then:

```bash
faros edge list
faros kubeconfig edge my-cluster > kc.yaml
kubectl --kubeconfig kc.yaml get nodes
```

### 3. Connect a server

```bash
faros edge create my-server --type server
faros edge join-command my-server
faros ssh my-server -- uptime
```

### 4. Give an AI agent your workspace

```bash
faros mcp url --mcpserver-name default
```

This prints the workspace's MCP endpoint and ready-to-paste configuration, with its long-lived token, for Claude Code, Claude Desktop and Codex. The endpoint carries the tools of every enabled provider and every connected edge, and each call is authorized as you.

## Security

The hub authenticates users with OIDC or a static token, and providers with their own workspace service-account token. Org-owned providers never see a user's bearer: they receive a short-lived, workspace-bound delegated token. Agents refuse to proxy to link-local and metadata addresses and can be locked to an allow-list. Webhooks into the agents provider are signature-checked. Details are in [Security](https://faroshq.github.io/faros/security.html).

Some hardened behaviours ship off by default for one release so existing installs can roll forward; the Helm doc's section "Turning on the hardened defaults early" lists the values that turn them on.

To report a vulnerability, open a private security advisory on this repository rather than a public issue.

## CLI reference

The full, generated reference is in [docs/cli](docs/cli/README.md) (one page per
command, regenerated with `make docs-cli`). `faros --help` groups the commands the
same way; `faros completion --help` sets up shell completion.

| Command | What it does |
|---|---|
| `faros login --hub-url <hub> [--token t] [-i]` | Log in (browser OIDC with silent refresh, or a static token); writes the `faros` kubeconfig context |
| `faros logout` | Forget the cached tokens and the faros contexts on this machine |
| `faros use [--org O --workspace W]` | Switch the active organization and workspace (interactive picker without flags) |
| `faros whoami [-o json]` | Hub, identity, token state, org/workspace with your roles, and what kubectl points at |
| `faros token [--refresh]` | Print a current bearer for curl; `--refresh` forces the OIDC refresh grant |
| `faros edge create\|list\|get\|delete <name>` | Manage edges; `list`/`get` take `-o wide\|json\|yaml\|name` |
| `faros edge join-command\|upgrade <name>` | Print the agent join command or upgrade instructions |
| `faros edge kubeconfig <name> [-o file\|--merge]` | Kubeconfig for a cluster edge through the hub, standalone or merged as context `faros-<name>` |
| `faros connect [<edge>]` / `faros disconnect` | Point kubectl at a cluster edge and back at the hub workspace |
| `faros ssh <name> [-- cmd]` | Open a shell or run a command on a server edge |
| `faros org list\|create\|members …` | Organizations you belong to; `members list\|add\|remove\|set-role` manage access |
| `faros workspace list\|create\|members …` | Workspaces of an organization and their members |
| `faros mcp url --mcpserver-name <name>` / `--edge <name>` | Print the workspace or per-edge MCP endpoint |
| `faros env [--json] [--no-mcp]` | Print `HUB`, `CLUSTER`, `ORG`, `WS`, `TOKEN`, `AS`, `MCP_URL`, `MCP_TOKEN` as shell exports |
| `faros app list\|create\|status\|sync\|promote\|publish` | Manage App Studio projects |
| `faros commit <repositoryRef> [--branch main] [--dry-run]` | Record local git commits through faros (`code__commit_files`) |
| `faros sandbox sync\|exec\|logs\|restart\|status <instance> [component]` | Drive a development-mode instance through the data plane |
| `faros agent join\|run\|install\|uninstall\|upgrade`, `faros install` | Run or install the agent on a cluster or host |
| `faros dev init\|update\|delete` | Manage a local kind-based environment |

Hidden but still accepted: `faros list`/`ls` (edge list), `faros kubeconfig edge`,
`faros get`, `faros apply`, `faros get-token` (the kubectl exec plugin) and
`faros kcp-workspace` (raw kcp workspace navigation).

### Building an app from a terminal or an AI agent

These commands call the hub REST and MCP APIs as you, resolving the org and
workspace UUIDs from the kubeconfig's `faros` context. Every one takes
`--org` / `--workspace` (display name or UUID) to target another workspace, and
the `app` and `sandbox` commands take `-o json` for raw output.

```bash
eval "$(faros env)"                          # exports for curl and scripts
faros app create shop --template application --display-name Shop --wait
faros app status shop                        # repository ref, commits, dev URL, promotion, publishing
gh repo clone <owner>/<repository ref> shop && cd shop
faros sandbox sync shop-dev api ./api        # authoritative sync of api/ into component "api"
faros sandbox exec shop-dev api -- node -e 'console.log(1)'   # exits with the command's exit code
faros sandbox logs shop-dev api -f
git add -A && git commit -m "Add cart"       # commit locally, never push
faros commit <repository ref>                # faros records the commit; your branch is reset onto it
faros app promote shop --hostname-prefix shop   # the prefix is locked after the first promote
faros app publish shop --mode public         # or restricted, or private
```

`faros commit` refuses to run when the working tree is dirty or
`origin/<branch>` has moved past your base. `faros commit` and `faros sandbox
sync` send binary files base64-encoded (at most 25 MiB each, 48 MiB per commit
or sync) when the hub's code provider or the component's dev agent supports
it. Otherwise `commit` refuses a change that includes binaries and `sync`
skips them with a warning. `exec` needs a prior sync.

## Repository layout

| Path | Contents |
|---|---|
| `cmd/` | `faros` CLI (which also runs the agent through `faros agent`), `faros-hub`, and the release helper |
| `pkg/hub` | Hub: control-plane bootstrap, tenancy, provider registry, proxies, MCP aggregation |
| `pkg/agent` | Edge agent and tunnel |
| `providers/` | The providers listed above, each its own Go module |
| `deploy/charts` | Helm charts for the hub and the agent |
| `docs/` | Published docs and design documents |

## Documentation

Published: [Getting started](https://faroshq.github.io/faros/getting-started.html) · [Helm deployment](https://faroshq.github.io/faros/helm.html) · [Security](https://faroshq.github.io/faros/security.html) · [Ingress](https://faroshq.github.io/faros/ingress/) · [MCP architecture](https://faroshq.github.io/faros/mcp-architecture.html) · [Developer guide](https://faroshq.github.io/faros/developers.html)

Design documents in this repository: [providers](docs/providers.md), [organizations and the workspace tree](docs/organizations.md), [provider scoping](docs/provider-scoping.md), [provider actions](docs/provider-actions.md), [BYO providers](docs/byo-providers.md), [MCP architecture](docs/mcp-architecture.md).

## Under the hood

Workspaces are served by [kcp](https://github.com/kcp-dev/kcp), which gives every tenant a Kubernetes-style API server without a cluster per tenant; providers publish their APIs into it and tenants bind them. You meet this as an operator when you size and back up the hub, and as a provider author when you write one. Users see the portal, the CLI, the API and MCP.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for building, running the local stack, tests and the pull-request workflow.

## License

Apache 2.0. See [LICENSE](LICENSE).
