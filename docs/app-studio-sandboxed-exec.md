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
6. A dedicated `kedge-exec-worker` container owns durable, caller-bound session
   records on the component PVC. It re-hashes the actual managed source files,
   runs the direct argv only when the persistent workspace exactly matches the
   expected revision and digest, and returns bounded stdout, stderr, exit
   state, and truncation metadata.

Start, poll, and cancel are caller-bound and idempotent per assistant run and
tool call. The worker binds each idempotency key to the request fingerprint and
persists terminal results, so a lost Start response or a different provider
replica cannot dispatch the command twice. A worker restart marks an unfinished
record interrupted rather than redispatching it. App Studio separately records
the effect through its durable run-event ledger. In `always_ask`, approval
checkpoints register the typed exec input so a second approved command can
resume after the first one.

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
boundary. Worker session records and the cross-container workspace lock live in
a separate protected metadata directory mounted back over
`<workspace>/.kedge-platform`; command children cannot traverse, replace, or
rename that mount. The source manifest is still revalidated against actual
managed files before execution, so a runtime mutation causes a revision/digest
mismatch until the next authoritative sync repairs it.

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
and runtime-created files. Execution does not occur in the application
container. The dedicated worker has its own PID namespace and container cgroup,
its own `/tmp`, a minimal environment, no copied application mounts or secret
environment, and a masked service-account credential path. It shares the Pod
network because it remains part of the live development environment.

The worker runs command children as the workspace UID with supplemental platform
groups removed. It is container PID 1 and reaps every remaining process in its
namespace after success, timeout, or cancellation, including descendants that
create a new session. If cleanup cannot be proven, the worker exits so the
container runtime tears down the entire worker cgroup. Development overlays
force `shareProcessNamespace: false`; the command cannot join the application's
PID namespace.

This is a development sandbox boundary, not a host or network sandbox. A command
can mutate the derived workspace and reach services allowed by the development
Pod's network policy. The safety controls are:

- tenant-caller authorization at the Infrastructure API boundary;
- explicit per-template exec opt-in and provider-owned target resolution;
- exact command-scope disclosure when `always_ask` is selected, with fail-closed
  denial under `never`;
- direct argv, confined relative cwd, bounded time/output, container-bounded
  process-tree cleanup, and PVC-lock-serialized workspace operations;
- revision/digest verification immediately before execution;
- durable caller/fingerprint-bound idempotency outside provider process memory;
- no App Studio, application secret mount, or runtime service-account credential
  in the worker; and
- no automatic runtime-to-FileStore writeback.

Do not describe this mode as network-denied or as stronger than its Pod network
policy. Runtime namespaces should use development-scoped identities and
least-privilege network access. Application credentials remain available to the
application container but are intentionally not copied into the worker.

## Single execution path

Infrastructure supports only persistent execution through the selected live
development component's dedicated worker. There is no executor mode selector,
disposable source-staging pod, or compatibility path that can silently switch a
project back to a second workspace. The data-plane handler fails closed when
the live component control target or exec worker is unavailable.

This keeps command execution on the development Service proxy and control-secret
path used by sync, logs, and restart, but gives execution a separate named
Service port and container boundary. It does not need separate executor-Pod
lifecycle, source upload, or executor-image configuration: the worker uses the
component's development image and the injected development-agent binary.

Stateful PTY sessions, package-registry policy controls, and approved
runtime-to-FileStore change sets remain separate future capabilities.
