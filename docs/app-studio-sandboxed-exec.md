# App Studio development command execution

App Studio exposes a bounded `exec_command` assistant tool for compiler, test,
lint, type-check, formatter-check, code-generation, and diagnostic commands.
Normal execution reuses the selected component's persistent development
workspace, toolchain, dependency installation, and caches. It does not create a
second source-only executor pod.

Infrastructure remains the runtime authority. App Studio never receives a
runtime kubeconfig and never executes tenant source inside the App Studio
provider. Calls cross the provider boundary through Infrastructure's published,
caller-authorized data-plane `exec` capability.

## Request path

1. App Studio waits for the current run-local source mutation to complete its
   ordered development synchronization.
2. It captures a stable component snapshot from FileStore and binds its
   component-relative digest to the workspace's durable source revision.
3. App Studio applies the run's approval mode. The default `on_request` mode
   dispatches bounded commands without interrupting the run; `always_ask`
   presents the sanitized direct argv, component, relative working directory,
   timeout, development-network profile, and runtime-only write policy before
   every command; `never` rejects the call without contacting Infrastructure.
4. App Studio calls Infrastructure as the tenant caller:
   `POST .../{resource}/{instance}/components/{component}/exec`. The start
   request carries the expected revision and digest, not another file snapshot.
5. Infrastructure re-authorizes the caller for both the instance and the
   template's explicit exec capability, resolves the platform-owned live
   control Service's `exec` port, and forwards the lifecycle request with the
   instance control token. Infrastructure provider replicas retain no local
   execution-session state.
6. The platform coordinator owns the durable, caller-bound session and
   idempotency records on the separate per-component platform-state PVC at
   `/kedge/state`. It forwards an already-authorized execution request to the
   stateless executor over pod loopback. The executor re-hashes the actual
   managed source files, runs typed argv only when the workspace exactly
   matches the expected revision and SHA-256 digest, and returns bounded
   stdout, stderr, exit state, and truncation metadata.

Start, poll, and cancel are caller-bound and idempotent per assistant run and
tool call. The coordinator binds each idempotency key to the request fingerprint
and persists terminal results, so a lost Start response or a different provider
replica cannot dispatch the command twice. Coordinator recovery marks an
unfinished record interrupted rather than redispatching it. App Studio
separately records the effect through its durable run-event ledger. In
`always_ask`, approval checkpoints register the typed exec input so a second
approved command can resume after the first one.

## One development workspace

FileStore remains the durable assistant-authored source authority. Each project
workspace has a monotonic durable source revision. Full component syncs carry
that revision, a digest, and the complete managed text-file set.

The live component workspace is derived but persistent state:

- managed source is made authoritative on every full sync;
- a reserved manifest records only platform-managed source paths;
- paths absent from the next managed set are deleted;
- `node_modules`, compiler caches, build output, logs, and other runtime-created
  paths are never inferred as source and are preserved;
- a repeated revision/digest is a no-op only after the agent re-hashes the
  actual files; and
- a stale revision or conflicting digest fails closed.

This removes the former split between a preview PVC with installed dependencies
and a disposable exec `emptyDir` without them. Package-install reload rules and
later `npm run build`, tests, linters, and framework CLIs observe the same
toolchain state.

Commands may mutate the derived runtime workspace. Those mutations can affect
the running preview but are not synchronized back to FileStore, and a later
authoritative source sync restores managed files. There is no implicit two-way
watcher. A future runtime-to-FileStore feature must be a separate approved,
bounded change-set operation with baseline hashes and conflict handling.

The source manifest is an integrity and convergence record, not an authorization
boundary. Execution session records live on the coordinator's separate
platform-state PVC, not in the application workspace. The manifest is still
revalidated against actual managed files before execution, so a runtime mutation
causes a revision/digest mismatch until the next authoritative sync repairs it.

## Template opt-in and request limits

Execution is disabled unless a development component also declares a data-plane
`exec` capability and a proxy `sync` endpoint for the live control Service. The
template declaration can only lower provider ceilings:

```yaml
spec:
  development:
    components:
      app:
        workspacePath: .
        devImage: "${kedge.devImage.node}"
        workingDir: /workspace
        startCommand: npm run dev -- --host 0.0.0.0
  dataPlane:
    components:
      app:
        exec:
          maxTimeoutSeconds: 120
          maxOutputBytes: 262144
        endpoints:
          sync: { ... }
```

The caller cannot choose an image, absolute component root, environment
overrides, volume, service account, Kubernetes object, or control endpoint.
Workdir is relative to the selected component root. Argv count and token size,
timeout, response bytes, session count, and retention are bounded. There is no
implicit command-string shell, PTY, host path, pod exec, or connection upgrade.
An explicit interpreter in argv is allowed because arbitrary project scripts
can already launch one.

## Authority and safety boundary

Persistent exec uses the selected component's development image and persistent
workspace, so commands see the real toolchain, installed dependencies, caches,
and runtime-created files. Execution occurs in the component's separate
stateless executor container, not in the runtime supervisor container.

The three containers have deliberately separate authorities:

| Container | Owns | Does not receive |
|---|---|---|
| platform coordinator | public control and exec endpoints, the instance control token, and durable session/idempotency state on `/kedge/state` | application environment or secrets |
| app runtime supervisor | the application environment and secrets; starts, restarts, and reloads the app process | the platform control token and platform-state PVC |
| stateless executor | typed argv execution after workspace revision/SHA-256 verification | the platform control token, platform-state PVC, application environment/secrets, and service-account credentials |

The App Studio → Infrastructure control contract remains token-authenticated on
the component Service's public ports `7070` (control) and `7071` (exec), using
`X-Sandbox-Control-Token`. The runtime supervisor's internal control surface
binds only to pod loopback on `127.0.0.1:7072`; the executor's internal surface
binds only to pod loopback on `127.0.0.1:7073`. The coordinator is the only
container exposed through the public Service for these platform operations.

All three containers run as UID/GID `1000`, with
`allowPrivilegeEscalation: false` and all Linux capabilities dropped. The pod
uses seccomp `RuntimeDefault`, `fsGroup: 1000`, and
`shareProcessNamespace: false`. The executor has its own `/tmp` and a masked
service-account path; it shares only the component workspace needed for the
command and the injected agent binary.

This is a development sandbox boundary, not a host or network sandbox. A command
can mutate the derived workspace and reach services allowed by the development
Pod's network policy. The safety controls are:

- tenant-caller authorization at the Infrastructure API boundary;
- explicit per-template exec opt-in and provider-owned target resolution;
- exact command-scope disclosure when `always_ask` is selected, with fail-closed
  denial under `never`;
- direct typed argv, confined relative cwd, bounded time/output, and
  container-bounded process-tree cleanup;
- revision/digest verification immediately before execution;
- durable caller/fingerprint-bound idempotency outside provider process memory;
- no App Studio, application secret mount, or runtime service-account credential
  in the executor; and
- no automatic runtime-to-FileStore writeback.

Do not describe this mode as network-denied or as stronger than its Pod network
policy. Runtime namespaces should use development-scoped identities and
least-privilege network access. Application credentials remain available to the
runtime supervisor but are intentionally not copied into the executor.

## Execution topology

The component Service proxy and control-secret path used by sync, logs, and
restart also carries exec on its separate named port. The coordinator owns the
request lifecycle and durable records; the stateless executor uses the
component's development image and injected development-agent binary to run the
command in the persistent workspace. The data-plane handler fails closed when
the live component control target is unavailable.

Stateful PTY sessions, package-registry policy controls, and approved
runtime-to-FileStore change sets remain separate future capabilities.
