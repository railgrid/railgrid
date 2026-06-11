import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { STORAGE_KEYS } from '@/lib/constants'
import { graphqlMutate } from '@/composables/useGraphQL'

// ProviderDTO is the wire shape returned by the hub's GET /api/providers.
// Keep it aligned with pkg/hub/providers/api.go:providerDTO.
export interface ProviderDTO {
  name: string
  displayName: string
  version?: string
  ready: boolean
  hasUI: boolean
  hasBackend: boolean
  iconURL?: string
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

  // bindingNamesByProvider maps provider-name → kcp APIBinding name in the
  // user's tenant workspace. Empty when the provider is not enabled for
  // this user. Used by the Disable button and the catalog status badge.
  const bindingNamesByProvider = ref<Record<string, string>>({})

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

  async function load() {
    if (loading.value) return
    loading.value = true
    error.value = null
    try {
      const res = await fetch('/api/providers', {
        headers: authHeaders(),
        credentials: 'same-origin',
      })
      if (!res.ok) {
        throw new Error(`provider list failed: ${res.status} ${res.statusText}`)
      }
      const body = (await res.json()) as ProvidersResponse
      items.value = body.items ?? []
      categories.value = body.categories ?? []
      loaded.value = true
      // Best-effort: also refresh the user's enabled set. Failure here
      // doesn't block the catalog from rendering.
      await refreshBindings().catch(() => {
        /* surfaced via Disable button being unavailable */
      })
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
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
    if (!t.orgUUID || !t.workspaceUUID) return
    const url = `/api/orgs/${encodeURIComponent(t.orgUUID)}/workspaces/${encodeURIComponent(t.workspaceUUID)}/providers/enabled`
    const res = await fetch(url, {
      headers: authHeaders(),
      credentials: 'same-origin',
    })
    if (!res.ok) throw new Error(`list enabled providers: ${res.status}`)
    const body = (await res.json()) as { bindingNamesByProvider?: Record<string, string> }
    bindingNamesByProvider.value = body.bindingNamesByProvider ?? {}
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

    const body = {
      acceptedClaims: accept.map((c) => ({ group: c.group ?? '', resource: c.resource })),
    }
    const url = `/api/orgs/${encodeURIComponent(t.orgUUID)}/workspaces/${encodeURIComponent(t.workspaceUUID)}/providers/${encodeURIComponent(p.name)}/enable`
    const res = await fetch(url, {
      method: 'POST',
      headers: { ...authHeaders(), 'Content-Type': 'application/json' },
      credentials: 'same-origin',
      body: JSON.stringify(body),
    })
    if (!res.ok && res.status !== 409) {
      const detail = await res.text().catch(() => '')
      throw new Error(`enable ${p.name} failed: ${res.status} ${res.statusText} ${detail}`)
    }
    bindingNamesByProvider.value = { ...bindingNamesByProvider.value, [p.name]: p.name }
  }

  // readTenantSelection mirrors the storage shape written by
  // tenant.ts's savePersisted — kept inline to avoid an import cycle
  // with @/stores/tenant (same pattern as authHeaders above).
  function readTenantSelection(): { orgUUID: string | null; workspaceUUID: string | null } {
    try {
      const raw = localStorage.getItem('kedge:portal:tenant')
      if (!raw) return { orgUUID: null, workspaceUUID: null }
      const parsed = JSON.parse(raw) as { orgUUID?: string | null; workspaceUUID?: string | null }
      return { orgUUID: parsed.orgUUID ?? null, workspaceUUID: parsed.workspaceUUID ?? null }
    } catch {
      return { orgUUID: null, workspaceUUID: null }
    }
  }

  async function disable(p: ProviderDTO): Promise<void> {
    const bindingName = bindingNamesByProvider.value[p.name]
    if (!bindingName) return
    // Disable = remove the tenant's APIBinding, via the GraphQL gateway (like
    // every other kcp call). Idempotent: an already-gone binding is fine.
    try {
      await graphqlMutate(
        'mutation($n: String!) { apis_kcp_io { v1alpha2 { deleteAPIBinding(name: $n) } } }',
        { n: bindingName },
      )
    } catch (e) {
      const msg = e instanceof Error ? e.message : String(e)
      if (!/not\s*found/i.test(msg)) throw new Error(`disable ${p.name} failed: ${msg}`)
    }
    const next = { ...bindingNamesByProvider.value }
    delete next[p.name]
    bindingNamesByProvider.value = next
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
    bindingNamesByProvider,
    enabledNavItems,
    categorizedNavItems,
    isEnabled,
    load,
    refreshBindings,
    enable,
    disable,
    byName,
  }
})

// authHeaders reads the same localStorage slots the rest of the portal
// uses — kept inline (not imported from @/stores/auth or @/stores/tenant)
// to avoid an import cycle with stores that themselves import providers.
//
// Returns:
//   Authorization      — OIDC bearer the hub authenticates
//   X-Kedge-Org        — sidebar-selected org (so /services/providers/*
//                        scopes operations to the workspace the user is
//                        viewing instead of always landing in the
//                        personal-org default)
//   X-Kedge-Workspace  — sidebar-selected child workspace (optional;
//                        omitted = org-scope)
//
// Hub resolver verifies the org/workspace headers against the
// authenticated user's UserMembershipIndex before honoring them, so a
// client can't spoof workspace access just by setting these.
function authHeaders(): Record<string, string> {
  const h: Record<string, string> = {}
  try {
    const raw = localStorage.getItem(STORAGE_KEYS.auth)
    if (raw) {
      const parsed = JSON.parse(raw) as { idToken?: string }
      if (parsed.idToken) h['Authorization'] = `Bearer ${parsed.idToken}`
    }
  } catch {
    /* ignore */
  }
  try {
    const raw = localStorage.getItem('kedge:portal:tenant')
    if (raw) {
      const t = JSON.parse(raw) as { orgUUID?: string | null; workspaceUUID?: string | null }
      if (t.orgUUID) h['X-Kedge-Org'] = t.orgUUID
      if (t.workspaceUUID) h['X-Kedge-Workspace'] = t.workspaceUUID
    }
  } catch {
    /* ignore */
  }
  return h
}
