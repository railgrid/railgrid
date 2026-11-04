# Provider copy selection audit and end-to-end acceptance

This record covers the local working branch and the existing Tilt cluster.
Provider UI code is deliberately trusted inside the portal; the org-bound native
and backend credential boundary remains separate. No provider installation or
binding is silently migrated to another source as part of copy selection.

## Confirmed problems and corrections

- The org-visible catalog shadows platform entries by name, but workspace runtime
  metadata now comes from the exact currently bound provider. Readiness, version,
  navigation children, dashboard tiles and provider pages share that selection.
- The script loader previously cached only name/version. Its cache and loaded DOM
  shortcut now include stable source identity. Direct custom-element providers
  require reload on copy changes; App Studio can supersede its retained loader.
- Backend MCP/OAuth/health routes and UI grants now resolve live workspace binding
  export path/name/issuer and current registration. Aggregate MCP takes one fresh
  LogicalCluster/APIBinding snapshot per enumeration, rather than per-provider
  administrative read loops or a stale source cache.
- Hub-access/composition projections, org backend resource/verb fences and org
  native dependency policies use the binding's selected source. App Studio action
  proofs and resource discovery no longer let a same-name org shadow choose a
  platform-bound operation's identity or declaration.
- Source-instance metadata distinguishes recreated workspaces at the same export
  path/name; issuer mismatch must not keep a stale registration executable.
  Source-aware asset pins and the SRI cache likewise reject a changed issuer or
  export at the same provider version.
- Org App Studio's lazy JavaScript, CSS and file URLs use the bootstrap's scoped
  asset directory rather than anonymously falling through to platform files.
  New asset grants are limited to static files, source path/name/issuer/version,
  and eight hours, with live authorization checks on redemption; legacy query
  grants retain five-minute validity. This is asset delivery authority, not an
  API or terminal session. A page needs reloading for later uncached asset loads
  after eight hours. MCP, OAuth and other backend route classes are excluded.

## Existing cluster state

The default workspace binds platform App Studio and Edges while org copies of both
are present in the catalog. Agents is bound to an older org installation whose
current trust/source validation is unavailable. Kuery has an org APIBinding with
`APIExportValid=False`, but no matching registered org provider. Those conditions
are distinct from a platform/org copy mix-up; they must remain visibly unavailable,
not be repaired by switching bindings or granting new authority automatically.
The separate App Studio acceptance workspace binds org-owned App Studio and
platform Code/Infrastructure. The routing-verification workspace binds platform
Edges. Databricks, Planner, Factory and the Notes provider are org-owned.

## Validation

The source-only changes were applied to the existing Tilt checkout. Hub and portal
reported update/runtime `ok`; no existing provider binding, credential, or approval
was migrated.

- Root `make build`, `make vet`, final `make lint`, BYO security suite
  (`make test-byo-providers`), design-document verification, PortalKit parity,
  UI conformance, provider-contract verification and boilerplate checks passed.
  The first lint run caught formatting, which was corrected. An initial verifier
  run under Node 20 could not import TypeScript; the affected checks passed under
  Node 22.
- Fresh Go tests passed for hub, identity, REST, providers, MCP aggregation and
  KCP. The full providers package was rerun after the final asset-grant change.
- The portal production build/typecheck and all 488 portal tests passed under
  Node 22. App Studio's production build, bundle budgets, scoped-asset tests and
  provider Go build passed.
- Browser checks through the actual Tailscale portal confirmed platform Edges,
  App Studio, Code, Infrastructure and Quickstart, and org Databricks, Planner,
  Factory and Notes. Script source identities matched the binding; org assets
  used scoped grants, and platform pages requested no org UI grants. Agents and
  Kuery displayed finite errors and loaded no substitute executable bundle.
  Screenshots cover Edges, Planner and the missing Kuery source in desktop light
  and mobile dark themes. An initial Planner request hit a transient 502 during
  the control-plane restart; both readiness and browser checks passed afterward.
