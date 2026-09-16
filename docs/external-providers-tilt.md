# Developing external providers with Tilt

If you develop a provider in another repository (for example the private
[railgrid/providers](https://github.com/railgrid/providers)), you can run it in
the same Tilt session as Railgrid. Point `make tilt` or `make tilt-cluster` at
your checkout:

```sh
make tilt EXTERNAL_PROVIDERS_DIR=../providers
make tilt-cluster EXTERNAL_PROVIDERS_DIR=../providers
make tilt-cluster EXTERNAL_PROVIDERS_DIR=../providers EXTERNAL_PROVIDERS=linear

# Equivalent flags for a direct `tilt up`:
tilt up -f Tiltfile.cluster -- \
  --external-providers-dir=../providers \
  --external-providers=all \
  --external-providers-api-only
```

The external providers get their own `providers-<name>` groups in the Tilt
dashboard, next to the in-repo providers. Each group has the provider's
workload and whatever manual actions its repository defines, such as
register, init and unregister. The workloads start once the hub is up
(`railgrid-hub` and `kcp-dns`, or `hub` and `kro-mgmt-up` with `make tilt`). Railgrid's Tilt stack does not build, register or configure these
providers itself: it passes the stack's details to the provider repository's
Tilt library, which does that work.

Both stacks support this. With `make tilt-cluster` the providers run in the
`kcp-tilt` cluster next to the in-cluster hub. With the default `make tilt`
stack they run in the `railgrid-kro` kind cluster, which `make tilt` creates
before Tilt starts. They reach the host hub at `https://host.docker.internal:9443`,
which also relays their kcp access, because the embedded kcp listens only on
loopback. A direct `tilt up -f Tiltfile` with these flags must use
`--context kind-railgrid-kro`; the Tiltfile refuses any other context.

## Settings

| Make variable | Tilt flag | Environment fallback | Default |
| --- | --- | --- | --- |
| `EXTERNAL_PROVIDERS_DIR` | `--external-providers-dir` | `RAILGRID_EXTERNAL_PROVIDERS_DIR` | empty: disabled |
| `EXTERNAL_PROVIDERS` | `--external-providers` | `RAILGRID_EXTERNAL_PROVIDERS` | `all` (unset or empty loads every provider) |
| — | `--external-providers-api-only` | — | `false` |

The feature is opt-in because each provider has its own prerequisites. For
example, Factory needs scheduler values unless you pass
`--external-providers-api-only`. When `EXTERNAL_PROVIDERS` is `all`, a provider
that is missing such prerequisites is skipped, and a warning appears in the
Tiltfile log. The rest of the session still starts. Naming the provider
explicitly (`EXTERNAL_PROVIDERS=factory`) turns the missing prerequisite into
an error.

Relative directories are resolved from the Railgrid checkout.

## The contract a provider repository implements

A provider repository takes part by shipping a Tilt library at
`hack/tilt/providers.tilt`. The library exports one function:

```python
def railgrid_providers(selection='all', api_only=False, context='', hub_url='',
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
| `api_only` | `--external-providers-api-only` |
| `context` | `k8s_context()`, the `kind-kcp-tilt` cluster, already allowed |
| `hub_url` | `https://railgrid-hub.railgrid-system.svc.cluster.local:9443` |
| `hub_insecure` | `True`, because the local hub serves a self-signed certificate |
| `lifecycle_env` | `{'RAILGRID_KCP_KUBECONFIG': '<railgrid>/tilt-frontproxy.kubeconfig'}` |
| `resource_deps` | `['railgrid-hub', 'kcp-dns']` |
| `skip_unconfigured` | `True`: with `all`, skip providers that are missing local setup, with a warning |

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
