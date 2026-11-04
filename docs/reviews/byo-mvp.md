# BYO MVP reduction and acceptance

> Superseded browser boundary: the user subsequently chose trusted provider UI
> inside the portal. The isolated-origin/session/renewal evidence below describes
> the earlier implementation and is not a security claim for the current UI.
> Native runtime/backend org isolation remains. See the final policy-change entry.

This record covers the local `codex/org-provider-routing` working branch and its
current Tilt deployment. Verification results below distinguish native security
checks from the explicit local quota exception.

## Preserved full implementation

The source snapshot includes all tracked and untracked implementation files;
current branch HEAD and original staging indexes were left unchanged.

- `codex/byo-full-backup-20261103`: `ab35cba30a168be9124524d88e214747fa5bc483`.
- Active Tilt checkout source: `codex/byo-tilt-backup-20261103`,
  `6eb45e23e4b8bfff2709623b39175331c176b12d` (generated caches excluded).
- Commercial credential fixes, in the separate `railgrid/providers` checkout:
  `codex/byo-credentials-backup-20261103`,
  `a2ee0de6dedf8f6be0e74d1c997490c20be477d5`.

These are local branches; nothing was pushed. Historical full-implementation
review records and application acceptance harnesses remain on the full backup.

## MVP boundary

An org admin may install hostile provider code and accept damage to that org's
data. The provider must not acquire the user's general Hub token, another org's
access, platform authority, or unbounded shared-resource capacity.

The MVP retains:

- Existing Self-Hosting catalog and installation instructions, manual authored-provider
  registration/deployment, restricted native provider credentials and
  Hub-owned authenticated Edge routing. An explicit connected in-org Edge is
  checked before creating a workspace or issuing credentials. Tenant-supplied
  backend URLs cannot choose the Hub's destination.
- Explicit admin trust approval, source/export/binding pins, membership and
  current caller native RBAC for browser/delegated requests. Provider-native
  controllers/verbs retain separately approved org-scoped service permissions;
  they are not universally impersonated as an interactive user.
- One org-bound policy for browser/backend requests; a dedicated origin per
  browser session, native SRI, a host-only Secure/HttpOnly/Strict cookie and no
  ambient portal credentials. Session creation and renewal belong to the
  authenticated parent. No MessagePort network bridge or legacy sandbox returns.
- Automatic renewal of the ten-minute server lease, stable origin/cookie/frame,
  atomic activation/renewal/revocation and bounded stream reauthorization.
- Native KCP object/byte/storage/consumer limits, protected quota metadata,
  admission guards, request rate/concurrency/body/stream/cancellation limits,
  accepted-claim correctness and infrastructure-authority fences.

Logical storage audits and persistent consumer capacity reservations are retained
because manual deployment does not stop native credentials from writing to KCP.
They are containment controls, not an infrastructure deployment service.

## Removed from this branch

- Net-new standalone authored-provider registration page. The existing mainline
  Self-Hosting tab, Edge picker, installation instructions and install-target
  discovery were incorrectly removed in the initial reduction and are restored.
  Catalog enable/disable/open, trust/consent and manual registration remain.
- BYO App Studio preview handoff, instance lookup adapter, special app-auth methods,
  app access-proxy hint machinery and client navigation capability.
- Generic BYO OAuth completion/state relay and its browser messaging/slot machinery.
  Existing platform app authentication and generic backend OAuth contract routes
  are not removed.
- Rich App Studio acceptance harnesses, historical credential-audit tooling,
  historical review records, unrelated lockfile refresh and preview-specific
  local listener configuration. Their source remains on the backup branch.

Native browser workers/downloads/forms and ordinary bounded streams need no new
bridge and are not deliberately broken. They are not a promise of App Studio,
interactive PTY, embedded-preview or external OAuth compatibility.
Existing installed providers, their credentials, APIBindings and stored data are
preserved. Credential-save recovery and version/UID mutation fences remain.

## Checks

- `make test-byo-providers`: passed, including native identity/source/claim/RBAC
  controls, SDK/Edges authenticated relay, isolated-origin and portal contracts.
- Portal full `npm test` under Node 22: 51/51 passed; portal production build passed.
- `make test-byo-availability`: passed against an isolated, unmodified embedded KCP,
  using allocated free ports beside Tilt. Native guard unit tests passed and the
  real two-org admission/request/virtual-workspace suite passed in 28 seconds.
- `make test-kcp-quota-diagnostic`: fails on the pinned upstream quota implementation,
  as documented in `docs/upstream-kcp-requirements.md`. This is an unresolved
  release blocker, not an MVP test to delete or weaken.
- Final formatting, pinned lint (zero issues), build, vet, boilerplate,
  provider-contract and PortalKit parity checks passed. App Studio portal full
  tests and typecheck passed after removal of the handoff-only test.
