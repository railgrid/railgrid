# macOS service edges

Stage 1 adds `MacOSServer`, a macOS host connected by the Railgrid agent's
outbound reverse tunnel. It is a service-only edge: a `MacOSServer` has no SSH
or Kubernetes data plane. A Railgrid `Service` reaches an HTTP service on the Mac
host, normally through `127.0.0.1` or another address allowed by the agent's
service policy.

This runbook is deliberately generic. Use the hub URL, tenant cluster, edge
name, and one-time join token shown by the portal for your own tenant. Keep
those values private; do not paste them into documentation, issues, or logs.

## Stage 1 boundary

The implemented transport covers registration, tunnel connectivity, host-local
Service proxying, and persistent agent supervision through a macOS
LaunchDaemon. It does not include SSH, Kubernetes proxying, a command
execution worker, or a runtime that enrolls itself as a worker. Validation on an
Apple Silicon MacBook using the Darwin arm64 agent registered the edge as Ready;
the authenticated health Service returned HTTP 200 with
`executionEnabled: false`, while an unrelated user received HTTP 403 and an
anonymous request received HTTP 401. Disabling Wi-Fi for about 30 seconds and
restoring it recovered the request. Sleep caused a request timeout; after wake,
a fresh heartbeat preceded a successful HTTP 200 response. After reboot, the
agent reconnected once the user re-enabled Tailscale, and the local stub was
manually restarted before the health check. Exact reconnect latency and
offline-status convergence were not measured; launchd process details were not
remotely inspected, and no manual Railgrid agent restart was reported. The matrix
below remains the repeatable acceptance procedure.

## Build or install the binaries

The agent build produces both Darwin architectures:

```sh
make build-macos-agent
# bin/railgrid-darwin-arm64
# bin/railgrid-darwin-amd64
```

Select the binary that matches the Mac, then install the CLI at a durable path
before invoking `sudo railgrid agent join`:

```sh
sudo install -m 0755 <selected-railgrid-darwin-binary> /usr/local/bin/railgrid
command -v railgrid
railgrid version
```

For the localhost health fixture, use the foreground source form during a
validation session:

```sh
go run ./hack/edges-macos
```

The native helper target is also available:

```sh
make build-macos-stub-native
./bin/macos-stub
```

Downloadable Darwin stub builds are produced by `make build-macos-stub`:
`bin/macos-stub-darwin-arm64` and `bin/macos-stub-darwin-amd64`.

The stub listens only on `127.0.0.1:17873` by default. Both `GET /` and
`GET /healthz` return JSON with `status: "ok"` and
`executionEnabled: false`:

```sh
curl --fail http://127.0.0.1:17873/healthz
```

The stub is a connectivity fixture. It does not execute commands and it does
not enroll a worker.

## Create and join a MacOSServer

Create the edge from the portal, or from a tenant-scoped CLI context:

```sh
railgrid edge create mac-mini --type macos
```

Copy the displayed hub URL, cluster, and one-time token privately from the
portal. The hub URL must be scoped to the tenant cluster. The following keeps
the token out of the command text and shell history as far as the shell
configuration permits:

```sh
RAILGRID_EDGE_NAME='mac-mini'
RAILGRID_HUB_URL='https://<hub-host>/clusters/<tenant-cluster>'
RAILGRID_CLUSTER='<tenant-cluster>'
read -r -s RAILGRID_JOIN_TOKEN
printf '\n'
```

Run the persistent install from the Mac account that should own the agent,
using that account explicitly as the non-root launchd worker:

```sh
sudo railgrid agent join \
  --hub-url "$RAILGRID_HUB_URL" \
  --edge-name "$RAILGRID_EDGE_NAME" \
  --type macos \
  --worker-user "$(id -un)" \
  --cluster "$RAILGRID_CLUSTER" \
  --token "$RAILGRID_JOIN_TOKEN"
```

If the portal shows the default cluster, omit `--cluster`; keep the scoped
cluster segment in `--hub-url`. The installer writes the bootstrap config for
the worker account with owner-only permissions, creates
`/Library/LaunchDaemons/com.railgrid.agent.<edge>.plist`, and loads it with
`launchctl`. The plist runs `railgrid agent run` as the configured non-root user;
it sets `HOME` to that user's home directory, uses `RunAtLoad` and `KeepAlive`,
and writes agent logs below
`~/Library/Logs/Railgrid/agent-<edge>.log` and
`~/Library/Logs/Railgrid/agent-<edge>.error.log`.

