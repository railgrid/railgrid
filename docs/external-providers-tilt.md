# Developing external providers with Tilt

If you develop a provider in another repository (for example the private
[railgrid/providers](https://github.com/railgrid/providers)), you can run it in
the same Tilt session as Railgrid. Point `make tilt` or `make tilt-cluster` at
your checkout:

```sh
make tilt EXTERNAL_PROVIDERS_DIR=../providers
make tilt EXTERNAL_PROVIDERS_DIR=../providers,../experimental-providers
make tilt-cluster EXTERNAL_PROVIDERS_DIR=../providers
make tilt-cluster EXTERNAL_PROVIDERS_DIR=../providers EXTERNAL_PROVIDERS=planner

# Equivalent flags for a direct `tilt up`:
tilt up -f Tiltfile.cluster -- \
  --external-providers-dir=../providers,../experimental-providers \
  --external-providers=all
```

The external providers get their own `providers-<name>` groups in the Tilt
dashboard, next to the in-repo providers. Each group has the provider's
workload and whatever manual actions its repository defines, such as
register, init and unregister. The workloads start once the hub is up
(`railgrid-hub` and `kcp-dns`, or `hub` and `kro-mgmt-up` with `make tilt`). Railgrid's Tilt stack does not build, register or configure these
providers itself: it passes the stack's details to the provider repository's
Tilt library, which does that work.

## Independent checkouts

Run `make tilt` from each checkout. Fresh checkouts automatically get separate
ports, kind clusters, Docker container names, kubeconfigs, and state. Startup
also configures the kind cluster to resolve `host.docker.internal` to the host. No stack
name or special runner profile is required. `make tilt-status` shows the saved
ports and checkout identity; the Tilt dashboard URL is also printed at startup.
The settings persist in `.kcp/tilt-checkout.json` so restarts keep the same URLs.
New allocations use ports 20000–29999, excluding the host's ephemeral range.
A locked per-user registry at `~/.local/state/railgrid/tilt-ports.json`
uses the OS account’s home even when a harness overrides `HOME` or
`XDG_STATE_HOME`. It prevents other checkouts from
claiming a stopped stack's saved ports. Deleted checkouts release their claims
on the next allocation. The registry coordinates this user's checkouts; other
programs can still occupy a saved port.
Do not copy `.kcp` between checkouts. An occupied saved port fails startup with
its name and number; stop the process using it before restarting.

Existing checkouts with `.kcp/admin.kubeconfig` keep their previous defaults,
kcp/port conflict checks, and conditional cluster preparation (only when
external providers are configured). Fresh isolated checkouts always prepare
their own kind cluster, so they cannot inherit another checkout's context.
The Go VCS-stamping workaround applies only to Git worktrees (`.git` is a file);
ordinary clones keep normal Go build stamping.

`tilt-checkout.py env` only reads prepared settings. Tiltfile loading uses the
explicit `prepare-env` operation to refresh derived provider manifests.
This applies to the ordinary `make tilt` stack; `make tilt-cluster` still uses
its existing cluster configuration. Bare `tilt up` selects its API port before
reading the Tiltfile, so use `make tilt` for automatic concurrent startup.

External providers run in the checkout's kind cluster and reach its host hub
through `host.docker.internal` on the allocated hub port. Their Tilt library
must honor `<PROVIDER>_PORT` for port forwarding and
`RAILGRID_TILT_KCP_LOOPBACK_PORT` for the embedded-kcp bridge. Update the
private providers checkout alongside this change. Provider registration and
initialization remain the ordinary manual Tilt actions. This creates a real,
independent development environment; it does not copy tenant data or credentials
from the existing environment.

## Settings

| Make variable | Tilt flag | Environment fallback | Default |
| --- | --- | --- | --- |
| `EXTERNAL_PROVIDERS_DIR` | `--external-providers-dir` | `RAILGRID_EXTERNAL_PROVIDERS_DIR` | empty: disabled |
| `EXTERNAL_PROVIDERS` | `--external-providers` | `RAILGRID_EXTERNAL_PROVIDERS` | `all` (unset or empty loads every provider) |

The feature is opt-in because a provider can have its own local
prerequisites. When `EXTERNAL_PROVIDERS` is `all`, a provider whose library
reports a missing prerequisite is skipped, and a warning appears in the
Tiltfile log. The rest of the session still starts. Naming that provider
explicitly (`EXTERNAL_PROVIDERS=<name>`) turns the missing prerequisite into
an error.

`EXTERNAL_PROVIDERS_DIR`, `--external-providers-dir`, and the environment fallback
accept comma-separated checkout paths in both stacks. Quote the value if paths
contain spaces. Relative directories are resolved from the Railgrid checkout;
blank entries are ignored and repeated absolute paths are loaded once. Each
checkout must provide `hack/tilt/providers.tilt`. The same comma-separated value
also works with `make permission-claim-policy` and `make verify-provider-contract`.

The provider selection is passed to each checkout's library. Use `all` when
loading multiple repositories; to select one provider by name, supply only its
checkout. Provider resource names and host ports must be distinct across all
loaded repositories.

## The contract a provider repository implements

A provider repository takes part by shipping a Tilt library at
`hack/tilt/providers.tilt`. The library exports one function:

```python
def railgrid_providers(selection='all', context='', hub_url='',
                       hub_insecure=False, lifecycle_env=None, resource_deps=None,
                       skip_unconfigured=False):
    ...
    return configured  # list of provider names that were configured
```

`Tiltfile.cluster` loads the library with `load_dynamic` and calls the function
with these arguments:

| Argument | Value from `Tiltfile.cluster` |
| --- | --- |
| `selection` | `--external-providers` (`all` or one provider name) |
| `context` | `k8s_context()`, the `kind-kcp-tilt` cluster, already allowed |
| `hub_url` | `https://railgrid-hub.railgrid-system.svc.cluster.local:9443` |
| `hub_insecure` | `True`, because the local hub serves a self-signed certificate |
| `lifecycle_env` | `{'RAILGRID_KCP_KUBECONFIG': '<railgrid>/tilt-frontproxy.kubeconfig'}` |
| `resource_deps` | `['railgrid-hub', 'kcp-dns']` |
| `skip_unconfigured` | `True`: with `all`, skip providers that are missing local setup, with a warning |
| `host_ports` | `False` here. `make tilt` passes `True`: its hub runs on the host, so each provider is forwarded to a local port and its CatalogEntry points at `http://127.0.0.1:<port>` |
| `kcp_loopback_proxy` | `''` here. `make tilt` passes `host.docker.internal:9443`: embedded kcp publishes `https://127.0.0.1:6443` virtual-workspace URLs, so a sidecar bridges each pod's `127.0.0.1:6443` to the hub, which relays them |

Pods can reach the kcp endpoint in the admin kubeconfig
(`https://kcp.localhost:8443`) because the `kcp-dns` resource points
`kcp.localhost` at the Envoy gateway inside the cluster. A kubeconfig that a
lifecycle job derives from it therefore works from the provider pods.

### Rules for the library

- **Define no config flags.** `Tiltfile.cluster` has already called
  `config.parse()`. Parse your own flags in your repository's standalone
  `Tiltfile`, then call the library from there as well:

  ```python
  load('hack/tilt/providers.tilt', 'railgrid_providers')
  config.define_string('provider')
  settings = config.parse()
  allow_k8s_contexts('kind-kcp-tilt')
  railgrid_providers(selection=settings.get('provider', 'all'), context='kind-kcp-tilt')
  ```

- **Use absolute paths.** Tilt runs a function from a loaded file in the
  *caller's* working directory, so relative paths would resolve inside the
  Railgrid checkout. Capture your repository's root when the file loads, and
  build every path from it: `docker_build`, `helm`, `read_file`,
  `watch_file`, and the command paths in `local` and `local_resource`. Pass
  `dir=ROOT` to `local` and `local_resource` as well.

  ```python
  ROOT = os.path.dirname(os.path.dirname(os.path.dirname(__file__)))
  ```

- **Keep resource names distinct from Railgrid's.** The session holds one
  namespace for Tilt resources. Railgrid's in-repo providers (`quickstart`,
  `code`, `kuery`, `app-studio`, `agents`, `edges`, `infrastructure`,
  `secrets`) already use their own names, `<name>-*` resource names and
  `providers-<name>` labels.
- **Let per-provider values win.** Treat `hub_url` and `hub_insecure` as
  defaults that a developer's private values file can override.
- **Add `resource_deps` to every workload and lifecycle resource** so that
  nothing tries to reach the hub before it is up.

The reference implementation is `hack/tilt/providers.tilt` in
railgrid/providers, together with its `hack/tilt/test_runtime.py` tests.