- Read-only aggregate MCP initialization and tools enumeration passed: 57 tools
  across Code, Databricks, Edges, Infrastructure and Planner. No orphan Kuery
  source was substituted. On this warm local stack, initialization took 820 ms
  and tools enumeration 1,131 ms; these are observations, not load benchmarks.

- Live credential regressions passed all five Databricks/Planner create,
  partial-save recovery and owned-Secret rotation/update cases. Expected write
  sequences and UID/resourceVersion stability were checked; all fixture
  Connections and Secrets were deleted with UID preconditions. Factory's disabled
  draft save passed with exactly one FactoryLine patch, no activation or run,
  preserved disabled references, and UID-fenced cleanup.

- App Studio switched platform → org → platform in the same browser document.
  Source issuer keys matched each binding. Org main.js, lazy page JavaScript,
  stylesheet and style module returned 200 through its scoped asset directory;
  the return to platform used the platform bundle (valid 304 cache revalidation).
  The final org main.js bytes independently matched the SHA-384 grant pin.
  Screenshots are `provider-copy-appstudio-platform-0.png`,
  `provider-copy-appstudio-org-1.png` and
  `provider-copy-appstudio-platform-2.png` under the active checkout's
  `artifacts/screenshots/`. Initial attempts during startup timed out or treated
  a valid 304 as failure; the final bounded browser check passed all three legs.
- Org App Studio is running
  `railgrid/app-studio-provider:codex-copy-selection-20261104`, image
  `sha256:45507080b59adaca7f22d330181ec5971fc61d0f3e160a124eae966a5b73996b`.
  Its replacement pod was Ready with zero restarts. PostgreSQL and both PVC UIDs
  were preserved. The Helm dry-run included unrelated workload changes, so the
  update changed only the Deployment's image. **Helm remains revision 5 with
  the old image value**; a later Helm reconciliation must deliberately retain
  the deployed image. Task-owned image archives and release exports were removed.

- A freshly authored `byo-mvp-notes-20261104-a91e` provider completed actual
  registration (201), edge deployment, bootstrap, explicit organization-v1
  approval and Enable (200). The binding matched its exact org export path,
  name and issuer. Its first bootstrap failed because the generated test
  manifest omitted this kind cluster's required host alias; correcting only
  the fixture's networking made bootstrap succeed.
- Through the real Tailscale browser, that provider created a Note (201),
  listed/read it (200), preserved it across a page reload, updated it (200),
  deleted it (200), and confirmed the list was empty. The source key named the
  expected org/provider. Main JavaScript and a separately imported source-marker
  asset both returned 200 under the scoped org grant; grant minting passed on
  initial open and reload, with zero browser page errors. Rendered evidence:
  `artifacts/screenshots/byo-notes-lifecycle.png` in this worktree.
  UID/source-fenced cleanup completed: binding absent, approval revoked,
  registration and catalog source absent, and the exact task namespace deleted
  with its recorded UID precondition and verified absent. The unique local image
  and private credentials were removed. Root independently confirmed the catalog
  absence. Credential-free results are retained at
  `artifacts/byo-notes-lifecycle.json`; existing providers and data were preserved.
- Final Tilt status was update/runtime `ok` for Hub, portal, Edges, Code,
  Infrastructure, Quickstart and platform App Studio; org App Studio's separate
  Deployment was Ready. No existing binding was switched by this work.

## Limits of this acceptance

These results validate provider-copy selection, a custom CRUD lifecycle,
credential-save recovery and App Studio asset/source switching. They do not
re-establish all rich App Studio workflows (workers, terminal reconnect, uploads,
previews or OAuth). Generic org-owned anonymous OAuth callbacks remain outside
the reduced MVP; authenticated scoped OAuth routes follow the binding, while
anonymous callbacks remain platform-only. Browser code is intentionally trusted
inside the portal per the chosen trust model. The native/backend org isolation
boundary remains separate; do not infer browser credential isolation or production
KCP quota readiness from this acceptance.
