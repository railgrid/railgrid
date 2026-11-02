import { useTenantStore } from './tenant'
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
  // One command that moves an existing install to chartVersion while keeping
  // the values it was installed with (helm --reuse-values).
  upgrade?: InstallStep
  values?: ResolvedValue[]
  docsURL?: string
  // The chart's values reference in Markdown, carried inline by the provider so
  // it renders offline and matches the chart version actually installed.
  valuesDoc?: string
  // Things the operator must resolve by hand before the commands will work.
  warnings?: string[]
}

export interface ProviderInstallation {
  phase: 'NotStarted' | 'Preparing' | 'Queued' | 'Installing' | 'Checking' | 'Ready' | 'Failed'
  message: string
  namespace?: string
  job?: string
  version?: string
  attempt?: number
  canRetry: boolean
}

export interface OrgProvider {
  name: string
  workspacePath: string
  clusterName?: string
  // Hub-recorded hosting route, included only when the current caller can see
  // the referenced Edge. Never infer it from the first eligible install target.
  hostingEdge?: {
    name: string
    workspace: string
    workspaceDisplayName?: string
  }
  phase?: string
  // False while the workspace exists but the chart has not been installed yet —
  // the state a half-finished install sits in.
  registered: boolean
  // True while kcp is deleting this provider workspace. The list keeps this
  // row until the workspace and its owned APIs have finished deletion.
  deleting?: boolean
  displayName?: string
  description?: string
  version?: string
  apiExportName?: string
  ready: boolean
  // What the org's copy runs vs. the baseline of what the platform's managed
  // copy runs now (chart versions when both entries carry one, the entries'
  // spec.version otherwise). The hub owns the comparison; the UI only renders
  // upgradeAvailable.
  installedVersion?: string
  availableVersion?: string
  upgradeAvailable?: boolean
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

// API failures use Kubernetes Status envelopes. Keep the actionable message,
// rather than making operators read a serialized object in the setup form.
async function providerRequestError(response: Response, action: string): Promise<Error> {
  const body = await response.text().catch(() => '')
  let message = body
  try {
    const status = JSON.parse(body) as { message?: unknown }
    if (typeof status.message === 'string') message = status.message
  } catch { /* Older hubs can return plain text. */ }
  if (/workspaces limit exceeded/i.test(message)) {
    return new Error('Provider setup is blocked by the organization’s workspace limit. Self-hosting needs an internal provider workspace. Ask an organization admin to review the workspace allowance, then retry.')
  }
  return new Error(`${action}: ${message || `HTTP ${response.status}`}`)
}

function readTenantSelection(): { orgUUID: string | null } {
  return { orgUUID: useTenantStore().orgUUID }
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
  const installTargetsError = ref<string | null>(null)
  const installTargetsLoading = ref(false)
  const installTargetsLoaded = ref(false)

  let scopeGeneration = 0
  function resetForOrganization() {
    scopeGeneration += 1
    items.value = []
    loading.value = false
    error.value = null
    loaded.value = false
    supported.value = true
    installTargets.value = []
    installTargetsEligible.value = false
    installTargetsReason.value = null
    installTargetsError.value = null
    installTargetsLoading.value = false
    installTargetsLoaded.value = false
  }

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

  function assertCurrentOrganization(orgUUID: string, generation: number, action: string): void {
    if (generation !== scopeGeneration || readTenantSelection().orgUUID !== orgUUID) {
      throw new Error(`Organization changed while ${action}; reopen the provider in the current organization.`)
    }
  }

  async function load(): Promise<boolean> {
    const url = orgURL()
    if (!url || loading.value) return false
    const generation = scopeGeneration
    loading.value = true
    error.value = null
    try {
      const res = await authFetch(url, { tenant: true })
      if (generation !== scopeGeneration || url !== orgURL()) return false
      if (res.status === 501) {
        // Hub built without org-provider support. Not an error to surface.
        supported.value = false
        items.value = []
        loaded.value = true
        return false
      }
      if (!res.ok) throw new Error(`list self-hosted providers: ${res.status}`)
      const body = (await res.json()) as { items?: OrgProvider[] }
      if (generation !== scopeGeneration || url !== orgURL()) return false
      supported.value = true
      items.value = body.items ?? []
      loaded.value = true
      return true
    } catch (e) {
      if (generation !== scopeGeneration || url !== orgURL()) return false
      error.value = e instanceof Error ? e.message : String(e)
      return false
    } finally {
      if (generation === scopeGeneration) loading.value = false
    }
  }

  // loadInstallTargets asks the hub which clusters could host a provider. The
  // hub runs the identical check when registration is posted, so this drives
  // button state without becoming a second, drift-prone source of truth.
  async function loadInstallTargets(): Promise<void> {
    const url = orgURL('/install-targets')
    if (!url || installTargetsLoading.value) return
    const generation = scopeGeneration
    installTargetsLoading.value = true
    installTargetsError.value = null
    try {
      const res = await authFetch(url, { tenant: true })
      if (generation !== scopeGeneration || url !== orgURL('/install-targets')) return
      if (res.status === 501) {
        supported.value = false
        return
      }
      if (!res.ok) throw new Error(`list install targets: ${res.status}`)
      const body = (await res.json()) as EdgeInstallTargets
      if (generation !== scopeGeneration || url !== orgURL('/install-targets')) return
      installTargets.value = body.items ?? []
      installTargetsEligible.value = !!body.eligible
      installTargetsReason.value = body.reason ?? null
      installTargetsLoaded.value = true
    } catch (e) {
      if (generation !== scopeGeneration || url !== orgURL('/install-targets')) return
      // Keep this failure local to the preflight surface. The caller renders
      // self-hosting as unavailable rather than guessing it is fine, because
      // guessing "fine" produces a 409 on click.
      installTargetsError.value = e instanceof Error ? e.message : String(e)
      installTargetsEligible.value = false
      installTargetsReason.value = 'could not check for a connected cluster'
      installTargetsLoaded.value = true
    } finally {
      if (generation === scopeGeneration) installTargetsLoading.value = false
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
    const { orgUUID } = readTenantSelection()
    const generation = scopeGeneration
    const url = orgURL()
    if (!orgUUID || !url) throw new Error('select an organization first')
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
    // The response contains a long-lived credential. Do not even parse it
    // after the caller has moved to another organization.
    assertCurrentOrganization(orgUUID, generation, 'preparing provider installation')
    if (!res.ok) {
      throw await providerRequestError(res, `Could not prepare ${name}`)
    }
    const registration = (await res.json()) as OrgProviderRegistration
    assertCurrentOrganization(orgUUID, generation, 'preparing provider installation')
    await load()
    assertCurrentOrganization(orgUUID, generation, 'preparing provider installation')
    return registration
  }

  // instructions re-fetches the credential and steps for an already-registered
  // provider. The hub re-mints from the same Secret, so this returns the SAME
  // credential rather than a second live one.
  async function instructions(name: string): Promise<OrgProviderRegistration> {
    const { orgUUID } = readTenantSelection()
    const generation = scopeGeneration
    const url = orgURL(`/${encodeURIComponent(name)}/kubeconfig`)
    if (!orgUUID || !url) throw new Error('select an organization first')
    const res = await authFetch(url, { tenant: true })
    // kubeconfig responses contain a long-lived credential; discard late
    // responses before reading the body if the tenant scope changed.
    assertCurrentOrganization(orgUUID, generation, `loading installation details for ${name}`)
    if (!res.ok) {
      throw await providerRequestError(res, `Could not load installation details for ${name}`)
    }
    const result = (await res.json()) as OrgProviderRegistration
    assertCurrentOrganization(orgUUID, generation, `loading installation details for ${name}`)
    return result
  }

  async function startInstallation(name: string, edge?: Pick<EdgeInstallTarget, 'workspace' | 'name'>, retry = false): Promise<ProviderInstallation> {
    const { orgUUID } = readTenantSelection()
    const generation = scopeGeneration
    const url = orgURL(`/${encodeURIComponent(name)}/installation`)
    if (!orgUUID || !url) throw new Error('select an organization first')
    const res = await authFetch(url, { method: 'POST', tenant: true, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ edge: edge ? { workspace: edge.workspace, name: edge.name } : undefined, retry }) })
    assertCurrentOrganization(orgUUID, generation, `starting installation of ${name}`)
    if (!res.ok) throw await providerRequestError(res, `Could not install ${name}`)
    const result = await res.json() as ProviderInstallation
    assertCurrentOrganization(orgUUID, generation, `starting installation of ${name}`)
    await load()
    assertCurrentOrganization(orgUUID, generation, `starting installation of ${name}`)
    return result
  }

