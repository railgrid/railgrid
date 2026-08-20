import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import { authFetch } from '@/auth/session'

// Self-hosted ("bring your own") providers: the ones this organization runs
// itself, in its own cluster, instead of consuming the platform's copy.
//
// Backed by /api/orgs/{org}/providers (pkg/hub/restapi/org_providers.go).
// Registering one creates a kcp workspace and mints a credential scoped to it;
// the hub returns that credential plus the Helm steps to install the provider
// against it. Both only ever come back in a response body, so the caller must
// hold on to what it is given — see registration below.

export interface InstallStep {
  title: string
  description?: string
  command: string
}

export interface ResolvedValue {
  name: string
  value: string
  description?: string
  // The hub could not fill this in; the command carries a placeholder.
  unresolved?: boolean
}

export interface InstallInstructions {
  provider: string
  workspacePath: string
  namespace: string
  releaseName: string
  chartRef: string
  chartVersion?: string
  kubeconfigFilename: string
  steps: InstallStep[]
  values?: ResolvedValue[]
  docsURL?: string
  // The chart's values reference in Markdown, carried inline by the provider so
  // it renders offline and matches the chart version actually installed.
  valuesDoc?: string
  // Things the operator must resolve by hand before the commands will work.
  warnings?: string[]
}

export interface OrgProvider {
  name: string
  workspacePath: string
  clusterName?: string
  phase?: string
  // False while the workspace exists but the chart has not been installed yet —
  // the state a half-finished install sits in.
  registered: boolean
  displayName?: string
  description?: string
  version?: string
  apiExportName?: string
  ready: boolean
}

// Registration is the credential plus the steps that use it. The credential is
// returned once per call and never listed, so losing this object means
// re-fetching it from the kubeconfig endpoint.
export interface OrgProviderRegistration {
  provider: OrgProvider
  kubeconfig: string
  instructions?: InstallInstructions
}

// A cluster a self-hosted provider can be installed into. The hub reaches an
// org-owned provider's backend over that cluster's edge tunnel and has no other
// route in, so an edge is a precondition, not a nicety — see
// docs/byo-provider-edge-transport.md.
export interface EdgeInstallTarget {
  workspace: string
  workspaceDisplayName?: string
  name: string
  // The only field to gate on. Connected today; the hub owns the rule so the
  // UI does not have to re-derive it if it tightens.
  eligible: boolean
  connected: boolean
  phase?: string
  agentVersion?: string
}

export interface EdgeInstallTargets {
  items: EdgeInstallTarget[]
  eligible: boolean
  // Why not, in words the user can act on. Empty when eligible.
  reason?: string
}

function readTenantSelection(): { orgUUID: string | null } {
  // Read the selection from localStorage rather than importing the tenant
  // store, matching the import-cycle avoidance already used in stores/providers.
  try {
    const raw = localStorage.getItem('faros:portal:tenant')
    if (!raw) return { orgUUID: null }
    const parsed = JSON.parse(raw) as { orgUUID?: string | null }
    return { orgUUID: parsed.orgUUID ?? null }
  } catch {
    return { orgUUID: null }
  }
}

