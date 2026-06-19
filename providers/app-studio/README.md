# app-studio provider

> [!IMPORTANT]
> **Read-only mirror — do not push or open PRs here.**
> The standalone [`faroshq/provider-app-studio`](https://github.com/faroshq/provider-app-studio)
> repository is **automatically synced** from the kedge monorepo
> [`faroshq/kedge`](https://github.com/faroshq/kedge) (path `providers/app-studio/`)
> via [splitsh-lite](https://github.com/splitsh/lite). Every sync force-updates
> the mirror, so any direct change here is overwritten. File issues and PRs
> against [`faroshq/kedge`](https://github.com/faroshq/kedge) instead.
> See [docs/provider-publishing.md](../../docs/provider-publishing.md) for how
> the mirror is published.

App Studio is a kedge provider that gives each tenant a **persistent AI project
workspace**: named Projects with durable "memory" (goals / requirements /
constraints) and a chat surface backed by the tenant's own LLM credentials,
with optional MCP tool use against their workspace. Projects are stored as
`projects.ai.kedge.faros.sh` resources in the tenant's own kcp workspace; chat
transcripts persist in the provider's message store (Postgres in production and
local dev, with explicit in-memory mode available only for throwaway UI work).

The provider acts **as the calling user**: the hub's backend proxy forwards
`/services/providers/app-studio/*` with the verified `X-Kedge-Tenant` /
`X-Kedge-User` headers and the caller's bearer token, and the provider builds a
per-request, token-scoped client (see `tenant/`). There is no provider
service-account escalation.

## What's here

| Surface | Where |
|---|---|
| Provider binary | `main.go` — loads the provider kubeconfig, opens the message store, mounts `/api` + the embedded portal, heartbeats the hub |
| REST / LLM / message API | `api/` — Project CRUD, memory, LLM settings, streaming chat (`/api/projects/*`) |
| API type | `apis/ai/v1alpha1/` — the `Project` CRD type (deepcopy generated) |
| Typed client | `client/` — trimmed dynamic client for the Project resource |
| Tenant client | `tenant/` — token-forwarding `ClientFactory` (host+TLS from the provider kubeconfig, caller token per request) |
| Message store | `store/` — Postgres + in-memory + envelope-encryption implementations |
| Portal | `portal/` — the Vue micro-frontend (`<kedge-provider-app-studio>`), embedded via `assets.go` |
| Registration | `manifest.yaml` — CatalogEntry + APIExport (`ai.kedge.faros.sh`) + Code provider dependency + the Project APIResourceSchema + `secrets` claim |
| Deploy | `deploy/chart/` — Helm chart (Deployment, Service, CatalogEntry) |
| CI (mirror) | `.github/workflows/{image,chart}.yaml` — publish the image + chart to GHCR (run only in the mirror) |

## Configuration

Environment variables consumed by the binary:

| Var | Purpose |
|---|---|
| `PORT` | Listen port (default `8081`) |
| `KEDGE_HUB_URL` | Hub base URL (heartbeat + MCP endpoint resolution) |
| `KEDGE_HUB_TOKEN` | Bearer token for the heartbeat |
| `KEDGE_PROVIDER_NAME` | CatalogEntry name (default `app-studio`) |
| `KEDGE_PROVIDER_KUBECONFIG` | Provider kubeconfig (kcp front-proxy host + TLS only) |
| `APP_STUDIO_DATABASE_URL` | Postgres DSN for the message store |
| `APP_STUDIO_IN_MEMORY_MESSAGE_STORE` | `true` → non-durable in-memory store (dev) |
| `APP_STUDIO_MESSAGE_ENCRYPTION_KEYS` | Comma-separated `key-id:base64-aes-key` entries |
| `APP_STUDIO_MESSAGE_RETENTION` | Retention window (`time.ParseDuration`, e.g. `720h`) |
| `APP_STUDIO_WORKSPACE_ROOT` | Filesystem root for App Studio project workspaces and local file tools |
| `APP_STUDIO_MCP_INSECURE_SKIP_TLS_VERIFY` | `true` → skip TLS verify on MCP calls (dev) |

## Local message history

`make run-provider-app-studio` starts/reuses a local Postgres container by
default and passes `APP_STUDIO_DATABASE_URL` to the provider:

```sh
make app-studio-db-up
make run-provider-app-studio
```

The database container is named `kedge-app-studio-postgres`, listens on
`127.0.0.1:55432`, and stores data under `.kcp/app-studio-postgres/`. Both
Tiltfiles expose it as the `app-studio-db` resource, so hard-refreshing the UI
or rebuilding the provider no longer drops prior conversation history.

To use your own database, set `APP_STUDIO_DATABASE_URL` in the environment or in
`providers/app-studio/.env` (copy from `.env.example`). To intentionally use the
old throwaway behavior, set `APP_STUDIO_IN_MEMORY_MESSAGE_STORE=true`.

## Local project files

App Studio keeps project files in its own workspace root so the assistant can
list, read, search, and safely mutate text files before asking provider-code to
commit selected changed files to git. Set `APP_STUDIO_WORKSPACE_ROOT` to choose
the directory; the binary defaults to a temp directory, while the Helm chart
mounts a persistent volume at `/var/lib/kedge-app-studio/workspaces`.

The assistant-facing workspace tools are App Studio local tools. Provider-code
remains the git-source boundary: `commit_project_files` reads selected workspace
files and delegates the actual commit to the Code provider's `code__commit_files`
tool.

## Runtime workers

App Studio does not run build, test, preview, or log commands inside the
provider pod. The assistant can recommend build and test checks from project
context, but App Studio no longer advertises runtime execution tools until a
tenant-isolated worker implementation is productized.
