# Contributing to Kedge

## Prerequisites

- Go 1.25+
- [kind](https://kind.sigs.k8s.io/) (for running a local agent cluster)
- `openssl` (for generating dev TLS certs)
- `golangci-lint` (for linting)

Tools like kcp, Dex, controller-gen, and apigen are installed automatically by Make on first use into `hack/tools/`.

## Development environment

### Full stack (single terminal)

The fastest way to get everything running:

```bash
make dev
```

This starts kcp, Dex, and the hub in one terminal. Ctrl-C stops everything.

In a second terminal, log in and register an edge:

```bash
make dev-login
make dev-edge-create
make dev-run-edge
```

### Component by component

If you prefer running services separately:

```bash
# Terminal 1: kcp + Dex
make dev-infra

# Terminal 2: Hub (rebuild with make run-hub after code changes)
make run-hub

# Terminal 3: Edge agent (requires dev-edge-create first)
make dev-run-edge
```

### Deploying a test workload

```bash
make dev-create-workload
```

This applies an example `VirtualWorkload` that targets dev sites.

## Building

```bash
make build          # All binaries (kedge, kedge-hub, kedge-agent)
make build-kedge    # CLI only
make build-hub      # Hub only
make build-agent    # Agent only
```

Binaries are output to `bin/`.

## Code generation

After changing API types in `apis/`:

```bash
make codegen
```

This runs:
1. `controller-gen` to generate CRDs from Go types
2. `apigen` to generate kcp APIResourceSchemas and APIExports
3. `ensure-boilerplate.sh` to add license headers to any new files

## Testing and verification

```bash
make test               # Run all tests
make test-util          # Run utility package tests only
make lint               # Run golangci-lint
make vet                # Run go vet
make verify             # Run all checks (vet + lint + test + boilerplate)
make verify-boilerplate # Check license headers only (no modifications)
```

## License headers

All Go files must have the Apache 2.0 license boilerplate. To add it to new files:

```bash
make boilerplate
```

This is also run as part of `make codegen`. The `make verify` target checks headers without modifying files.

## Project layout

```
apis/                  Custom resource types (Site, VirtualWorkload, Placement)
cmd/
  kedge/               CLI entrypoint
  kedge-hub/           Hub server entrypoint
  kedge-agent/         Agent entrypoint
config/
  crds/                Generated CRD YAML
  kcp/                 Generated kcp APIResourceSchemas and APIExports
pkg/
  agent/               Agent logic (tunnel, workload reconciler, status reporter)
  cli/                 CLI commands and auth
  hub/                 Hub server, controllers (scheduler, status, site lifecycle)
  server/              HTTP handlers (auth, kcp proxy)
  virtual/builder/     Tunnel endpoint handlers (edge proxy, agent proxy, site proxy)
  client/              Dynamic kedge API client
  util/                Shared utilities (connman, revdial, ssh, http)
hack/
  boilerplate/         License header template
  scripts/             Dev environment scripts
  dev/                 Dev config (Dex config, example manifests)
```

## Submitting changes

1. Fork the repository and create a feature branch.
2. Make your changes.
3. Run `make codegen` if you changed API types.
4. Run `make verify` to ensure all checks pass.
5. Submit a pull request.
