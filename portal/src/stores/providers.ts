import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { authFetch } from '@/auth/session'

// ProviderDTO is the wire shape returned by the hub's GET /api/providers.
// Keep it aligned with pkg/hub/providers/api.go:providerDTO.
// ProviderScope mirrors pkg/hub/providers.ScopeGlobal / ScopeOrg.
// 'global' = a platform provider faros operates; 'org' = one this
// organization registered and runs itself ("bring your own").
export type ProviderScope = 'global' | 'org'

export interface ProviderDTO {
  name: string
  // Absent on responses from an older hub; treat a missing value as
  // 'global' so the catalog still renders during a rolling upgrade.
  scope?: ProviderScope
  // UUID of the owning organization when scope === 'org'.
  ownerOrg?: string
  displayName: string
  // Short "what is this" blurb from CatalogEntry.spec.description. The
  // catalog cards and the first-run welcome flow render it; may be absent
  // on entries that declare none.
  description?: string
  version?: string
  ready: boolean
  // Present only when ready is false. The hub owns and sanitizes these
  // explanations so provider transport details never reach the browser.
  readinessReason?: string
  readinessMessage?: string
  hasUI: boolean
  hasBackend: boolean
  iconURL?: string
  // True when the provider requests background access to the workspace's
  // edge clusters (verb "proxy" on edges) on Enable. Rendered in the
  // Enable confirmation dialog alongside permission claims.
  edgeProxyAccess?: boolean
  // When set, the portal renders this Vue Router route name in-tree
  // instead of loading /main.js. First-party providers (mcp, kubernetes-
  // edges, server-edges) use this to surface their existing SPA pages
  // through the uniform providers list.
  builtinRoute?: string
  // Sub-nav entries the side nav renders indented under this provider.
  // Used by providers that span multiple SPA pages (e.g. Kubernetes →
  // Workloads).
  children?: NavChildDTO[]
  // Optional grouping key. Matched against CategoryDTO[].name to render
  // a section header in the side nav and catalog page. Empty/missing →
  // entry appears at the top level under "Providers".
  category?: string
  // Providers that must be enabled in the current workspace before this
  // provider can be enabled.
  dependencies?: ProviderDependencyDTO[]
  // Populated when the provider declares spec.apiExport. The portal uses
  // these coordinates to build the APIBinding it POSTs into the tenant
  // workspace on Enable.
  apiExportPath?: string
  apiExportName?: string
  permissionClaims?: PermissionClaim[]
  // Builtin = true for first-party providers shipped with the hub
  // binary, regardless of how they surface UI (legacy builtinRoute or
  // new custom-element via embedded assets). Side-nav skips the
  // APIBinding-required gate for these.
  builtin?: boolean
  // True when the provider publishes enough deployment metadata for an
  // organization to run its own copy. Drives the Self-Hosting tab.
  selfHostable?: boolean
  // Provider-specific setup guidance for self-hosting.
  selfHostingDocsURL?: string
}

export interface ProviderDependencyDTO {
  name: string
}

// EnabledProviderDetail mirrors pkg/hub/restapi.EnabledProviderDetail — which
// APIExport a workspace's binding actually points at.
export interface EnabledProviderDetail {
  bindingName: string
  exportPath: string
  // true when the binding targets this org's own instance rather than the
  // platform's.
  selfHosted: boolean
  // Claims still pointing at a different copy of a dependency than this
  // workspace uses — what every dependent looks like after that dependency is
  // swapped for a self-hosted one. Absent in the healthy case.
  staleClaims?: StaleClaim[]
  // true when the provider was disabled but kcp is still cascade-deleting the
  // bound APIs' resources — the binding stays live (and listed) until then.
  terminating?: boolean
  // kcp's explanation of what is holding a terminating binding open (leftover
  // CR finalizers, failed deletes). A binding in this state never finishes
  // disabling on its own. Absent while deletion progresses normally.
  deletionBlocked?: string
}

// StaleClaim mirrors pkg/hub/restapi.StaleClaim. kcp reports a binding with a
// mispointed claim as perfectly healthy while serving none of the claimed
// resources, so this is the only place a user can see it before the dependent
// provider starts failing for reasons that look unrelated.
export interface StaleClaim {
  group: string
  resource: string
  // The copy this workspace actually uses, and so the one the provider would
  // have to be repointed at.
  boundExportPath: string
  claimedIdentity: string
  boundIdentity: string
  // true when re-enabling this provider repairs the pin — only possible when
  // the org owns the provider's APIExport. A platform provider's export is
  // shared by every org, so re-enabling changes nothing.
  repointable: boolean
}