  async function getInstallation(name: string): Promise<ProviderInstallation> {
    const { orgUUID } = readTenantSelection()
    const generation = scopeGeneration
    const url = orgURL(`/${encodeURIComponent(name)}/installation`)
    if (!orgUUID || !url) throw new Error('select an organization first')
    const res = await authFetch(url, { tenant: true })
    assertCurrentOrganization(orgUUID, generation, `refreshing installation of ${name}`)
    if (!res.ok) throw await providerRequestError(res, `Could not refresh installation of ${name}`)
    const result = await res.json() as ProviderInstallation
    assertCurrentOrganization(orgUUID, generation, `refreshing installation of ${name}`)
    return result
  }

  async function remove(name: string): Promise<void> {
    const { orgUUID } = readTenantSelection()
    const generation = scopeGeneration
    const url = orgURL(`/${encodeURIComponent(name)}`)
    if (!orgUUID || !url) throw new Error('select an organization first')
    const res = await authFetch(url, { method: 'DELETE', tenant: true })
    assertCurrentOrganization(orgUUID, generation, `removing ${name}`)
    if (!res.ok && res.status !== 404) {
      throw await providerRequestError(res, `Could not remove ${name}`)
    }
    // Workspace deletion is asynchronous. Keep the row until an authoritative
    // list response confirms the workspace is gone; the portal polls while the
    // server marks it as deleting.
    await load()
    assertCurrentOrganization(orgUUID, generation, `removing ${name}`)
  }

  return {
    items,
    loading,
    loaded,
    error,
    supported,
    resetForOrganization,
    byName,
    isSelfHosted,
    load,
    register,
    startInstallation,
    getInstallation,
    instructions,
    remove,
    installTargets,
    eligibleInstallTargets,
    installTargetsEligible,
    installTargetsReason,
    installTargetsError,
    installTargetsLoading,
    installTargetsLoaded,
    loadInstallTargets,
  }
})
