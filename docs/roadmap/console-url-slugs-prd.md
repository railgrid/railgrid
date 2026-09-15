Status: **NOT IMPLEMENTED.** Proposal written 13 September 2026; no implementation
phase has landed on main as of baseline commit `f273ebe5`. Section 2 records the
current baseline; the slug capabilities described elsewhere are proposed.

# Console URL slugs

## 1. Summary

Railgrid console URLs currently expose organization and workspace UUIDs. That is
correct for identity and authorization, but it makes links difficult to read,
remember, communicate, and recognize in support conversations. This proposal
adds durable, human-readable URL slugs while preserving UUIDs as the platform's
authoritative identities.

The intended URL shape is `/ui/<orgSlug>/<workspaceSlug>/...`. Organization
settings live at `/ui/<orgSlug>/settings/...`; workspace settings use
`/ui/<orgSlug>/settings/workspaces/<workspaceSlug>`. A slug is an address for a
specific object on one Railgrid hub. It is immutable after allocation, and a
display name remains editable and may be shared by several organizations or
workspaces. Organization slugs are unique across a hub. Workspace slugs are
unique within their organization.

This is a product requirements document with a proposed technical contract.
The new slug routes, storage, endpoints, and command options do not exist in
the baseline described in section 2.

## 2. Where we are (dated 13 September 2026)

Organization display names are editable and are not unique. The canonical
portal navigation helper in
[`provider-sdk/portalkit/navigation.ts`](../../provider-sdk/portalkit/navigation.ts)
parses and builds UUID organization and workspace scopes. The shared
[`provider-sdk/portalkit/tenant.ts`](../../provider-sdk/portalkit/tenant.ts) reads
UUID selections from the tenant context and uses UUIDs for tenant headers;
hosted documents take their authority from their own URL when a scope is
present. The route context store and guard verify the requested UUID context
before the destination mounts, including ownership and deletion checks
([`portal/src/stores/routeContext.ts`](../../portal/src/stores/routeContext.ts),
[`portal/src/router/contextGuard.ts`](../../portal/src/router/contextGuard.ts)).
Workspace settings currently include the workspace UUID in the detail route
and use UUIDs for selection and API calls
([`portal/src/pages/TenantSettingsPage.vue`](../../portal/src/pages/TenantSettingsPage.vue)).
The organization creation form currently asks only for a display name
([`portal/src/pages/OrganizationCreatePage.vue`](../../portal/src/pages/OrganizationCreatePage.vue)).

These facts describe the baseline at commit `f273ebe5` on the date above. During
backend staging before slug cutover, the existing UUID console routes and UUID
API contracts continue to work. After slug cutover, UUID-shaped console URLs
are not-found without redirects; UUID API paths, headers, and identities remain
supported. The slug work adds a resolution layer instead of changing platform
identity.

## 3. Users, problem, and outcomes

The primary users are people who share console links with teammates, bookmark
an operating workspace, or use a URL while diagnosing an incident. Organization
admins also need a stable address for settings and workspace administration.
Platform operators need the address to remain unambiguous while objects are
renamed, removed, or recreated. Provider authors need a reliable resolved
tenant context without having to understand URL parsing.

Today, a link such as `/ui/4c.../b7.../providers` communicates almost nothing
to a human and is easy to copy incorrectly. Display names cannot solve this:
they are mutable, can collide, and may contain characters unsuitable for a
path. A slug should make the destination legible and stable while UUIDs keep
identity, API authorization, kcp addressing, and provider isolation precise.

Success means a user can create an organization or workspace, see a useful
editable suggestion, share its resulting URL, later rename the display name
without breaking the URL, and receive a clear unavailable/not-found state when
the target is absent or inaccessible. A copied link must never silently open a
different account, organization, or workspace.

## 4. Goals and non-goals

Goals:

- Add readable, immutable slugs to organization and workspace URLs.
- Guarantee hub-wide organization uniqueness and per-organization workspace
  uniqueness, including across deletion and recreation.
- Let users edit an initial suggestion during creation, while making the final
  slug explicit before submission.
- Preserve UUIDs in APIs, request headers, kcp logical-cluster identities, and
  provider context.
- Resolve and authorize a slug before private content or provider code mounts.
- Make migration, retries, conflicts, and temporary failures observable and
  recoverable.

Non-goals:

- Slugs are not usernames, handles for authentication, credentials, secrets, or
  authorization grants.
- Display names do not become unique, immutable, or the source of identity.
- This proposal does not rename kcp objects, UUIDs, API paths, provider routes,
  or persisted tenant identity.
- It does not provide compatibility redirects from old UUID URLs, aliases,
  user-controlled redirects, or automatic slug changes after creation.
- It does not copy slug authority into provider bundles or browser localStorage.

## 5. URL and slug behavior

An organization URL is `/ui/<orgSlug>/...`; a workspace URL is
`/ui/<orgSlug>/<workspaceSlug>/...`. For example, an Acme organization with a
`production` workspace uses `/ui/acme/production/providers`; its organization
settings use `/ui/acme/settings/organizations`, and its workspace settings use
`/ui/acme/settings/workspaces/production`. Provider asset paths keep their
existing base path; only navigation paths become slug-scoped.