- Code generation completed successfully and a before/after full-source tree
  comparison was identical (`a4dbf65fe318308457a51d23d7edba5d7857b6fa`). The normal
  `make verify-codegen` wrapper reports a false failure because it explicitly
  compares against HEAD, which excludes these uncommitted MVP changes. No generated
  difference was hidden or accepted; the source index remained unchanged.
- Native credential retirement and cross-cluster impersonation e2e passed
  (14.8 seconds). An initial run referenced the intentionally removed audit tool;
  only that tool-specific assertion was removed. Native containment checks remain.
- SDK complete build/vet/tests pass, including real loopback listener binding.
  All seven changed provider modules build. Six complete module test suites pass;
  Kuery's SQLite tests require CGO, disabled in this environment. Its build passes.
- Live deployment acceptance completed; results and cleanup are recorded below.

## Deployment and live acceptance

Hub, Portal, Infrastructure and App Studio are healthy on Tilt port 20000.
Only reviewed source paths were copied to the active checkout with content-hash
preconditions. The retired preview-only bind argument was removed; provider
selection was unchanged. The org App Studio Helm release is revision 5, using
`railgrid/app-studio-provider:codex-org-byo-mvp-20261104`, image ID
`sha256:151944b0ca8ddc748a7e08deb674d9f80c8454e11059d45da2c72061af19fc72`.
Database, workspace storage, existing credentials and APIBindings were preserved.
The AccessProxy image rebuild uses the ordinary pre-extension proxy source.
No existing provider/data is deleted as part of deployment.
The live security smoke uses a uniquely owned fake ConfigMap and a disposable
foreign workspace, with a direct-access positive control for the same multi-org
administrator. Session cleanup and fixture cleanup must verify recorded identity.

## Hosted release limitation

Hosted enrollment remains fail-closed (`organizationQuotaSelectionSupported`
false). Current Tilt uses the existing explicit dev-only embedded-KCP quota bypass;
this disables native quota enforcement/probes and some inventory checks there.
Cross-org/RBAC/session smoke tests on that stack do not establish hostile-tenant
storage-exhaustion safety. The unmodified native guard suite and failing upstream
quota diagnostic are recorded separately. Public hosted enrollment must remain
closed until supported upstream quota accounting passes the diagnostic and real
capacity acceptance; no dependency cache patch or widened quota is a substitute.

## Late network-boundary finding

A configured platform Infrastructure backend listened on `*:20007`; a direct
health request from the provider-host kind node to the host gateway returned 200.
No identity headers were forged and no tenant data was fetched. This proved
reachability, not a completed authorization exploit. Because the shard-facing
contract trusts private `X-Remote-*` transport, shared backends now honor the
minimal SDK `RAILGRID_LISTEN_HOST` setting, and Tilt gives all host-run first-party
public/replica listeners a loopback default. Container defaults are unchanged.
Etcd client/peer listeners were already loopback-only. Native KCP remains an
authenticated endpoint for export clients, with its separate native checks.
After rollout all seven shared public listeners (ports 20006–20012) and the
Edges replica listener (20013) bind to `127.0.0.1`. Host loopback health checks
returned 200. A temporary non-root pod with no capabilities and no mounted
ServiceAccount token could not TCP-connect to host gateway ports 20006–20013.
Its unique namespace was UID-fenced and removed. No identity headers were forged
and no tenant data was read by the network probe.

A real Infrastructure `instances/exec` call through Hub/KCP returned 200,
`succeeded`, exit 0 and the expected retained fake-file SHA-256 after this rollout.
This proves the private-listener change preserves the legitimate shard path.

Same-org org-owned Databricks/Planner/Factory containers are not shared platform
backends; their native credentials remain org-bound, and protecting an org from
its approved providers is outside this trust model. Deployment of mutually hostile
orgs requires separate/enforced hosting-network isolation; no ineffective
NetworkPolicy is installed on kindnet as a substitute.

## Live MVP results

- Fresh controlled provider `byo-mvp-fresh-20261104-c7f2` was manually registered
  with the explicit connected `provider-host` Edge, deployed using only its
  restricted provider credential, bootstrapped, approved and enabled with consent.
  Its isolated UI created, listed and deleted a real Note; API readback returned
  404 after deletion. Its exact trust/binding/registration, UID-fenced hosting
  namespace, image and private kubeconfig were cleaned up. Existing providers
  were not disabled or removed.
- Catalog screenshots were captured and visually inspected at 1400×1000 in light
  and dark modes. Only the controlled fixture changed state to demonstrate
  Disable/Enable; no deployment wizard/Create/Self-Hosting UI remains.
- Databricks/Planner credential run `20261104022140-148fcaa0` passed all five
  fake-only create/partial-save recovery/update cases; Factory Off run
  `20261104022207-31018682` passed without activation/run. Both cleaned up with
  identity checks; no Secret data was inspected or recorded.