// CategoryDTO mirrors pkg/hub/providers.Category — the hub publishes its
// canonical category registry so the portal renders matching nav headers
// without hard-coding the list.
export interface CategoryDTO {
  name: string
  icon?: string // lucide-vue-next component name, resolved client-side
  order?: number
}

// NavChildDTO mirrors pkg/hub/providers.NavChild — a sub-nav entry the
// portal renders indented under its parent provider.
export interface NavChildDTO {
  displayName: string
  builtinRoute: string
}

export interface PermissionClaim {
  group?: string
  resource: string
  verbs?: string[]
  tenantScoped?: boolean
}

export type ProviderBindingsLoadState = 'idle' | 'loading' | 'ready' | 'error'

interface ProvidersResponse {
  items: ProviderDTO[]
  categories?: CategoryDTO[]
}

export const useProvidersStore = defineStore('providers', () => {
  const items = ref<ProviderDTO[]>([])
  const categories = ref<CategoryDTO[]>([])
  const loaded = ref(false)
  const loading = ref(false)
  const error = ref<string | null>(null)
  // CatalogEntry responses are organization-scoped: an org's own provider
  // registration can shadow a platform entry with the same name. Keep the
  // scope alongside the data so a late response from the previous org cannot
  // repopulate the new org's catalog.
  const catalogOrgUUID = ref<string | null>(null)
  let catalogRequestSequence = 0
  let catalogRequestOrg: string | null = null

  // bindingNamesByProvider maps provider-name → kcp APIBinding name in the
  // user's tenant workspace. Empty when the provider is not enabled for
  // this user. Used by the Disable button and the catalog status badge.
  const bindingNamesByProvider = ref<Record<string, string>>({})

  // bindingsByProvider says WHICH instance of a provider a workspace is bound
  // to, not just that it is bound. Once an org self-hosts a provider the
  // platform also ships, both compete for one name, and a workspace bound
  // before the switch still points at the platform export — rendering that as
  // plain "Enabled" would tell the user they are running their own instance
  // when they are not.
  const bindingsByProvider = ref<Record<string, EnabledProviderDetail>>({})

  // bindingsWorkspace records which workspace the map above was fetched for,
  // and is the only safe way to read an *empty* map as "nothing is enabled
  // here". Two windows make emptiness ambiguous otherwise: load() flips
  // `loaded` before awaiting refreshBindings. A same-scope refresh intentionally
  // retains the last-known map while it is loading or has errored; an org,
  // workspace, or organization-only transition clears it before the request.
  // Callers that branch on emptiness (the first-run welcome flow) must check
  // this matches the active workspace; callers that only read individual
  // entries (the sidebar, the Disable button) can pair the map with
  // `bindingsStale` when they need to explain that it is non-authoritative.
  const bindingsWorkspace = ref<string | null>(null)
  // Binding reads are a separate finite state machine from the catalog read.
  // ProviderFrame must not interpret an empty map as "unbound" until this
  // projection is ready for the active org/workspace. The request target is
  // retained on errors so a failure for an old workspace cannot be displayed
  // as the current workspace's failure.
  const bindingsLoadState = ref<ProviderBindingsLoadState>('idle')
  const bindingsError = ref<string | null>(null)
  const bindingsOrgUUID = ref<string | null>(null)
  const bindingsRequestOrgUUID = ref<string | null>(null)
  const bindingsRequestWorkspaceUUID = ref<string | null>(null)
  // True while loading or retrying an established projection for the same
  // tenant scope. Consumers can keep rendering the last-known navigation set
  // while showing a non-authoritative refresh/error affordance. A projection
  // from another org/workspace is never marked stale: it is cleared before
  // the new request starts.
  const bindingsStale = ref(false)
  let bindingRequestSequence = 0
  // Provider enables/disables are long-lived writes (the hub may ask us to
  // retry while it provisions the provider workspace). Keep a generation per
  // provider, rather than one global counter, so an enable for one provider
  // cannot supersede an unrelated write for another provider.
  const enableRequestGenerations = new Map<string, number>()
  const disableRequestGenerations = new Map<string, number>()
  // Clearing the per-provider maps on an org reset must also invalidate an
  // action that captured a generation before the clear. Otherwise a new
  // action for the same provider could reuse the same number while a stale
  // completion is still in flight.
  let providerActionEpoch = 0

  // ProviderNavItem captures one provider entry plus its declared sub-nav.
  // children[] carries the routes the side-nav renders indented under the
  // parent. Used by both the flat AppLayout (bar/floating modes — children
  // get flattened) and the tree layout (vertical sidebar).
  type ProviderNavItem = {
    name: string
    label: string
    to: string
    iconURL: string | null
    version: string
    builtin: boolean
    category: string
    children: { label: string; to: string }[]
  }

  // enabledNavItems is the list of providers that should show up in the
  // side nav. Two paths to inclusion:
  //  - Built-in providers (spec.ui.builtinRoute set) always appear — they
  //    ship as part of the portal and don't need a per-user APIBinding.
  //  - Third-party providers appear only when ready, with a UI, AND the
  //    current user has bound their APIExport.
  // The `to` distinguishes them: builtins route to /{builtinRoute},
  // third-party to /providers/{name}.
  const enabledNavItems = computed<ProviderNavItem[]>(() =>
    items.value
      .filter((p) => {
        if (!p.ready || !p.hasUI) return false
        // Legacy in-tree route OR new-style first-party provider:
        // always shown, no binding required.
        if (p.builtinRoute || p.builtin) return true
        return !!bindingNamesByProvider.value[p.name]
      })
      .map((p) => {
        // builtinRoute → in-tree SPA route (legacy).
        // builtin (no route) → ProviderFrame at /providers/{name}.
        // third-party → ProviderFrame at /providers/{name}.
        const parentTo = p.builtinRoute ? `/${p.builtinRoute}` : `/providers/${p.name}`
        return {
          name: p.name,
          label: p.displayName,
          to: parentTo,
          iconURL: p.iconURL ?? null,
          version: p.version ?? '',
          builtin: !!p.builtinRoute || !!p.builtin,
          category: p.category ?? '',
          // Child routes nest UNDER the parent for new-style providers
          // (so kubernetes-edges' Workloads child lands at
          // /providers/kubernetes-edges/workloads), while legacy
          // builtinRoute providers keep their top-level child URLs
          // (/workloads).
          children: (p.children ?? []).map((c) => ({
            label: c.displayName,
            to: p.builtinRoute ? `/${c.builtinRoute}` : `${parentTo}/${c.builtinRoute}`,
          })),
        }
      }),
  )

  // categorizedNavItems groups enabledNavItems by category for the
  // sidebar's tree layout. Output is sorted by:
  //  1. categories with a registry entry first, by their declared order
  //  2. then ad-hoc category names (alphabetical) — third-party
  //     providers can put themselves in arbitrary categories and we still
  //     show them; they just don't get a registered icon
  //  3. uncategorized items last, under no header (rendered flat)
  // Within each group, items are sorted alphabetically by label.
  const categorizedNavItems = computed(() => {
    const groups = new Map<string, ProviderNavItem[]>()
    const uncategorized: ProviderNavItem[] = []
    for (const it of enabledNavItems.value) {
      if (!it.category) {
        uncategorized.push(it)
        continue
      }
      const arr = groups.get(it.category) ?? []
      arr.push(it)
      groups.set(it.category, arr)
    }

    const known = new Map<string, CategoryDTO>()
    for (const c of categories.value) known.set(c.name, c)

    const orderedNames = [...groups.keys()].sort((a, b) => {
      const ka = known.get(a)
      const kb = known.get(b)
      if (ka && !kb) return -1
      if (!ka && kb) return 1
      if (ka && kb) return (ka.order ?? 0) - (kb.order ?? 0) || a.localeCompare(b)
      return a.localeCompare(b)
    })

    const out: Array<{
      name: string
      icon: string | null // lucide component name, or null for ad-hoc categories
      items: ProviderNavItem[]
    }> = []
    for (const name of orderedNames) {
      const arr = groups.get(name)!.slice().sort((a, b) => a.label.localeCompare(b.label))
      out.push({ name, icon: known.get(name)?.icon ?? null, items: arr })
    }
    uncategorized.sort((a, b) => a.label.localeCompare(b.label))
    return { groups: out, uncategorized }
  })

  function isEnabled(name: string): boolean {
    return !!bindingNamesByProvider.value[name]
  }

  // isSelfManaged distinguishes providers this organization registered and runs
  // itself from the platform catalog. A hub that predates provider scoping omits
  // `scope` entirely, so anything unset counts as platform-managed.
  function isSelfManaged(p: ProviderDTO): boolean {
    return p.scope === 'org'
  }

  // selfManaged is the organization's own providers, alphabetical. The catalog
  // page renders these in their own section above the platform catalog: they are
  // operated by the org's own team, so "who do I ask when this breaks" is a
  // different answer than for a platform provider, and that distinction is worth
  // more to a user than any category grouping.
  const selfManaged = computed<ProviderDTO[]>(() =>
    items.value
      .filter(isSelfManaged)
      .slice()
      .sort((a, b) => a.displayName.localeCompare(b.displayName)),
  )

  // selfHostable is the platform catalog filtered to providers that publish a
  // deployment recipe — what the Self-Hosting tab offers to run yourself.
  const selfHostable = computed<ProviderDTO[]>(() =>
    items.value
      .filter((p) => p.selfHostable && !isSelfManaged(p))
      .slice()
      .sort((a, b) => a.displayName.localeCompare(b.displayName)),
  )

  // enableable is the set of providers the user can actually turn on in the
  // current workspace: ready, and declaring an APIExport to bind. Everything
  // else in the catalog is either still starting up or shows up unconditionally
  // (built-ins), so neither belongs in a "what can I switch on" list.
  //
  // Ordering matches the catalog page: registry categories by declared order,
  // then ad-hoc categories alphabetically, then uncategorized — which puts the
  // foundational ones (Edges, AI) in front of the long tail. The welcome flow
  // leans on that to present a sane reading order without its own ranking.
  const enableable = computed<ProviderDTO[]>(() => {
    const known = new Map(categories.value.map((c) => [c.name, c]))
    const rank = (name: string) => {
      const c = known.get(name)
      // Unknown/ad-hoc categories sort after every registered one, and
      // uncategorized entries after those.
      if (!name) return Number.MAX_SAFE_INTEGER
      return c ? (c.order ?? 0) : Number.MAX_SAFE_INTEGER - 1
    }
    return items.value
      .filter((p) => p.ready && !!p.apiExportName)
      .slice()
      .sort((a, b) => {
        const ca = a.category ?? ''
        const cb = b.category ?? ''
        return (
          rank(ca) - rank(cb) ||
          ca.localeCompare(cb) ||
          a.displayName.localeCompare(b.displayName)
        )
      })
  })

  // hasAnyEnabled answers "has this workspace been set up at all?". False for a
  // fresh workspace, which is what triggers the welcome flow on the dashboard.
  const hasAnyEnabled = computed(() => Object.keys(bindingNamesByProvider.value).length > 0)

  function isDependencySatisfied(name: string): boolean {
    const p = byName(name)
    if (!p) return isEnabled(name)
    if (!p.ready) return false
    if (!p.apiExportName || p.builtinRoute || p.builtin) return true
    return isEnabled(p.name)
  }

  function missingDependencies(p: ProviderDTO): string[] {
    return (p.dependencies ?? [])
      .map((dep) => dep.name.trim())
      .filter((name) => name && !isDependencySatisfied(name))
  }

  function hasMissingDependencies(p: ProviderDTO): boolean {
    return missingDependencies(p).length > 0
  }

  // staleClaims lists this provider's claims that still point at a copy of a
  // dependency the workspace no longer uses — the state a provider is left in
  // when a dependency underneath it is swapped for a self-hosted one.
  //
  // Worth showing prominently despite looking like a detail: kcp keeps
  // reporting such a provider as Enabled and healthy, so nothing else in the UI
  // distinguishes it from one that works.
  // isDisabling / deletionBlocked expose the Disable-in-progress state. kcp
  // deletes an APIBinding asynchronously (all CRs of the bound APIs go first),
  // so after a Disable the binding can linger — indefinitely, when a leftover
  // CR finalizer's controller is gone. Without these the catalog re-renders
  // such a binding as plain Enabled and the Disable button looks broken.
  function isDisabling(name: string): boolean {
    return !!bindingsByProvider.value[name]?.terminating
  }

  function deletionBlocked(name: string): string {
    return bindingsByProvider.value[name]?.deletionBlocked ?? ''
  }

  function staleClaims(name: string): StaleClaim[] {
    return bindingsByProvider.value[name]?.staleClaims ?? []
  }

  function hasStaleClaims(name: string): boolean {
    return staleClaims(name).length > 0
  }

  function dependencyLabel(name: string): string {
    return byName(name)?.displayName ?? name
  }

  function dependencyLabels(names: string[]): string[] {
    return names.map((name) => dependencyLabel(name))
  }

  function clearBindingProjection() {
    bindingNamesByProvider.value = {}
    bindingsByProvider.value = {}
    bindingsWorkspace.value = null
    bindingsOrgUUID.value = null
    bindingsStale.value = false
  }

  function clearBindings(invalidate = true) {
    if (invalidate) bindingRequestSequence++
    clearBindingProjection()
    bindingsLoadState.value = 'idle'
    bindingsError.value = null
    bindingsRequestOrgUUID.value = null
    bindingsRequestWorkspaceUUID.value = null
  }

  // Drop every org-scoped catalog value before starting the next org's read.
  // In-flight requests are fenced by catalogRequestSequence, so a slow old
  // response cannot restore the previous organization's providers.
  function resetForOrganization() {
    catalogRequestSequence++
    providerActionEpoch++
    enableRequestGenerations.clear()
    disableRequestGenerations.clear()
    catalogRequestOrg = null
    loading.value = false
    loaded.value = false
    catalogOrgUUID.value = null
    items.value = []
    categories.value = []
    error.value = null
    clearBindings()
  }

  async function load(requestedOrgUUID?: string | null) {
    // App.vue passes the reactive tenant selection when an org switch is
    // already in flight. Falling back to localStorage keeps existing callers
    // (including the initial auth bootstrap) compatible while ensuring a
    // same-tick switch cannot start a request under the previous org.
    const targetOrgUUID = requestedOrgUUID === undefined
      ? readTenantSelection().orgUUID
      : requestedOrgUUID
    const hasExplicitOrg = requestedOrgUUID !== undefined
    if (loading.value && catalogRequestOrg === targetOrgUUID) return
    const requestSequence = ++catalogRequestSequence
    catalogRequestOrg = targetOrgUUID
    loading.value = true
    error.value = null
    try {
      // The catalog is org-scoped, but this request may begin in the same
      // tick as the tenant store's persistence watcher. Pass the captured
      // org directly instead of asking authFetch to read potentially stale
      // localStorage tenant headers.
      const res = await authFetch('/api/providers', {
        headers: targetOrgUUID ? { 'X-Faros-Org': targetOrgUUID } : undefined,
      })
      if (!res.ok) {
        throw new Error(`provider list failed: ${res.status} ${res.statusText}`)
      }
      const body = (await res.json()) as ProvidersResponse
      if (
        requestSequence !== catalogRequestSequence ||
        (!hasExplicitOrg && readTenantSelection().orgUUID !== targetOrgUUID)
      ) return
      items.value = body.items ?? []
      categories.value = body.categories ?? []
      catalogOrgUUID.value = targetOrgUUID
      loaded.value = true
      // Best-effort: also refresh the user's enabled set. Failure here
      // doesn't block the catalog from rendering.
      // During an org watcher flush, localStorage can still carry the old
      // workspace selection. Do not issue a binding request under that stale
      // scope; the auth-cluster watcher will refresh once the new workspace
      // selection is persisted.
      const currentSelection = readTenantSelection()
      if (currentSelection.orgUUID === targetOrgUUID && currentSelection.workspaceMode !== 'organization') {
        await refreshBindings().catch(() => {
          /* surfaced via Disable button being unavailable */
        })
      }
    } catch (e) {
      if (
        requestSequence !== catalogRequestSequence ||
        (!hasExplicitOrg && readTenantSelection().orgUUID !== targetOrgUUID)
      ) return
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      if (requestSequence === catalogRequestSequence) {
        loading.value = false
        catalogRequestOrg = null
      }
    }
  }

  // refreshBindings hits the server-side endpoint
  // GET /api/orgs/{org}/workspaces/{ws}/providers/enabled
  // which lists APIBindings via the hub's kcp-admin client and returns
  // the provider-name → binding-name map directly.
  //
  // Previously this POST'd to /clusters/{cluster}/apis/.../apibindings,
  // but the kcp user-proxy enforces User.Spec.DefaultCluster BEFORE
  // forwarding — sibling workspaces 403'd silently and the sidebar's
  // enabled-set stayed stuck on the boot-time snapshot. Going through
  // the REST endpoint lets the bootstrapper read as kcp-admin in the
  // target workspace, with tenant.Middleware verifying the caller's
  // Membership upstream.
  async function refreshBindings() {
    const t = readTenantSelection()
    // Every caller gets its own authoritative read. In particular, concurrent
    // successful provider writes must each trigger a post-write resync; a
    // same-target deduplication here would let a later completion merely join
    // an earlier request that started before its write was applied.
    const requestSequence = ++bindingRequestSequence
    bindingsRequestOrgUUID.value = t.orgUUID
    bindingsRequestWorkspaceUUID.value = t.workspaceUUID
    bindingsError.value = null
    // Keep an established projection only while re-reading the exact same
    // org/workspace. This is the last-known navigation state and is safe to
    // retain as explicitly non-authoritative while loading or on error. Any
    // authority boundary (org, workspace, or organization-only mode) must
    // clear before the request so the old binding set cannot leak.
    const retainProjection = !!t.orgUUID && !!t.workspaceUUID &&
      bindingsOrgUUID.value === t.orgUUID &&
      bindingsWorkspace.value === t.workspaceUUID
    if (!retainProjection) clearBindingProjection()
    bindingsStale.value = retainProjection
    // Organization-only mode is intentionally workspace-free. Even if an
    // older tab has not observed the synchronous tenant normalization yet,
    // never probe its stale workspace for enabled bindings.
    if (t.workspaceMode === 'organization' || !t.orgUUID || !t.workspaceUUID) {
      bindingsLoadState.value = 'idle'
      return
    }

    bindingsLoadState.value = 'loading'
    const url = `/api/orgs/${encodeURIComponent(t.orgUUID)}/workspaces/${encodeURIComponent(t.workspaceUUID)}/providers/enabled`
    try {
      const res = await authFetch(url, { tenant: true })
      if (requestSequence !== bindingRequestSequence) return
      if (!sameTenantSelection(t, readTenantSelection())) return
      if (!res.ok) throw new Error(`list enabled providers: ${res.status}`)
      const body = (await res.json()) as {
        bindingNamesByProvider?: Record<string, string>
        bindingsByProvider?: Record<string, EnabledProviderDetail>
      }
      if (requestSequence !== bindingRequestSequence) return
      if (!sameTenantSelection(t, readTenantSelection())) return
      bindingNamesByProvider.value = body.bindingNamesByProvider ?? {}
      bindingsByProvider.value = body.bindingsByProvider ?? {}
      bindingsOrgUUID.value = t.orgUUID
      bindingsWorkspace.value = t.workspaceUUID
      bindingsStale.value = false
      bindingsLoadState.value = 'ready'
    } catch (e: unknown) {
      // A late failure belongs to the request's captured tenant, not to the
      // workspace now on screen. Keep the active projection untouched and
      // resolve quietly when either the request or tenant selection is stale.
      if (requestSequence !== bindingRequestSequence) return
      if (!sameTenantSelection(t, readTenantSelection())) return
      bindingsLoadState.value = 'error'
      bindingsError.value = e instanceof Error ? e.message : String(e ?? 'Failed to list provider bindings.')
      // Keep the established Promise rejection contract: callers that use
      // refreshBindings directly still receive a current-scope failure, while
      // stale requests are quiet and the finite projection lets UI consumers
      // render a truthful Retry.
      throw e
    }
  }

  // enable hits the server-side endpoint
  // POST /api/orgs/{org}/workspaces/{ws}/providers/{name}/enable
  // which creates the APIBinding via the hub's kcp-admin client.
  //
  // The old implementation POST'd directly to
  // /clusters/{cluster}/apis/apis.kcp.io/v1alpha2/apibindings, but the
  // hub's user-facing kcp proxy enforces User.Spec.DefaultCluster
  // BEFORE forwarding to kcp — every sibling workspace (anything that
  // isn't the user's default) 403'd with "cluster access denied"
  // regardless of the per-workspace RBAC commit #220 grants. Going
  // through the REST endpoint lets the bootstrapper write the binding
  // as kcp-admin on the user's behalf, with the membership check
  // happening at the tenant.Middleware layer.
  //
  // `accept` is the list of permission claims the user explicitly
  // accepted in the confirmation dialog. The server merges this with
  // the provider's declared claims — anything the user didn't accept
  // is sent to kcp as state=Rejected (which prevents the binding from
  // going Bound and surfaces the mismatch cleanly).
  async function enable(p: ProviderDTO, accept: PermissionClaim[]): Promise<void> {
    if (!p.apiExportPath || !p.apiExportName) {
      throw new Error(`${p.name}: provider declares no APIExport to bind`)
    }

    // Pull the sidebar selection straight from localStorage so we don't
    // take a dependency on @/stores/tenant (existing import-cycle
    // avoidance pattern in this file).
    const t = readTenantSelection()
    if (!t.orgUUID || !t.workspaceUUID) {
      throw new Error('select an organization and workspace before enabling a provider')
    }
    const enableRequestGeneration = (enableRequestGenerations.get(p.name) ?? 0) + 1
    enableRequestGenerations.set(p.name, enableRequestGeneration)
    const actionEpochAtStart = providerActionEpoch
    const isCurrentEnable = (): boolean =>
      actionEpochAtStart === providerActionEpoch &&
      enableRequestGenerations.get(p.name) === enableRequestGeneration &&
      sameTenantSelection(t, readTenantSelection())

    const body = {
      acceptedClaims: accept.map((c) => ({ group: c.group ?? '', resource: c.resource })),
    }
    const url = `/api/orgs/${encodeURIComponent(t.orgUUID)}/workspaces/${encodeURIComponent(t.workspaceUUID)}/providers/${encodeURIComponent(p.name)}/enable`

    try {
      // A provider that requests edge-proxy access can't be enabled until the
      // catalog controller has provisioned its workspace, so an Enable clicked
      // during that window returns 409 "retry shortly". That's a transient,
      // retryable signal (the enable endpoint is idempotent), so back off and
      // re-try a few times instead of surfacing an error the user has to clear
      // with a manual page refresh. Non-409 failures are terminal — throw at once.
      const backoffMs = [750, 1250, 1750, 2500, 3000] // ~9s total across 6 attempts
      for (let attempt = 0; ; attempt++) {
        // A workspace/org switch or a newer enable invalidates the old write.
        // Do not continue posting a request whose completion can no longer be
        // applied to the visible binding state.
        if (!isCurrentEnable()) return
        const res = await authFetch(url, {
          method: 'POST',
          tenant: true,
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        })
        if (res.ok) break
        const detail = await res.text().catch(() => '')
        if (res.status === 409 && attempt < backoffMs.length) {
          await new Promise((r) => setTimeout(r, backoffMs[attempt]))
          continue
        }
        if (!isCurrentEnable()) return
        throw new Error(`enable ${p.name} failed: ${res.status} ${res.statusText} ${detail}`)
      }
      if (!isCurrentEnable()) return
      // Re-read the server-owned binding projection instead of optimistically
      // mutating the map. The refresh is itself generation-fenced, and if the
      // selection changed while the write completed it cannot retarget the new
      // workspace or expose A's binding under B.
      await refreshBindings()
    } catch (e: unknown) {
      // A rejected request from an old tenant must not surface after a switch;
      // current-scope failures retain the existing rejection contract.
      if (!isCurrentEnable()) return
      throw e
    }
  }

  // readTenantSelection mirrors the storage shape written by
  // tenant.ts's savePersisted — kept inline to avoid an import cycle
  // with @/stores/tenant. Used for the enable/disable request *body*;
  // the org/workspace request *headers* come from authFetch({tenant:true}).
  function readTenantSelection(): {
    orgUUID: string | null
    workspaceUUID: string | null
    workspaceMode: 'workspace' | 'organization'
  } {
    try {
      const raw = localStorage.getItem('faros:portal:tenant')
      if (!raw) return { orgUUID: null, workspaceUUID: null, workspaceMode: 'workspace' }
      const parsed = JSON.parse(raw) as {
        orgUUID?: string | null
        workspaceUUID?: string | null
        workspaceMode?: 'workspace' | 'organization'
      }
      const orgUUID = parsed.orgUUID ?? null
      const storedWorkspaceUUID = parsed.workspaceUUID ?? null
      const workspaceMode = parsed.workspaceMode === 'organization' ||
        (parsed.workspaceMode === undefined && orgUUID !== null && storedWorkspaceUUID === null)
        ? 'organization'
        : 'workspace'
      return {
        orgUUID,
        workspaceUUID: workspaceMode === 'organization' ? null : storedWorkspaceUUID,
        workspaceMode,
      }
    } catch {
      return { orgUUID: null, workspaceUUID: null, workspaceMode: 'workspace' }
    }
  }

  function sameTenantSelection(
    left: ReturnType<typeof readTenantSelection>,
    right: ReturnType<typeof readTenantSelection>,
  ): boolean {
    return left.orgUUID === right.orgUUID &&
      left.workspaceUUID === right.workspaceUUID &&
      left.workspaceMode === right.workspaceMode
  }

  async function disable(p: ProviderDTO): Promise<void> {
    // Capture the tenant before consulting the binding map. The map is only
    // authoritative for the exact org/workspace that produced it; reading a
    // name before this check can post a disable for workspace A after the user
    // has already switched to workspace B.
    const t = readTenantSelection()
    if (!t.orgUUID || !t.workspaceUUID) {
      throw new Error('select an organization and workspace before disabling a provider')
    }
    const bindingProjectionCurrent =
      bindingsLoadState.value === 'ready' &&
      bindingsOrgUUID.value === t.orgUUID &&
      bindingsWorkspace.value === t.workspaceUUID &&
      bindingsRequestOrgUUID.value === t.orgUUID &&
      bindingsRequestWorkspaceUUID.value === t.workspaceUUID
    if (!bindingProjectionCurrent || !sameTenantSelection(t, readTenantSelection())) return

    const bindingName = bindingNamesByProvider.value[p.name]
    if (!bindingName) return
    const disableRequestGeneration = (disableRequestGenerations.get(p.name) ?? 0) + 1
    disableRequestGenerations.set(p.name, disableRequestGeneration)
    const actionEpochAtStart = providerActionEpoch
    const isCurrentDisable = (): boolean =>
      actionEpochAtStart === providerActionEpoch &&
      disableRequestGenerations.get(p.name) === disableRequestGeneration &&
      sameTenantSelection(t, readTenantSelection())

    // Disable = server-side endpoint (mirror of enable). It deletes the
    // APIBinding AND tears down the edge-proxy RBAC grant — the latter
    // needs kcp-admin credentials the tenant doesn't hold, so a direct
    // GraphQL deleteAPIBinding would leave the grant dangling.
    const url = `/api/orgs/${encodeURIComponent(t.orgUUID)}/workspaces/${encodeURIComponent(t.workspaceUUID)}/providers/${encodeURIComponent(p.name)}/disable`
    try {
      if (!isCurrentDisable()) return
      const res = await authFetch(url, { method: 'POST', tenant: true })
      if (!isCurrentDisable()) return
      // Idempotent server-side; 404 means the route target is already gone.
      if (!res.ok && res.status !== 404) {
        const detail = await res.text().catch(() => '')
        if (!isCurrentDisable()) return
        throw new Error(`disable ${p.name} failed: ${res.status} ${res.statusText} ${detail}`)
      }
      if (!isCurrentDisable()) return
      // Resync from the server rather than deleting the map entry locally: kcp
      // deletes the binding asynchronously (every CR of the bound APIs goes
      // first), so right after a 204 the binding usually still exists — now
      // flagged terminating, possibly with a deletionBlocked reason the card
      // must show. A local delete would render "disabled" for a binding that
      // can be stuck alive indefinitely. This also stops a bogus 404 (e.g. a
      // proxy answering instead of the hub) from silently reading as success.
      await refreshBindings()
    } catch (e: unknown) {
      // Selection changes invalidate the write. Treat that as a quiet no-op;
      // in particular, never surface A's failure or refresh B's bindings.
      if (!isCurrentDisable()) return
      throw e
    }
  }

  function byName(name: string): ProviderDTO | undefined {
    return items.value.find((p) => p.name === name)
  }

  return {
    items,
    categories,
    loaded,
    loading,
    error,
    catalogOrgUUID,
    bindingNamesByProvider,
    bindingsWorkspace,
    bindingsLoadState,
    bindingsError,
    bindingsOrgUUID,
    bindingsRequestOrgUUID,
    bindingsRequestWorkspaceUUID,
    bindingsStale,
    enabledNavItems,
    categorizedNavItems,
    enableable,
    selfManaged,
    selfHostable,
    bindingsByProvider,
    hasAnyEnabled,
    isEnabled,
    isSelfManaged,
    missingDependencies,
    hasMissingDependencies,
    staleClaims,
    hasStaleClaims,
    isDisabling,
    deletionBlocked,
    dependencyLabel,
    dependencyLabels,
    load,
    resetForOrganization,
    clearBindings,
    refreshBindings,
    enable,
    disable,
    byName,
  }
})