A valid slug is one to 63 ASCII characters consisting of lowercase letters,
digits, and single hyphens between segments. UUID-shaped values are rejected as
console scopes. The reserved words `login`, `auth`, `organizations`, `bonkers`,
`providers`, and `settings` are unavailable at both scopes. Invalid input and a
collision produce distinct, actionable errors; the allocator never silently
substitutes a different value for an explicitly requested slug.

Reservations are permanent. Deleting an organization or workspace makes its
binding unavailable for resolution, but its slug is never reused. A stale link
therefore cannot begin addressing a newly created object. A missing or denied
slug has a generic not-found/unavailable result so the console does not disclose
whether an inaccessible object exists. There are no redirects from UUID-shaped
console scopes or from a prior slug. A UUID-shaped console link is therefore
treated as an invalid/not-found console route, even though UUID API paths remain
valid.

## 6. Creation, settings, and suggestions

The organization and workspace creation forms show a suggested slug, an
editable slug field, and a full URL preview. They explain that the slug is
permanent after creation. The display name drives the suggestion until the user
edits the slug manually. The form always submits the slug currently shown.
Availability checks are debounced and fenced against stale responses; a check
is advisory, while the create request is authoritative and may return a
conflict if another request wins the race.

The form makes its asynchronous states visible: editing can show a pending
check, the current candidate can be valid and available or invalid/unavailable,
submission can be in progress, and a conflict or temporary failure can be
retried without losing the user's display name or chosen slug. A stale check
must not replace the result for the current candidate. Route resolution has
equivalent loading, pending-provisioning, unavailable, and retryable-error
states, and private content remains unmounted until resolution is authoritative.
A cross-tab storage update must not replace the active document's URL scope or
its resolved authority.

Suggestions derive from the display name by NFKD normalization, removal of
combining marks, lowercasing, replacement of other runs with a hyphen, trimming,
and truncation to the limit. If the normalized name is empty, use the relevant
organization or workspace fallback. Automatic allocation adds `-2`, `-3`, and
later suffixes when needed. A personal organization suggestion comes from the
user display name, with no email fallback; if it produces no usable base, the
fallback is `personal`. Its initial workspace suggestion is `default`. An
omitted CLI `--slug` follows the same automatic allocation; an optional
`--slug` requests an explicit value.

After creation, settings show a read-only slug and a copyable URL. Editing a
display name must not alter that URL. Any update request that includes a slug
is rejected, making immutability visible to API clients as well as the UI.

## 7. Proposed system contract

The hub owns slug validation, suggestion, durable allocation, and lookup in a
new `pkg/hub/slugs` package. Its proposed `NewStore(client.Client) *Store`
accepts an uncached controller-runtime client scoped to `root:railgrid:users`.
The store exposes `Allocate(ctx, target, displayName, requested)`,
`Get(ctx, target)`, `Resolve(ctx, kind, orgUUID, slug)`,
`Suggest(ctx, kind, orgUUID, displayName)`, and
`Available(ctx, kind, orgUUID, slug)`, plus `Validate` and `Base`. An empty
`requested` value asks `Allocate` to choose a candidate. Targets identify a
kind (`organization` or `workspace`), the organization UUID when applicable,
and the target UUID. Returned bindings contain the target and slug. Exported
`ErrInvalid`, `ErrConflict`, and `ErrNotFound` sentinels, alongside a distinct
storage error, let HTTP and CLI surfaces preserve the distinction.

Durability uses two central, immutable records: a
`TenantSlugBinding` keyed by target identity and a `TenantSlugReservation`
keyed by scope and slug. Neither uses owner references. Allocation reserves the
candidate and then atomically creates the binding; both records must agree
before resolution succeeds. A reservation owned by another target is a
conflict. Same-target retries are idempotent. A losing or
interrupted reservation remains reserved but does not resolve, preventing reuse
while allowing reconciliation to repair a missing binding. No tenant can read,
write, or list these central records directly; their specs are immutable.

Existing organization and workspace list, create, and detail responses gain a
`slug` field. Create accepts an optional slug; PATCH rejects a slug field. UUID
paths and UUID headers remain supported and authoritative. A new user-only
`GET /api/console/context?org=<slug>&workspace=<slug>` resolves the optional
workspace and returns organization and workspace views with UUIDs. It ignores
tenant headers, uses the existing tenant authorization policy, returns generic
not-found for unknown or denied contexts, returns 401 when unauthenticated,
sets `no-store`, rejects soft-deleted operating contexts, and leaves genuinely
temporary failures retryable.

A user-only `POST /api/console/slug-check` accepts `kind`, optional `orgUUID`,
`displayName`, and optional `slug`, returning `suggestedSlug`, `available`,
`valid`, and an optional message. An omitted candidate checks the suggestion.
Invalid candidates use a 200 response with `valid: false`; malformed requests
use 400. Workspace checks use the same create permission as workspace creation.
The endpoint is advisory and cannot reserve a slug; create remains the
authority and returns 409 for a collision.