- `/tmp/byo-mvp-browser-results.650s.json` proved 65 successful scoped reads over
  650 seconds, two automatic renewals, unchanged document, revoked teardown and
  stale-cookie rejection. The same human administrator had a positive native
  access control in org B, while the org A provider's direct and forged-scope
  access returned 404. The aggregate legitimate-request header check initially
  misclassified the intentionally forged org B request as normal traffic; the
  harness now tags that adversarial request consistently with its other probes.
  The artifact retains that failure rather than reporting the entire run green.
- Final short run `/tmp/byo-mvp-browser-final.json` passed all 24 checks, including
  real foreign-org direct-access control (200), provider cross-org/forged-scope
  rejection (404), admin-path denial, no browser token, isolated cookie/storage,
  native SRI, removed helper rejection, cancellation, teardown and stale-cookie
  denial. It does not count as another ten-minute renewal test.
- The fake ConfigMap was deleted with its recorded UID precondition. Automatic
  approval initially rejected whole-workspace cleanup because the public API has
  no UID precondition. The user then explicitly approved the exact task workspace
  `codex-byo-mvp-foreign-db42f839`, UUID `f532d068-650d-44c5-9f1f-b18f2bf9213b`.
  Its name, UUID and generated cluster identity were rechecked, normal deletion
  was requested, and the deletion marker was confirmed. The normal grace-period
  lifecycle applies; no preexisting foreign workspace data was touched.

## Rendered evidence

![MVP catalog in light mode, controlled org provider enabled](../../artifacts/screenshots/provider-catalog-mvp-light.png)

![MVP catalog in dark mode, controlled org provider available](../../artifacts/screenshots/provider-catalog-mvp-dark.png)

Images are in the managed Tilt checkout's `artifacts/screenshots/` directory.
The full backup is archival; carry forward the private-listener correction before
reusing it in a tenant-hosting environment.

## Scope correction: preserve existing mainline self-hosting

The initial reduction incorrectly treated all deployment UI as optional new BYO
work. The Self-Hosting tab, cluster selection, credential/install instructions,
self-hostable recipes and install-target discovery already existed at the branch
base (`56f30ab910ab7406e4e334e72c6e9183ef888dd8`). They are restored from the
pre-reduction backup, retaining subsequent authorization/trust fixes. The MVP cut
applies only to net-new branch functionality; it does not authorize removal of
existing mainline features. The new standalone authored-provider registration
page remains deferred. Security isolation and automatic session renewal remain.

The earlier 51-test and browser results above describe the initial reduced UI,
not the restoration. Restoration validation is recorded separately below.

Restoration validation:

- Final full portal suite passed 52/52 tests under Node 22; fresh
  `go test -count=1 ./pkg/hub/restapi` passed, including install-target and
  organization gateway authorization regressions.
- Restored the pre-reduction Self-Hosting tab, hosting Edge selection, existing
  installation/upgrade/credential instructions and source recipes. The endpoint
  still filters targets by organization and visible workspace membership, and
  registration independently validates the connected target.
- Full portal production build passed. Root `make fix-lint` and `make lint`
  passed with zero issues. `make verify-design-docs`, `make verify-portalkit`
  and `make verify-ui-conformance` passed (490 files, zero UI violations).
  The trust-model design document now has the required catalog metadata.
- Live Chromium verified Self-Hosting → Catalog navigation, seven existing
  install-detail controls, four self-host actions, and install-target HTTP 200
  with one eligible connected cluster. Checks ran at 1400×1000 and 390×844
  in both light and dark themes with no document horizontal overflow.
  No provider was registered, upgraded, removed, or reconfigured by this check.
- New screenshots: `artifacts/screenshots/self-hosting-restored-{light,dark}-{desktop,mobile}.png`
  in the active Tilt checkout. Runtime Hub and Portal were both `ok` after rollout.
- `make dev-browser-ready` reached its existing Edges Enable step and returned
  HTTP 412; the stack itself remained healthy. The helper removed its browser
  state before failing. The previous browser scope was restored from the saved
  configuration and validated through actual login/API/browser checks. No stack
  teardown or tenant-data reset was performed.
- A merge-base audit found no further accidental mainline feature deletions.
  The historical auditor, scoped preview helpers and extra Tilt listener-wait
  helper were introduced after the branch base and remain deferred. Retiring the
  old member-registration migration stays coupled to the explicit admin-only
  provider-management policy.

## Final policy change: trusted portal UI

The user explicitly selected **Trust provider code inside the portal** after
being told that it exposes portal DOM/storage/token and every organization or
platform permission of the signed-in user. That choice supersedes the earlier
browser-isolation and renewable-browser-session requirements. No browser
containment of hostile provider UI is claimed.

