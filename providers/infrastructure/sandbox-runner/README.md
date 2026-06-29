# kedge-sandbox-runner

The container image that runs inside every **SandboxRunner** pod — an App Studio
*live development environment*. It is owned by the **infrastructure provider**,
alongside the [`sandbox-runner` template](../install/templates/sandbox-runner.yaml)
whose `spec.runnerImage` defaults to it (`ghcr.io/faroshq/kedge-sandbox-runner:latest`).

## What it is

A single, dependency-free Go program (`main.go`, standard library only) that acts
as the pod's entrypoint and **control plane for one workspace**. It lets the
platform sync code, restart the app, and read logs **without** shelling into the
pod or handing anyone a runtime-cluster credential.

The image is built `FROM node:22-bookworm` (plus `git` + `ca-certificates`)
because the supervised dev process typically runs `npm` / `vite`. The Go binary
is the `ENTRYPOINT`; the user's app is a child process it manages.

```
┌─ SandboxRunner pod ────────────────────────────────────────┐
│  sandbox-runner (Go)            user dev process            │
│  ── control API :7070 ──┐       ── npm run dev :3000 ──┐    │
│   /healthz /sync         │ start/stop/restart           │   │
│   /restart /logs         └──────────► (child)  ◄── logs ┘   │
└──────────▲─────────────────────────────────────────────────┘
           │ reverse-proxied by the infra provider data-plane
           │ (caller's logs/sync/restart, control token injected)
```

## What the Go app does

1. **Supervises the dev process.** Runs `SANDBOX_START_COMMAND` (e.g.
   `npm install && npm run dev`) in the workspace, streams its stdout/stderr into
   a 500-line in-memory ring buffer, and can stop/start it on demand.

2. **Serves an HTTP control API on `:7070`:**

   | Method & path | Auth | Purpose |
   |---|---|---|
   | `GET /healthz` | none | Liveness probe. |
   | `POST /sync` | token | Write/delete workspace files, optionally restart. Body: `{files:[{path,content}], deletePaths:[], restart:"auto"\|"always"\|""}`. |
   | `POST /restart` | token | Stop + start the dev process. |
   | `GET /logs` | token | Buffered dev-process output (`text/plain`). |

   `restart:"auto"` restarts **only** when a startup-affecting file actually
   changed (`package.json`, a lockfile, `vite.config.*`, `server.js`) or the
   process isn't currently running — so editing a component doesn't bounce the
   server, but changing dependencies does.

## How it's driven

App Studio never talks to this pod directly. The infrastructure provider's
data-plane handler authorizes the caller against their workload instance, then
**reverse-proxies** the `logs` / `sync` / `restart` verb to this runner's `:7070`
through the runtime cluster's Service, injecting the per-instance control token.
Consumers therefore hold no runtime credential.

## Configuration (environment)

Set by the template's ResourceGraphDefinition on the pod:

| Variable | Default | Meaning |
|---|---|---|
| `SANDBOX_WORKDIR` | `/workspace` | Workspace root; all file ops are confined here. |
| `SANDBOX_START_COMMAND` | — | Command to launch the dev process (run via `sh -c`). |
| `SANDBOX_PORT` | — | Port the dev app binds (surfaced for the start command). |
| `SANDBOX_CONTROL_TOKEN` | — | Bearer for the control API. Read once, then **unset** from the environment. |
| `SANDBOX_ALLOW_INSECURE_CONTROL` | `false` | Dev-only escape hatch: serve the control API with no token. |

## Security posture

- **Workspace confinement.** All `/sync` writes/deletes go through `os.Root` rooted
  at `SANDBOX_WORKDIR`, plus path cleaning that rejects absolute paths and `../`
  escapes — a forged path can't write outside the workspace.
- **Authenticated control.** Every endpoint except `/healthz` requires
  `X-Sandbox-Control-Token`, constant-time compared against `SANDBOX_CONTROL_TOKEN`.
- **Non-root.** The template runs the pod as `runAsNonRoot` (uid 1000, seccomp
  `RuntimeDefault`).

This runs user-provided code; it is a **development** runtime, not a hardened
multi-tenant sandbox (no per-pod network policy or CPU/mem quotas are enforced
by the runner itself).

## Build & publish

Self-contained module (its own `go.mod`, no external requires), so the build
context is just this directory.

```bash
# build the image locally and load it into the local kind runtime cluster
make load-sandbox-runner-image            # → docker-build-sandbox-runner + kind load

# run the tests
go test ./...
```

CI: [`.github/workflows/infrastructure-sandbox-runner.yaml`](../../../.github/workflows/infrastructure-sandbox-runner.yaml)
tests + builds on every change under this directory, and on push to `main`
publishes `ghcr.io/<owner>/kedge-sandbox-runner` as `:latest` plus an immutable
`:sha-<short>` tag (PRs build single-arch without pushing). This mirrors how the
`application` template's example app images are published.