The router first resolves slug context to authorized UUIDs, retains both forms
in host-owned state, and only then mounts private content. Its
`NavigationScope` carries nullable `orgSlug` and `workspaceSlug` values while
the resolved context carries the UUIDs separately. The document-local
context accessor crosses independently bundled PortalKit copies through a
shared global symbol and invalidates synchronously on logout or account change.
Provider helpers receive resolved UUIDs and continue using the host-owned fetch;
they never place a slug in UUID headers or infer authority from localStorage.
The provider's navigation base path may include the slug, while its asset base
path and service authorization remain unchanged.

Workspace settings recovery uses the authorized workspace list as its source of
truth. After a refresh or a soft-delete transition, a stale detail selection is
restored only when the authorized list contains that workspace and it is not
pending deletion. If the row is absent or pending deletion, the console shows
the unavailable state and returns the user to an authorized settings/list
context; stale local state never resurrects a deleted workspace.

## 8. Backfill and lifecycle

An ordered, retryable backfill reads authoritative organization creation time
then UUID, followed by workspaces in the same order. Soft-deleted records are
included so their reservations remain permanent. Backfill binds central records;
it does not rename kcp resources or mutate existing IDs. New organization and
workspace creation and bootstrap route through the allocator and cannot overtake
the initial migration. A readiness gate reports when migration is complete.
During migration, UUID APIs remain usable. Re-running a batch is safe because
the target identity is stable and allocation is idempotent. A failed batch is
retryable; it must not produce a partial user-visible slug resolution.

## 9. Rollout, rollback, and operational states

Rollout should proceed in separable phases: introduce central schemas and
allocator self-checks; backfill and expose response fields; add context and
availability endpoints; verify provider compatibility; then cut over the host
router, forms, and provider navigation together. Before cutover, backend
staging preserves the current UUID console routes and UUID APIs. Each phase
must have metrics for migration progress, allocation conflicts, unresolved
bindings, context authorization failures, and temporary storage errors.
Operators need a readiness view that distinguishes pending migration from a
durable failure.

The cutover gate must verify that every participating provider receives the
resolved UUID context through the host-owned fetch, keeps UUID headers and API
calls, and uses the slug only for navigation. The host and provider bundles
must be released and checked as a compatible set, including not-found,
pending-provisioning, logout, stale-response, and cross-tab behavior. After
cutover, UUID-shaped console URLs are not-found with no redirects; UUID APIs
remain available.

If backend staging is unhealthy, stop before cutover and continue the existing
UUID console behavior. If a post-cutover rollback is required, roll back to a
matching slug-aware host/provider release and preserve every binding and
reservation. On the first slug release, if no previously verified slug-aware
release exists, keep the affected console surface unavailable while repairing
forward; do not silently re-enable UUID console routes. Rollback must never
delete reservations, rewrite bindings, or
reuse a slug. A later retry can resume from the durable records. Any release
that changes the resolver or frontend must retain generic not-found behavior
and the route-context stale-response and logout fences.

## 10. Acceptance and verification

Acceptance requires unit and integration coverage for syntax, reserved words,
UUID rejection, normalization, suffix allocation, concurrent allocation,
idempotent retries, permanent reservations, interrupted reservations,
cross-organization workspace collisions, deletion, and backfill ordering. HTTP
coverage must prove user-only authorization, generic unknown-versus-denied
responses, no-store, conflict handling, malformed input, temporary retryable
failures, UUID API compatibility, and PATCH immutability.

Portal coverage must exercise editable suggestions, manual edits, full URL
preview, stale availability responses, conflict recovery, read-only settings,
copy URL, personal organization defaults, and CLI omission/explicit slug
behavior. Router and provider checks must cover two tabs, logout, account
change, stale responses, query strings and hashes, UUID-shaped old routes,
not-found and pending states, organization-only settings, workspace settings,
and provider context retaining UUIDs after slug resolution. Full portal
typecheck/build and the shared design and PortalKit gates are required for an
implementation. Where the local environment is used for validation, browser
evidence must show the actual target state and persistence rather than a
simulated ready indicator.

## 11. Risks and safeguards

Readable names reveal meaning in URLs. This is a privacy tradeoff: a slug can
expose an organization or workspace theme to anyone who can see a link, so the
creation UI should explain that choice. Slugs reduce UUID exposure in console
paths; they do not make names confidential or remove UUIDs from authenticated
APIs, headers, kcp identity, or resolved provider context. Slugs are
identifiers, not credentials; authorization and bearer-token checks remain
independent.

The main correctness risks are collision races, stale browser context, and
accidentally allowing a provider or localStorage value to override the URL.
Durable reservation records, authoritative create-time checks, synchronous
invalidation, pre-mount resolution, and host-owned provider fetches address
those risks. Permanent reservations trade a growing small record set for the
strong guarantee that a historical link never changes its target.