The one-time token is used to exchange for the durable agent credential. Do
not put the token in a plist or a checked-in script. The agent's saved
credential remains under the worker user's `~/.railgrid` directory.

To let this Mac host an [edge add-on](edge-addons.md) — today only the coding
runner — add `--allow-addon runner` to either command. The daemon already runs
as the non-root worker account, so an add-on child runs as that same account and
`--addon-user` is neither required nor accepted here (it is a Linux/systemd
concern, where the agent runs as root). Without `--allow-addon`, an `Addon`
declared for this edge is reported `Allowed=False` and nothing is started.

For a short-lived foreground validation instead of launchd, run the agent in
another terminal with the same scoped values:

```sh
railgrid agent run \
  --hub-url "$RAILGRID_HUB_URL" \
  --edge-name "$RAILGRID_EDGE_NAME" \
  --type macos \
  --cluster "$RAILGRID_CLUSTER" \
  --token "$RAILGRID_JOIN_TOKEN"
```

## Add the health Service

The repository fixture is [hack/edges-macos/macos-service.yaml](../hack/edges-macos/macos-service.yaml).
It references an edge named `mac-mini`, sends HTTP to `127.0.0.1:17873`, and
uses the generic Service type. If the edge has another name, copy the fixture
and update both `metadata.labels.edges.railgrid.ai/edge` and
`spec.edgeRef.name` before applying it in the tenant workspace:

```sh
kubectl apply -f hack/edges-macos/macos-service.yaml
```

The authenticated Service URL has this shape:

```text
https://<hub-host>/clusters/<tenant-cluster>/apis/edges.railgrid.ai/v1alpha1/services/macos-runner-stub/proxy/healthz
```

Use the portal's Edges Service view or an authenticated tenant request to call
that URL. The caller's Railgrid credential authorizes access to the Service and
the tenant cluster; the fixture itself has no execution capability and does
not require an upstream service token.

## Authentication and tenant checks

Run these two checks with a tenant credential that is valid for the test
cluster. Do not use the one-time agent join token as the caller credential.

| Case | Action | Expected result |
| --- | --- | --- |
| `authenticatedService` | Call the Service proxy URL for `macos-runner-stub` from the owning tenant and request `/healthz`. | The request is authorized, reaches the Mac stub, returns HTTP 200, and contains `executionEnabled: false`. |
| `wrongtenant` | Repeat the same request with a credential or cluster context belonging to another tenant. | The request fails closed with an authorization or object-scope failure and must not reach the Mac stub. |

The Service proxy authorizes the caller before it fetches the tenant's Service
object and opens the Mac tunnel. A successful response therefore proves both
the caller's tenant scope and the host-local transport; it does not prove that
an execution worker exists.

## Sleep, network, and reboot matrix

Record the observed status and the time until the tunnel becomes healthy again
for each row. The expected recovery window depends on the hub and network, so
this runbook does not assign a fixed number of seconds.

| Case | Action | Expected result |
| --- | --- | --- |
| Foreground baseline | Run the stub and foreground agent, then call `authenticatedService`. | The MacOSServer becomes connected and the health response is returned. |
| Sleep and wake | Put the Mac to sleep, wake it, and retry the Service request. | Requests can fail while asleep; after the agent reconnects, the authenticated Service request recovers. |
| Network loss and restore | Disable the Mac's network, retry, restore network, then retry again. | Requests fail while the tunnel has no network path and recover after the agent reconnects. |
| Reboot with launchd agent | Reboot with the `railgrid agent join` LaunchDaemon installed. | launchd starts the agent again under the configured non-root account and the tunnel can recover. |
| Reboot with foreground stub | Reboot while the stub was started with `go run` or `./bin/macos-stub`. | The stub does not persist across reboot; start it again before expecting the Service health request to succeed. |

The foreground stub has no reboot persistence by design. A complete post-reboot
Service test therefore needs a separately managed stub process or a tested
LaunchDaemon wrapper for the stub; the repository fixture does not install one.

## Uninstall

Remove the persistent agent service from the Mac with:

```sh
sudo railgrid agent uninstall --type macos --edge-name "$RAILGRID_EDGE_NAME"
```

This unloads the `com.railgrid.agent.<edge>` LaunchDaemon and removes its plist.
Remove the `MacOSServer` and its `Service` from the tenant workspace through
the portal or the tenant-scoped Kubernetes API when the test is complete. The
uninstall command does not remove those API objects or stop a separately
started foreground stub.
