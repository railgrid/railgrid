import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { STORAGE_KEYS } from '@/lib/constants'

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
  // Populated when the provider declares spec.apiExport. The portal uses
  // these coordinates to build the APIBinding it POSTs into the tenant
  // workspace on Enable.
  apiExportPath?: string
  apiExportName?: string
  permissionClaims?: PermissionClaim[]
}

export interface PermissionClaim {
  group?: string
  resource: string
  verbs?: string[]
  tenantScoped?: boolean
}

interface ProvidersResponse {
  items: ProviderDTO[]
}

// APIBindingDTO is a slim mirror of the bits we need from kcp's APIBinding
// list response (apis.kcp.io/v1alpha2). We only consume two fields, so we
// keep this minimal.
interface APIBindingDTO {
  metadata: { name: string; resourceVersion?: string }
  spec: {
    reference: {
      export: { path: string; name: string }
    }
  }
}

interface APIBindingListResponse {
  items: APIBindingDTO[]
}

export const useProvidersStore = defineStore('providers', () => {
  const items = ref<ProviderDTO[]>([])
  const loaded = ref(false)
  const loading = ref(false)
  const error = ref<string | null>(null)

  // bindingNamesByProvider maps provider-name → kcp APIBinding name in the
  // user's tenant workspace. Empty when the provider is not enabled for
  // this user. Used by the Disable button and the catalog status badge.
  const bindingNamesByProvider = ref<Record<string, string>>({})

  // Nav entries for AppLayout's side rail. Only providers that are ready,
  // have a UI, AND are bound by the current user appear in the nav. This is
  // the Phase 3 filter — Phase 1A used to show every ready+UI provider.
  const enabledNavItems = computed(() =>
    items.value
      .filter((p) => p.ready && p.hasUI && bindingNamesByProvider.value[p.name])
      .map((p) => ({
        name: p.name,
        label: p.displayName,
        to: `/providers/${p.name}`,
        iconURL: p.iconURL ?? null,
        version: p.version ?? '',
      })),
  )

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

  // refreshBindings lists APIBindings in the user's tenant workspace and
  // builds the bindingNamesByProvider map. A binding's `reference.export.path`
  // is of the form root:kedge:providers:{provider-name}, so we derive the
  // provider name from the trailing segment.
  async function refreshBindings() {
    const cluster = currentTenantWorkspace()
    if (!cluster) return
    const res = await fetch(
      `/clusters/${cluster}/apis/apis.kcp.io/v1alpha2/apibindings`,
      { headers: authHeaders(), credentials: 'same-origin' },
    )
    if (!res.ok) throw new Error(`list APIBindings: ${res.status}`)
    const body = (await res.json()) as APIBindingListResponse
    const next: Record<string, string> = {}
    for (const b of body.items ?? []) {
      const path = b.spec?.reference?.export?.path ?? ''
      if (!path.startsWith('root:kedge:providers:')) continue
      const providerName = path.substring('root:kedge:providers:'.length)
      next[providerName] = b.metadata.name
    }
    bindingNamesByProvider.value = next
  }

  // enable POSTs an APIBinding to the user's tenant workspace referencing
  // the provider's APIExport. `accept` is the list of permission claims the
  // user explicitly accepted in the confirmation dialog; each gets state
  // "Accepted" with a matchAll selector. Claims the provider declared but
  // the user did not accept are sent as state "Rejected" — kcp will refuse
  // to mark the binding Bound if any required claim is rejected, which
  // surfaces the mismatch cleanly to the user.
  async function enable(p: ProviderDTO, accept: PermissionClaim[]): Promise<void> {
    if (!p.apiExportPath || !p.apiExportName) {
      throw new Error(`${p.name}: provider declares no APIExport to bind`)
    }
    const cluster = currentTenantWorkspace()
    if (!cluster) throw new Error('no tenant workspace in session')

    const acceptedKey = (c: PermissionClaim) => `${c.group ?? ''}/${c.resource}`
    const acceptedSet = new Set(accept.map(acceptedKey))
    const claimEntries = (p.permissionClaims ?? []).map((c) => {
      const entry: Record<string, unknown> = {
        resource: c.resource,
        verbs: c.verbs ?? [],
        selector: { matchAll: true },
        state: acceptedSet.has(acceptedKey(c)) ? 'Accepted' : 'Rejected',
      }
      if (c.group) entry.group = c.group
      return entry
    })

    const bindingName = p.name
    const body = {
      apiVersion: 'apis.kcp.io/v1alpha2',
      kind: 'APIBinding',
      metadata: { name: bindingName },
      spec: {
        reference: {
          export: { path: p.apiExportPath, name: p.apiExportName },
        },
        permissionClaims: claimEntries,
      },
    }
    const res = await fetch(
      `/clusters/${cluster}/apis/apis.kcp.io/v1alpha2/apibindings`,
      {
        method: 'POST',
        headers: { ...authHeaders(), 'Content-Type': 'application/json' },
        credentials: 'same-origin',
        body: JSON.stringify(body),
      },
    )
    if (!res.ok && res.status !== 409) {
      const detail = await res.text().catch(() => '')
      throw new Error(`enable ${p.name} failed: ${res.status} ${res.statusText} ${detail}`)
    }
    bindingNamesByProvider.value = { ...bindingNamesByProvider.value, [p.name]: bindingName }
  }

  async function disable(p: ProviderDTO): Promise<void> {
    const cluster = currentTenantWorkspace()
    if (!cluster) throw new Error('no tenant workspace in session')
    const bindingName = bindingNamesByProvider.value[p.name]
    if (!bindingName) return
    const res = await fetch(
      `/clusters/${cluster}/apis/apis.kcp.io/v1alpha2/apibindings/${bindingName}`,
      {
        method: 'DELETE',
        headers: authHeaders(),
        credentials: 'same-origin',
      },
    )
    if (!res.ok && res.status !== 404) {
      const detail = await res.text().catch(() => '')
      throw new Error(`disable ${p.name} failed: ${res.status} ${res.statusText} ${detail}`)
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
    loaded,
    loading,
    error,
    bindingNamesByProvider,
    enabledNavItems,
    isEnabled,
    load,
    refreshBindings,
    enable,
    disable,
    byName,
  }
})

// authHeaders reads the same localStorage slot the rest of the portal uses.
// Kept private to this module so the store doesn't take a hard dep on the
// auth store (which would create an import cycle).
function authHeaders(): Record<string, string> {
  try {
    const raw = localStorage.getItem(STORAGE_KEYS.auth)
    if (!raw) return {}
    const parsed = JSON.parse(raw) as { idToken?: string }
    if (parsed.idToken) return { Authorization: `Bearer ${parsed.idToken}` }
  } catch {
    /* ignore */
  }
  return {}
}

// currentTenantWorkspace reads auth state directly from localStorage to
// avoid an import cycle with @/stores/auth.
function currentTenantWorkspace(): string | null {
  try {
    const raw = localStorage.getItem(STORAGE_KEYS.auth)
    if (!raw) return null
    const parsed = JSON.parse(raw) as { clusterName?: string }
    return parsed.clusterName ?? null
  } catch {
    return null
  }
}