export const useOrgProvidersStore = defineStore('orgProviders', () => {
  const items = ref<OrgProvider[]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)
  const loaded = ref(false)
  // True when the hub has org-provider support wired (it returns 501 otherwise),
  // so the UI can hide the whole surface instead of showing a broken tab.
  const supported = ref(true)

  // Where a self-hosted provider could go. Empty until loadInstallTargets runs;
  // eligibility starts false so a page that renders before the check completes
  // offers nothing it cannot deliver.
  const installTargets = ref<EdgeInstallTarget[]>([])
  const installTargetsEligible = ref(false)
  const installTargetsReason = ref<string | null>(null)
  const installTargetsLoading = ref(false)
  const installTargetsLoaded = ref(false)

  const eligibleInstallTargets = computed(() => installTargets.value.filter((t) => t.eligible))

  const byName = computed(() => {
    const out: Record<string, OrgProvider> = {}
    for (const p of items.value) out[p.name] = p
    return out
  })

  function isSelfHosted(name: string): boolean {
    return !!byName.value[name]
  }

  function orgURL(path = ''): string | null {
    const { orgUUID } = readTenantSelection()
    if (!orgUUID) return null
    return `/api/orgs/${encodeURIComponent(orgUUID)}/providers${path}`
  }

  async function load(): Promise<void> {
    const url = orgURL()
    if (!url || loading.value) return
    loading.value = true
    error.value = null
    try {
      const res = await authFetch(url, { tenant: true })
      if (res.status === 501) {
        // Hub built without org-provider support. Not an error to surface.
        supported.value = false
        items.value = []
        loaded.value = true
        return
      }
      if (!res.ok) throw new Error(`list self-hosted providers: ${res.status}`)
      const body = (await res.json()) as { items?: OrgProvider[] }
      supported.value = true
      items.value = body.items ?? []
      loaded.value = true
    } catch (e) {
      error.value = e instanceof Error ? e.message : String(e)
    } finally {
      loading.value = false
    }
  }

  // loadInstallTargets asks the hub which clusters could host a provider. The
  // hub runs the identical check when registration is posted, so this drives
  // button state without becoming a second, drift-prone source of truth.
  async function loadInstallTargets(): Promise<void> {
    const url = orgURL('/install-targets')
    if (!url || installTargetsLoading.value) return
    installTargetsLoading.value = true
    try {
      const res = await authFetch(url, { tenant: true })
      if (res.status === 501) {
        supported.value = false
        return
      }
      if (!res.ok) throw new Error(`list install targets: ${res.status}`)
      const body = (await res.json()) as EdgeInstallTargets
      installTargets.value = body.items ?? []
      installTargetsEligible.value = !!body.eligible
      installTargetsReason.value = body.reason ?? null
      installTargetsLoaded.value = true
    } catch (e) {
      // Surfaced through the same error slot as the rest of the surface; the
      // caller renders self-hosting as unavailable rather than guessing it is
      // fine, because guessing "fine" produces a 409 on click.
      error.value = e instanceof Error ? e.message : String(e)
      installTargetsEligible.value = false
      installTargetsReason.value = 'could not check for a connected cluster'
    } finally {
      installTargetsLoading.value = false
    }
  }

  // register creates the workspace + credential for a self-hosted copy of
  // `sourceProvider` on `edge`, returning the credential and install steps.
  // Idempotent on the hub side, so a retry returns the same workspace and the
  // same token.
  async function register(
    name: string,
    sourceProvider: string | undefined,
    edge: Pick<EdgeInstallTarget, 'workspace' | 'name'>,
  ): Promise<OrgProviderRegistration> {
    const url = orgURL()
    if (!url) throw new Error('select an organization first')
    const res = await authFetch(url, {
      method: 'POST',
      tenant: true,
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        name,
        sourceProvider: sourceProvider ?? name,
        edge: { workspace: edge.workspace, name: edge.name },
      }),
    })
    if (!res.ok) {
      const detail = await res.text().catch(() => '')
      throw new Error(`self-host ${name} failed: ${res.status} ${detail}`)
    }
    const registration = (await res.json()) as OrgProviderRegistration
    await load()
    return registration
  }

  // instructions re-fetches the credential and steps for an already-registered
  // provider. The hub re-mints from the same Secret, so this returns the SAME
  // credential rather than a second live one.
  async function instructions(name: string): Promise<OrgProviderRegistration> {
    const url = orgURL(`/${encodeURIComponent(name)}/kubeconfig`)
    if (!url) throw new Error('select an organization first')
    const res = await authFetch(url, { tenant: true })
    if (!res.ok) {
      const detail = await res.text().catch(() => '')
      throw new Error(`fetch install details for ${name} failed: ${res.status} ${detail}`)
    }
    return (await res.json()) as OrgProviderRegistration
  }

  async function remove(name: string): Promise<void> {
    const url = orgURL(`/${encodeURIComponent(name)}`)
    if (!url) throw new Error('select an organization first')
    const res = await authFetch(url, { method: 'DELETE', tenant: true })
    if (!res.ok && res.status !== 404) {
      const detail = await res.text().catch(() => '')
      throw new Error(`remove ${name} failed: ${res.status} ${detail}`)
    }
    items.value = items.value.filter((p) => p.name !== name)
    await load()
  }

  return {
    items,
    loading,
    loaded,
    error,
    supported,
    byName,
    isSelfHosted,
    load,
    register,
    instructions,
    remove,
    installTargets,
    eligibleInstallTargets,
    installTargetsEligible,
    installTargetsReason,
    installTargetsLoading,
    installTargetsLoaded,
    loadInstallTargets,
  }
})