Removed the dedicated origin package, bootstrap assets, browser cookie/session
store, mint/renew/revoke routes, host interception, iframe transport, browser-only
proxy adapter/CAS store method, domain flag, Helm/Tilt domain settings and origin
smoke harness. No old sandbox or MessagePort network bridge was restored.
Org pages, dashboard tiles and Agents overlays use the trusted custom-element
loader; centralized source-bound approval, authenticated UI grants, same-origin
bundle URLs and exact SHA-384 SRI are checked before script loading. Existing
Self-Hosting functionality remains. The backend trust store, approval revision,
opaque org-bound credentials, native identities, APIBinding/source checks, caller
RBAC, response sanitation, quotas/admission and private transport remain.
Ordinary portal authentication replaces the deleted provider browser lease;
there is no provider-specific ten-minute page expiry or renewal loop.

Validation of this final policy:

- `make fix-lint lint build vet` passed; `make test-byo-providers` passed the
  retained backend/native/SDK/relay and trusted-loading contracts.
- Full portal suite passed **479 tests**, with zero failures. Portal production
  build passed. Design-doc, PortalKit, UI-conformance and provider-contract
  verification passed. One concurrent npm-ci/component-test attempt failed
  during dependency replacement; the final stable-dependency run passed.
- Live Chromium opened Planner Connections, Databricks Connections and Factory
  Lines on `https://deathstar.tail1bddb4.ts.net:20001`, with bundle grants and
  connection lists returning 200. Provider custom elements rendered in the
  portal, with no `byo-*` requests, iframes or browser-session requests.
  Planner light desktop (1400×1000) and dark mobile (390×844) screenshots are
  `artifacts/screenshots/planner-trusted-tailscale-{light,dark}.png` in the active
  checkout; they were visually inspected.
- The real Databricks/Planner fake-credential harness run
  `20261104035602-74f26152` passed five cases: partial-save retry/create, owned
  Secret rotation, normal create, collision/retry, and owned Secret update/retry.
  Connection/Secret identity and version assertions passed. All task fixtures
  were cleaned with UID fences. No real credentials were used.
- Factory run `20261104035714-09257bb1` passed disabled incomplete-reference
  draft edit/save: exactly one existing FactoryLine PATCH, references preserved,
  no activation/run, and UID-fenced cleanup. The earlier adapted harness still
  matched the retired hostname in its response waiter; it timed out despite a
  successful PATCH, cleaned its draft, and was corrected before the passing run.
- The first page-load harness incorrectly required a custom element with
  `display:contents` to have a visible box; checking attachment and rendered
  content corrected that harness. These were harness defects, not product fixes.
- No external service integration, terminal, preview or OAuth acceptance is
  claimed by these checks. The existing upstream KCP quota release blocker
  remains; the trusted UI policy does not remove native/backend controls.

Local artifacts: `/tmp/byo-trusted-browser-results.json`,
`/tmp/byo-trusted-credential-live.log`, `/tmp/byo-trusted-factory-off-final.log`,
`/tmp/byo-trusted-security-tests.log`, `/tmp/byo-trusted-portal-tests.log`,
`/tmp/byo-trusted-build-lint.log`, `/tmp/byo-trusted-gates.log`.

## Follow-up: platform-bound Edges under an org catalog shadow

The org catalog advertises a Ready org-owned `edges`, while the selected workspace
still binds `root:railgrid:providers:edges` with `selfHosted:false`. The trusted
loader used the org catalog entry to request org approval and received 404 before
requesting a bundle. It now consumes the current binding projection: an explicit
platform binding under an advertised org shadow resolves the platform-only catalog
without tenant headers, requires the same provider name and exact binding export
path, and loads that public platform bundle with its pin. It does not fall back
from a failed org approval or grant. Stale binding projections cannot select the
platform exception. SRI refresh follows that same source without replacing the
org enrollment catalog. Provider pages, dashboard tiles and Agents overlays pass
the binding projection; backend routing and all approvals/installations are unchanged.

- Final full portal suite: 483 tests passed. Portal production build, design-doc,
  PortalKit and UI-conformance gates passed; 489 files, zero UI violations.
- Live Tailscale Edges page rendered its connected `provider-host` in light and
  dark themes. Platform main.js returned 200 without a grant; zero org trust or
  org UI-grant calls were made. Both screenshots were visually inspected:
  `artifacts/screenshots/edges-platform-binding-{light,dark}.png` in the active
  Tilt checkout. No installation, binding, approval or Edge was mutated.
- Artifacts: `/tmp/edges-bound-ui-browser-results.json`,
  `/tmp/edges-bound-ui-final-tests.log`, `/tmp/edges-bound-ui-build.log`,
  `/tmp/edges-bound-ui-gates.log`.
