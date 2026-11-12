# Railgrid Impeccable document preview

This worktree contains a scan of the incumbent design, plus a local review site.
`DESIGN.md` follows the standard eight sections. The schema v2 sidecar contains
nine specimens. CSS is extracted from canonical PortalKit where possible;
shell utilities are expanded to plain CSS. The preview displays those exact
sidecar snippets in shadow roots and links to the source lines.

Run with Node 20+ and installed portal dependencies:

```sh
node hack/impeccable-preview/generate.mjs
node hack/impeccable-preview/serve.mjs
```

If the worktree doesn't have its own dependencies, set
`RAILGRID_PORTAL_MODULES` to the absolute `portal/node_modules` directory of a
Railgrid checkout with dependencies installed. Visit http://127.0.0.1:5195/.
`RAILGRID_IMPECCABLE_PORT` can override the port.

The full design book is a separate review fixture at port 5197. This scan
captures the visual system from the checkout; see `docs/design/README.md` for
normative behavior, accessibility, provider integration, and exceptions.
