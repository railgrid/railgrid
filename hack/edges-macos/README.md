# macOS Edges health stub

This directory contains the Stage 1 macOS service-edge fixture. The complete
generic host setup and acceptance matrix is in
[`docs/macos-edges.md`](../../docs/macos-edges.md).

The fixture is intentionally small and safe to run on a development Mac:
`MacOSServer` reaches it through the Edges Service proxy, while the fixture
only reports health. It does not execute commands or enroll a worker.

Run the localhost-only fixture on the Mac host with:

```sh
go run ./hack/edges-macos
```

It serves `GET /` and `GET /healthz` on `127.0.0.1:17873`. Both return a small
JSON health document with `executionEnabled: false`. The process is only a
connectivity fixture; it does not run commands and does not enroll a worker.

To build a local binary, run `make build-macos-stub-native`. The downloadable
Darwin arm64 and amd64 binaries are produced by `make build-macos-stub`.

The build outputs are:

```text
bin/macos-stub-darwin-arm64
bin/macos-stub-darwin-amd64
```

The foreground process lasts only as long as the terminal session and has no
automatic restart after it exits or the Mac reboots. Sleep and network loss can
make the endpoint temporarily unreachable, but do not provide a process
supervisor. The persistent `faros` agent uses launchd, while the health stub
has no bundled LaunchDaemon installer. Start the stub again after a reboot, or
use a separately managed and tested local wrapper when validating reboot
recovery.

After creating and joining a `MacOSServer` named `mac-mini`, apply
`macos-service.yaml` to the Faros tenant workspace and use the Edges Service
endpoint to verify authenticated reachability.

The fixture's Service object targets `127.0.0.1:17873` and its health response
must remain `executionEnabled: false`. For the authentication checks, verify a
same-tenant request reaches the stub and a request from a different tenant is
denied before the stub is reached. The runbook documents the
`authenticatedService` and `wrongtenant` cases plus the sleep, network, and
reboot matrix.
