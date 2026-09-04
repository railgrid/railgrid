# Provider UI conformance

The durable UI constitution and browsable conformance contract live in
[design/quality/ui-conformance.md](design/quality/ui-conformance.md). This
short page remains the operational entry point for the existing check:

```sh
make verify-ui-conformance
```

The check runs focused fixture tests and the dependency-free scanner. It scans
the canonical PortalKit roots plus host and provider source roots, excludes
generated/vendor copies, and fails on any unregistered violation. Read the
[quality contract](design/quality/ui-conformance.md) for the scanned vocabulary,
exception policy, and evidence boundaries.
