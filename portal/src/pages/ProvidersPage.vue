<script setup lang="ts">
import { useScopedNavigation } from '@/composables/useScopedNavigation'
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import AppLayout from '@/components/AppLayout.vue'
import ProviderEnableDialog from '@/components/ProviderEnableDialog.vue'
import SelfHostInstructions from '@/components/SelfHostInstructions.vue'
import SelfHostWizard from '@/components/SelfHostWizard.vue'
import { confirmDialog } from '@/portalkit/confirm'
import { toast } from '@/portalkit/toast'
import { useProviderEnableFlow } from '@/composables/useProviderEnableFlow'
import { useProvidersStore, type ProviderDTO } from '@/stores/providers'
import { useOrgProvidersStore, type OrgProviderRegistration } from '@/stores/orgProviders'
import { useTenantStore } from '@/stores/tenant'
import { categoryIcons, fallbackCategoryIcon } from '@/lib/categoryIcons'
import { providerBindingAction } from '@/lib/providerBindingAction'
import { Puzzle, ExternalLink, AlertCircle, AlertTriangle, Plus, X, Loader2, Search, Server, Trash2, RefreshCw } from 'lucide-vue-next'

const { scopePath } = useScopedNavigation()
const route = useRoute()
const router = useRouter()

const providers = useProvidersStore()
const orgProviders = useOrgProvidersStore()
const tenant = useTenantStore()
const disableOperations = new Map<string, symbol>()
const currentTenantScope = computed(() => `${tenant.orgUUID ?? ''}/${tenant.workspaceUUID ?? ''}/${tenant.workspaceMode}`)

// Two views of the same catalog: what you can turn on ("Catalog"), and what you
// can run yourself ("Self-Hosting"). They are separate tabs rather than one list
// because the actions differ in kind — enabling binds an API in a workspace,
// self-hosting hands you a credential and a deployment to run.
type Tab = 'catalog' | 'self-hosting'
const tab = computed<Tab>(() => route.query.tab === 'self-hosting' ? 'self-hosting' : 'catalog')
const tabs: { key: Tab; label: string }[] = [
  { key: 'catalog', label: 'Catalog' },
  { key: 'self-hosting', label: 'Self-Hosting' },
]

// The provider whose install details are open, and what the hub returned for it.
const activeRegistration = ref<OrgProviderRegistration | null>(null)
const inventoryScope = ref<string | null>(null)
const inventoryCurrent = computed(() => inventoryScope.value === currentTenantScope.value && orgProviders.loaded)
const requestedInstallName = computed(() => typeof route.query.install === 'string' ? route.query.install : null)
const installProvider = computed(() => {
  const name = requestedInstallName.value
  if (!name || tab.value !== 'self-hosting' || !inventoryCurrent.value) return null
  const existing = orgProviders.byName[name]
  if (existing?.deleting) return null
  if (!existing && (!providers.loaded || !providers.selfHostable.some(provider => provider.name === name))) return null
  return {
    name,
    displayName: providerDisplayName(existing ?? providers.byName(name) ?? { name }),
    resume: !!existing,
    manual: !!existing && route.query.manual === '1',
  }
})
const canManageSelfHostedCredentials = computed(() => tenant.activeOrg?.role === 'admin' || tenant.activeOrg?.catalogEntryCreation === 'members')
const selfHostBusy = ref<Record<string, boolean>>({})
const instructionName = ref<string | null>(null)
const instructionError = ref<string | null>(null)
const selfHostedHeading = ref<HTMLElement | null>(null)
type SelfHostRemovalState = 'confirming' | 'requesting' | 'deleting'
const selfHostRemoval = ref<Record<string, SelfHostRemovalState>>({})
const migrationConfirming = ref<Record<string, boolean>>({})
const confirmingRemoval = ref(false)
const selfHostError = ref<string | null>(null)
const selfHostNotice = ref<string | null>(null)
let selfHostRequestGeneration = 0
const removalPollTimers = new Map<string, ReturnType<typeof setTimeout>>()
const removalOperations = new Map<string, symbol>()
const removalLabels = new Map<string, string>()
const removalPollProgress = new Map<string, { checks: number; failures: number }>()
const removalPollInFlight = new Map<string, symbol>()
let removalInventoryLoad: { scope: string; promise: Promise<boolean> } | null = null

watch(canManageSelfHostedCredentials, allowed => {
  if (!allowed) {
    selfHostRequestGeneration += 1
    activeRegistration.value = null
    instructionName.value = null
    instructionError.value = null
  }
})

const canSelfHost = computed(() => orgProviders.installTargetsEligible && orgProviders.eligibleInstallTargets.length > 0)

type ProviderPageQuery = 'tab' | 'install' | 'manage' | 'manual'
type QueryPatch = Partial<Record<ProviderPageQuery, string | null>>

function patchProviderQuery(patch: QueryPatch, replace = false) {
  const query = { ...route.query }
  let changed = false
  for (const key of ['tab', 'install', 'manage', 'manual'] as const) {
    if (!(key in patch)) continue
    const value = patch[key]
    if (value === null) {
      if (key in query) {
        delete query[key]
        changed = true
      }
    } else if (query[key] !== value) {
      query[key] = value!
      changed = true
    }
  }
  if (!changed) return
  const location = { query, hash: route.hash }
  return replace ? router.replace(location) : router.push(location)
}

function setTab(next: Tab) {
  if (next === tab.value && !requestedInstallName.value && route.query.manage === undefined) return
  patchProviderQuery({ tab: next, install: null, manage: null, manual: null })
}

function validProviderName(name: string): boolean {
  return name.length <= 253 && name.split('.').every(label => /^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(label))
}

// Route state is deliberately limited to the Providers page's query keys.
// Provider micro-frontends own /providers/:name paths and keep their own routes.
function normalizeProviderRoute() {
  const patch: QueryPatch = {}
  const rawTab = route.query.tab
  if (rawTab !== undefined && (typeof rawTab !== 'string' || !['catalog', 'self-hosting'].includes(rawTab))) {
    patch.tab = null
    patch.install = null
    patch.manage = null
    patch.manual = null
  }
  const hasInstall = route.query.install !== undefined
  const hasManage = route.query.manage !== undefined
  const hasManual = route.query.manual !== undefined
  if (hasManage) {
    // `manage` opened the retired full-page workspace/enablement screen. Keep
    // old links on the Self-Hosting list; never fetch credentials from it.
    patch.tab = 'self-hosting'
    patch.manage = null
    patch.install = null
    patch.manual = null
  } else if (inventoryCurrent.value && !orgProviders.supported && tab.value === 'self-hosting') {
    patch.tab = 'catalog'
    patch.install = null
    patch.manage = null
    patch.manual = null
  } else if (tab.value !== 'self-hosting') {
    if (hasInstall) patch.install = null
    if (hasManual) patch.manual = null
  } else if (hasInstall) {
    const rawName = route.query.install
    const name = typeof rawName === 'string' ? rawName : null
    if (!name || !validProviderName(name)) {
      patch.install = null
      patch.manual = null
    } else if (inventoryCurrent.value) {
      const existing = orgProviders.byName[name]
      if (existing?.deleting) {
        patch.install = null
        patch.manual = null
      } else if (providers.loaded && !existing && !providers.selfHostable.some(provider => provider.name === name)) {
        patch.install = null
        patch.manual = null
      } else if (hasManual && (route.query.manual !== '1' || !existing)) {
        // A manual marker only records a choice for a provider that already
        // exists. With a fresh inventory the install route stays open in its
        // review step; the stale marker must never auto-register on mount.
        patch.manual = null
      }
    }
  } else if (hasManual) {
    patch.manual = null
  }
  if (hasManual && (!hasInstall || route.query.manual !== '1')) patch.manual = null
  if (inventoryCurrent.value && !orgProviders.supported && (tab.value === 'self-hosting' || patch.tab === 'self-hosting')) {
    patch.tab = 'catalog'
    patch.install = null
    patch.manage = null
    patch.manual = null
  }
  if (Object.keys(patch).length) patchProviderQuery(patch, true)
}

watch(
  () => [route.query.tab, route.query.install, route.query.manage, route.query.manual, inventoryScope.value, orgProviders.loaded, orgProviders.supported, orgProviders.items.map(p => `${p.name}:${p.deleting}`).join('|'), providers.loaded],
  normalizeProviderRoute,
  { immediate: true },
)

function startInstall(p: { name: string }) {
  if (orgProviders.byName[p.name]?.deleting) return
  selfHostRequestGeneration += 1
  activeRegistration.value = null
  instructionName.value = null
  instructionError.value = null
  patchProviderQuery({ tab: 'self-hosting', install: p.name, manage: null, manual: null })
}

function cancelInstall() {
  patchProviderQuery({ install: null, manual: null })
}

function resumeManualInstall() {
  patchProviderQuery({ manual: '1' }, true)
}

function clearProviderPageRouteState() {
  patchProviderQuery({ tab: null, install: null, manage: null, manual: null }, true)
}

async function loadOrgProviderInventory(reset = false) {
  const targetScope = currentTenantScope.value
  if (reset || inventoryScope.value !== targetScope) {
    inventoryScope.value = null
    if (reset) orgProviders.resetForOrganization()
  }
  await orgProviders.load()
  if (targetScope === currentTenantScope.value && (orgProviders.loaded || orgProviders.error)) inventoryScope.value = targetScope
}

async function refreshOrgProviderInventory() {
  const targetOrgUUID = tenant.orgUUID
  const targetScope = dialogScope.value
  await Promise.all([
    loadOrgProviderInventory(),
    refreshSelfHostingCatalog(targetOrgUUID, targetScope),
  ])
}

// Keep idle health current without polling hidden tabs or the installation wizard.
let inventoryPollTimer: ReturnType<typeof setTimeout> | null = null
let inventoryPollGeneration = 0
let inventoryPollingMounted = false
function canPollSelfHostingList() {
  return inventoryPollingMounted && document.visibilityState !== 'hidden' &&
    tab.value === 'self-hosting' && !requestedInstallName.value && !!tenant.orgUUID
}
function stopInventoryPolling() {
  inventoryPollGeneration += 1
  if (inventoryPollTimer) clearTimeout(inventoryPollTimer)
  inventoryPollTimer = null
}
function canPollInventory() {
  return canPollSelfHostingList() && removalOperations.size === 0
}
function scheduleInventoryPoll(generation: number) {
  if (!canPollInventory() || generation !== inventoryPollGeneration) return
  inventoryPollTimer = setTimeout(async () => {
    inventoryPollTimer = null
    if (!canPollInventory() || generation !== inventoryPollGeneration) return
    try {
      await loadOrgProviderInventory()
    } finally {
      if (generation === inventoryPollGeneration) scheduleInventoryPoll(generation)
    }
  }, 15000)
}
function restartInventoryPolling(refresh = true) {
  stopInventoryPolling()
  if (!canPollSelfHostingList()) {
    pauseSelfHostRemovalPolling()
    return
  }
  if (removalOperations.size) {
    resumeSelfHostRemovalPolling()
    return
  }
  const generation = inventoryPollGeneration
  if (refresh) {
    void loadOrgProviderInventory().finally(() => scheduleInventoryPoll(generation))
  } else scheduleInventoryPoll(generation)
}
function onInventoryVisibilityChange() {
  restartInventoryPolling()
}
watch(() => `${tab.value}:${requestedInstallName.value ?? ''}:${currentTenantScope.value}`, () => restartInventoryPolling())

async function refreshSelfHostingCatalog(targetOrgUUID: string | null, targetScope: string): Promise<void> {
  if (!targetOrgUUID || tenant.orgUUID !== targetOrgUUID || dialogScope.value !== targetScope) return
  // The org's catalog entry shadows the platform DTO. Once its workspace is
  // gone, reload the catalog so the platform recipe can become self-hostable.
  await providers.load(targetOrgUUID)
  if (tenant.orgUUID !== targetOrgUUID || dialogScope.value !== targetScope) return
}

watch(
  () => `${route.params.orgID ?? ''}/${route.params.workspaceID ?? ''}`,
  (scope, previous) => {
    if (previous !== undefined && scope !== previous) clearProviderPageRouteState()
  },
  { flush: 'sync' },
)

function providerDisplayName(p: { name: string; displayName?: string }) {
  return (p.displayName !== p.name && p.displayName) || providers.byName(p.name)?.displayName || p.name
}

async function finishInstall(name: string) {
  const targetScope = dialogScope.value
  activeRegistration.value = null
  instructionName.value = null
  instructionError.value = null
  await patchProviderQuery({ tab: 'self-hosting', install: null, manage: null, manual: null }, true)
  if (dialogScope.value !== targetScope) return
  selfHostNotice.value = `${providerDisplayName(orgProviders.items.find(provider => provider.name === name) ?? providers.byName(name) ?? { name })} is ready. Workspace access is managed in Catalog.`
  void loadOrgProviderInventory()
  void providers.load()
  await nextTick()
  if (dialogScope.value === targetScope) selfHostedHeading.value?.focus()
}

// The hub-owned catalog is authoritative for whether the Edges prerequisite
// exists and is ready to open. Do not manufacture a provider route when the
// entry is absent: ProviderFrame can explain a known-but-unready provider, but
// it cannot make an unavailable catalog entry actionable.
type EdgesSelfHostState = 'absent' | 'unready' | 'ready'

const edgesProvider = computed(() => providers.byName('edges'))
const edgesSelfHostState = computed<EdgesSelfHostState>(() => {
  const edges = edgesProvider.value
  if (!edges) return 'absent'
  return edges.ready && !!edges.serving?.ui ? 'ready' : 'unready'
})

function showEdgesCatalogEntry() {
  setTab('catalog')
  selectedCategory.value = null
  search.value = 'edges'
}

function bindingAction(p: ProviderDTO) {
  return providerBindingAction({
    hasAPIExport: !!p.export?.name,
    enabled: providers.isEnabled(p.name),
    disabling: providers.isDisabling(p.name),
  })
}

type WorkspaceProviderCopy = 'organization' | 'platform' | 'unknown' | 'not-enabled' | 'not-self-hosted'

function bindingProjectionIsCurrent(): boolean {
  return tenant.workspaceMode === 'workspace' && !!tenant.orgUUID && !!tenant.workspaceUUID &&
    providers.bindingsLoadState === 'ready' && !providers.bindingsStale &&
    providers.bindingsOrgUUID === tenant.orgUUID && providers.bindingsWorkspace === tenant.workspaceUUID &&
    providers.bindingsRequestOrgUUID === tenant.orgUUID && providers.bindingsRequestWorkspaceUUID === tenant.workspaceUUID
}

function currentWorkspaceBinding(p: ProviderDTO) {
  return bindingProjectionIsCurrent() ? providers.bindingsByProvider[p.name] ?? null : null
}

function workspaceProviderCopy(p: ProviderDTO): WorkspaceProviderCopy {
  if (!providers.isSelfManaged(p)) return 'not-self-hosted'
  if (!providers.isEnabled(p.name)) return 'not-enabled'
  const binding = currentWorkspaceBinding(p)
  if (!binding) return 'unknown'
  return binding.selfHosted ? 'organization' : 'platform'
}

function bindingIdentity(binding: NonNullable<ReturnType<typeof currentWorkspaceBinding>>): string {
  return `${binding.bindingName}\n${binding.exportPath}\n${binding.selfHosted}`
}

function canDisableFromThisProjection(p: ProviderDTO): boolean {
  return providers.isSelfManaged(p) && providers.isEnabled(p.name)
    ? !!currentWorkspaceBinding(p)
    : true
}

function providerStatusLabel(p: ProviderDTO): string {
  if (!p.ready) {
    const copy = providers.isSelfManaged(p) ? workspaceProviderCopy(p) : 'not-self-hosted'
    if (copy === 'platform') return 'Not ready · platform copy'
    if (copy === 'organization') return 'Not ready · org copy'
    return 'Not ready'
  }
  if (p.serving?.ui?.builtinRoute) return 'Built-in'
  if (providers.isDisabling(p.name)) return 'Disabling'
  if (providers.isEnabled(p.name)) {
    if (!providers.isSelfManaged(p)) return 'Enabled'
    const copy = workspaceProviderCopy(p)
    if (copy === 'platform') return 'Enabled · platform copy'
    if (copy === 'organization') return 'Enabled · org copy'
    return 'Enabled · copy unknown'
  }
  return providers.hasMissingDependencies(p) ? 'Blocked' : 'Available'
}

function providerStatusClass(p: ProviderDTO): string {
  if (!p.ready) return 'border border-warning/30 bg-warning-subtle text-warning'
  if (p.serving?.ui?.builtinRoute) return 'border border-border-default bg-surface-overlay text-text-secondary'
  if (providers.isDisabling(p.name)) return 'border border-warning/30 bg-warning-subtle text-warning'
  if (providers.isEnabled(p.name)) {
    return providers.isSelfManaged(p) && workspaceProviderCopy(p) === 'platform'
      ? 'border border-warning/30 bg-warning-subtle text-warning'
      : 'border border-accent/30 bg-accent/10 text-accent'
  }
  if (providers.hasMissingDependencies(p)) return 'border border-warning/30 bg-warning-subtle text-warning'
  return 'border border-success/30 bg-success-subtle text-success'
}

function selfHostedPlatformBinding(p: ProviderDTO): boolean {
  return providers.isSelfManaged(p) && !!p.shadowsPlatform && workspaceProviderCopy(p) === 'platform'
}

async function showInstructions(name: string) {
  if (!canManageSelfHostedCredentials.value || orgProviders.byName[name]?.deleting || selfHostBusy.value[name] || activeRegistration.value?.provider.name === name) return
  const requestGeneration = ++selfHostRequestGeneration
  const targetScope = dialogScope.value
  instructionName.value = name
  instructionError.value = null
  activeRegistration.value = null
  selfHostBusy.value = { ...selfHostBusy.value, [name]: true }
  try {
    const registration = await orgProviders.instructions(name)
    if (canManageSelfHostedCredentials.value && requestGeneration === selfHostRequestGeneration && dialogScope.value === targetScope && instructionName.value === name) {
      activeRegistration.value = registration
    }
  } catch (e) {
    if (canManageSelfHostedCredentials.value && requestGeneration === selfHostRequestGeneration && dialogScope.value === targetScope && instructionName.value === name) {
      instructionError.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    if (requestGeneration === selfHostRequestGeneration) selfHostBusy.value = { ...selfHostBusy.value, [name]: false }
  }
}

function closeInstructions(name: string) {
  if (instructionName.value !== name) return
  selfHostRequestGeneration += 1
  activeRegistration.value = null
  instructionName.value = null
  instructionError.value = null
  selfHostBusy.value = { ...selfHostBusy.value, [name]: false }
}

async function removeSelfHosted(name: string) {
  if (!canManageSelfHostedCredentials.value || confirmingRemoval.value || selfHostRemoval.value[name] || orgProviders.byName[name]?.deleting) return
  const targetOrgUUID = tenant.orgUUID
  const targetScope = dialogScope.value
  const operation = Symbol(name)
  removalOperations.set(name, operation)
  removalLabels.set(name, providerDisplayName(orgProviders.items.find(provider => provider.name === name) ?? providers.byName(name) ?? { name }))
  removalPollProgress.set(name, { checks: 0, failures: 0 })
  stopInventoryPolling()
  selfHostRemoval.value = { ...selfHostRemoval.value, [name]: 'confirming' }
  selfHostError.value = null
  try {
    confirmingRemoval.value = true
    let confirmed: boolean
    try {
      confirmed = await confirmDialog({
        title: `Remove self-hosted "${name}"?`,
        message: 'This deletes the organization’s provider workspace, credential, and API registration. It does not uninstall the Helm release or workloads from your cluster. Workspaces that enabled this provider will stop working until you disable it there.',
        confirmLabel: 'Remove',
        danger: true,
      })
    } finally {
      confirmingRemoval.value = false
    }
    if (!confirmed) return
    // Confirmation is asynchronous. Do not apply a destructive action to a
    // same-named provider after the user has switched organizations or scope,
    // or after their organization policy no longer allows provider changes.
    if (!canManageSelfHostedCredentials.value || tenant.orgUUID !== targetOrgUUID || dialogScope.value !== targetScope) return
    selfHostRemoval.value = { ...selfHostRemoval.value, [name]: 'requesting' }
    selfHostNotice.value = null
    if (instructionName.value === name) closeInstructions(name)
    await orgProviders.remove(name)
    if (removalOperations.get(name) !== operation || dialogScope.value !== targetScope) return
    if (orgProviders.byName[name]) {
      selfHostRemoval.value = { ...selfHostRemoval.value, [name]: 'deleting' }
      scheduleSelfHostRemovalPoll(name, operation, targetOrgUUID, targetScope)
    } else {
      await finishSelfHostRemoval(name, operation, targetOrgUUID, targetScope)
    }
  } catch (e) {
    if (removalOperations.get(name) === operation && dialogScope.value === targetScope) {
      selfHostError.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    if (removalOperations.get(name) === operation && selfHostRemoval.value[name] !== 'deleting') {
      clearSelfHostRemoval(name, operation)
    }
  }
}

function clearSelfHostRemoval(name: string, operation: symbol): void {
  if (removalOperations.get(name) !== operation) return
  const timer = removalPollTimers.get(name)
  if (timer) clearTimeout(timer)
  removalPollTimers.delete(name)
  removalOperations.delete(name)
  removalLabels.delete(name)
  removalPollProgress.delete(name)
  const next = { ...selfHostRemoval.value }
  delete next[name]
  selfHostRemoval.value = next
  if (!removalOperations.size) restartInventoryPolling(false)
}

async function finishSelfHostRemoval(name: string, operation: symbol, targetOrgUUID: string | null, targetScope: string): Promise<void> {
  if (removalOperations.get(name) !== operation || tenant.orgUUID !== targetOrgUUID || dialogScope.value !== targetScope) return
  const label = removalLabels.get(name) ?? providerDisplayName(orgProviders.items.find(provider => provider.name === name) ?? providers.byName(name) ?? { name })
  clearSelfHostRemoval(name, operation)
  toast('ok', `${label} was removed from self-hosting.`, { scope: targetScope })
  await refreshSelfHostingCatalog(targetOrgUUID, targetScope)
}

const removalPollFastChecks = 5
const removalPollFastInterval = 2000
const removalPollSteadyInterval = 15000
function removalPollDelay(name: string): number {
  const progress = removalPollProgress.get(name) ?? { checks: 0, failures: 0 }
  if (progress.failures) return Math.min(removalPollFastInterval * 2 ** progress.failures, removalPollSteadyInterval)
  return progress.checks < removalPollFastChecks ? removalPollFastInterval : removalPollSteadyInterval
}

function loadSelfHostRemovalInventory(targetScope: string): Promise<boolean> {
  if (removalInventoryLoad?.scope === targetScope) return removalInventoryLoad.promise
  const entry: { scope: string; promise: Promise<boolean> } = {
    scope: targetScope,
    promise: Promise.resolve(false),
  }
  entry.promise = orgProviders.load()
    .then(loaded => loaded && !orgProviders.error)
    .catch(() => false)
    .finally(() => {
      if (removalInventoryLoad === entry) removalInventoryLoad = null
    })
  removalInventoryLoad = entry
  return entry.promise
}

function scheduleSelfHostRemovalPoll(name: string, operation: symbol, targetOrgUUID: string | null, targetScope: string, delay = removalPollDelay(name)): void {
  if (removalOperations.get(name) !== operation || removalPollTimers.has(name) || removalPollInFlight.has(name) || !canPollSelfHostingList()) return
  const timer = setTimeout(async () => {
    removalPollTimers.delete(name)
    if (removalOperations.get(name) !== operation || tenant.orgUUID !== targetOrgUUID || dialogScope.value !== targetScope || !canPollSelfHostingList()) return
    removalPollInFlight.set(name, operation)
    try {
      const loaded = await loadSelfHostRemovalInventory(targetScope)
      if (removalOperations.get(name) !== operation || tenant.orgUUID !== targetOrgUUID || dialogScope.value !== targetScope) return
      if (loaded && !orgProviders.error && !orgProviders.byName[name]) {
        await finishSelfHostRemoval(name, operation, targetOrgUUID, targetScope)
        return
      }
      const progress = removalPollProgress.get(name)
      if (progress) {
        if (loaded && !orgProviders.error) {
          progress.checks += 1
          progress.failures = 0
        } else {
          progress.failures += 1
        }
      }
    } finally {
      if (removalPollInFlight.get(name) === operation) removalPollInFlight.delete(name)
      const currentOperation = removalOperations.get(name)
      if (currentOperation === operation && dialogScope.value === targetScope) {
        scheduleSelfHostRemovalPoll(name, operation, targetOrgUUID, targetScope)
      } else if (currentOperation && selfHostRemoval.value[name] === 'deleting') {
        // A resumed operation may have waited behind this stale request. Once
        // the old request settles, give the current operation its own check.
        scheduleSelfHostRemovalPoll(name, currentOperation, tenant.orgUUID, dialogScope.value, 0)
      }
    }
  }, delay)
  removalPollTimers.set(name, timer)
}

function pauseSelfHostRemovalPolling(): void {
  for (const timer of removalPollTimers.values()) clearTimeout(timer)
  removalPollTimers.clear()
}

function resumeSelfHostRemovalPolling(): void {
  for (const [name, operation] of removalOperations) {
    if (selfHostRemoval.value[name] !== 'deleting') continue
    scheduleSelfHostRemovalPoll(name, operation, tenant.orgUUID, dialogScope.value, 0)
  }
}

function stopSelfHostRemovalPolling(): void {
  for (const timer of removalPollTimers.values()) clearTimeout(timer)
  removalPollTimers.clear()
  removalOperations.clear()
  removalLabels.clear()
  removalPollProgress.clear()
  removalPollInFlight.clear()
  selfHostRemoval.value = {}
}

function trackDeletingSelfHostedProviders(): void {
  if (!inventoryCurrent.value) return
  for (const provider of orgProviders.items) {
    if (!provider.deleting || removalOperations.has(provider.name)) continue
    const operation = Symbol(provider.name)
    removalOperations.set(provider.name, operation)
    removalLabels.set(provider.name, providerDisplayName(provider))
    removalPollProgress.set(provider.name, { checks: 0, failures: 0 })
    stopInventoryPolling()
    selfHostRemoval.value = { ...selfHostRemoval.value, [provider.name]: 'deleting' }
    scheduleSelfHostRemovalPoll(provider.name, operation, tenant.orgUUID, dialogScope.value)
  }
}

// Platform providers offering self-hosting that this org has not taken up yet.
const availableToSelfHost = computed(() =>
  providers.selfHostable.filter((p) => !inventoryCurrent.value || !orgProviders.isSelfHosted(p.name)),
)

// A card is a provider plus the resolved category metadata it belongs to.
// We carry the category on each card (rather than in a section header) so
// the grid can stay flat: categories become a chip inside the block and a
// filter control above it, instead of a separate section per category —
// which produced one header per provider when a category had a single entry.
interface ProviderCard extends ProviderDTO {
  categoryName: string
  categoryIcon: string | null
}

// Synthetic bucket name for providers that declare no category.
const OTHER = 'Other'

// allCards flattens providers into a single, stably-ordered list. Ordering
// matches the side-nav: registry categories first (by declared order), then
// ad-hoc categories alphabetically, then uncategorized ("Other") last; within
// a category, providers sort alphabetically by display name.
const allCards = computed<ProviderCard[]>(() => {
  const known = new Map(providers.categories.map((c) => [c.name, c]))
  const byCat = new Map<string, ProviderDTO[]>()
  const other: ProviderDTO[] = []
  for (const p of providers.items) {
    if (!p.category) {
      other.push(p)
      continue
    }
    const arr = byCat.get(p.category) ?? []
    arr.push(p)
    byCat.set(p.category, arr)
  }
  const names = [...byCat.keys()].sort((a, b) => {
    const ka = known.get(a)
    const kb = known.get(b)
    if (ka && !kb) return -1
    if (!ka && kb) return 1
    if (ka && kb) return (ka.order ?? 0) - (kb.order ?? 0) || a.localeCompare(b)
    return a.localeCompare(b)
  })
  const cards: ProviderCard[] = []
  for (const n of names) {
    const items = byCat.get(n)!.slice().sort((a, b) => a.displayName.localeCompare(b.displayName))
    for (const p of items) cards.push({ ...p, categoryName: n, categoryIcon: known.get(n)?.icon ?? null })
  }
  for (const p of other.sort((a, b) => a.displayName.localeCompare(b.displayName))) {
    cards.push({ ...p, categoryName: OTHER, categoryIcon: null })
  }
  return cards
})

// categoryChips is the ordered, de-duplicated list of categories present in
// the catalog — drives the filter row. Order follows allCards' first
// appearance so it lines up with the (now hidden) section ordering.
const categoryChips = computed(() => {
  const seen = new Map<string, string | null>()
  for (const c of allCards.value) {
    if (!seen.has(c.categoryName)) seen.set(c.categoryName, c.categoryIcon)
  }
  return [...seen.entries()].map(([name, icon]) => ({ name, icon }))
})

// Active filters. selectedCategory === null means "All".
const search = ref('')
const selectedCategory = ref<string | null>(null)

// filteredCards applies the search query (matched against display name,
// provider name, and category) and the active category chip.
const filteredCards = computed<ProviderCard[]>(() => {
  const q = search.value.trim().toLowerCase()
  return allCards.value.filter((c) => {
    if (selectedCategory.value && c.categoryName !== selectedCategory.value) return false
    if (!q) return true
    return (
      c.displayName.toLowerCase().includes(q) ||
      c.name.toLowerCase().includes(q) ||
      c.categoryName.toLowerCase().includes(q)
    )
  })
})

// The catalog renders as one flat grid, but self-managed providers — the ones
// this organization registered and runs itself — get their own labelled block
// at the top. They are a different kind of thing from the platform catalog: the
// org's own team operates them, so "who fixes this when it breaks" has a
// different answer, which matters more to a reader than any category grouping.
//
// Rather than a second <ul> (which would mean duplicating the whole card
// template), the sections are expressed as rows in the existing grid, with
// headers spanning the full width.
// orderedCards is filteredCards with the self-managed ones hoisted to the
// front, each group keeping the category ordering allCards established.
const orderedCards = computed<ProviderCard[]>(() => [
  ...filteredCards.value.filter((c) => providers.isSelfManaged(c)),
  ...filteredCards.value.filter((c) => !providers.isSelfManaged(c)),
])

// sectionHeaderFor returns the header to render immediately above a card, or
// null. Emitting the header from inside the card loop (rather than building a
// separate row list) keeps the card's own template untouched and its `p` in
// scope. Headers appear only when there is actually something to separate — an
// org with no self-managed providers sees the page exactly as before.
function sectionHeaderFor(card: ProviderCard, index: number): { title: string; subtitle: string } | null {
  const own = providers.isSelfManaged(card)
  const previous = index > 0 ? orderedCards.value[index - 1] : null
  const boundary = previous === null || providers.isSelfManaged(previous) !== own
  if (!boundary) return null
  // Nothing self-managed in view → no headers at all, not a lone one.
  if (!orderedCards.value.some((c) => providers.isSelfManaged(c))) return null
  return own
    ? { title: 'Self-hosted', subtitle: 'Providers your organization registered and runs itself.' }
    : { title: 'Platform catalog', subtitle: 'Providers operated by railgrid.' }
}

function categoryIcon(name: string | null): unknown {
  if (!name) return fallbackCategoryIcon
  return categoryIcons[name] ?? fallbackCategoryIcon
}

// The shared flow owns consent, per-provider busy state, and stale-response
// fencing for both this page and the onboarding wizard.
const {
  busy,
  actionError,
  dialogProvider,
  dialogScope,
  openEnableDialog,
  closeEnableDialog,
  onDialogConfirm,
} = useProviderEnableFlow({
  scope: () => `${tenant.orgUUID ?? ''}/${tenant.workspaceUUID ?? ''}/${tenant.workspaceMode}`,
  enable: (p, accept, acceptHubAccess, acceptCompositions) =>
    providers.enable(p, accept, acceptHubAccess, acceptCompositions),
  dependencyError: (p) => {
    const missing = providers.missingDependencies(p)
    return missing.length > 0
      ? `${p.displayName} requires ${providers.dependencyLabels(missing).join(', ')} to be enabled first.`
      : null
  },
})

// Always refetch on mount. The store's initial load happens at app boot
// (App.vue), but new CatalogEntry installs are common while the portal is
// open — users navigate here precisely to see what's now installed, so a
// stale cached list defeats the page's purpose. The store guards against
// concurrent calls so a no-op fast-path is safe.
onMounted(() => {
  inventoryPollingMounted = true
  document.addEventListener('visibilitychange', onInventoryVisibilityChange)
  restartInventoryPolling(false)
  providers.load()
  void loadOrgProviderInventory(true)
  // Loaded up front rather than when the tab opens: the Self-Host actions must
  // never render enabled and then become disabled a moment later.
  orgProviders.loadInstallTargets()
})

watch(dialogScope, () => {
  // Install instructions are scoped to this organization and workspace.
  disableOperations.clear()
  selfHostRequestGeneration += 1
  activeRegistration.value = null
  instructionName.value = null
  instructionError.value = null
  inventoryScope.value = null
  stopSelfHostRemovalPolling()
  clearProviderPageRouteState()
  selfHostError.value = null
  selfHostNotice.value = null
  void loadOrgProviderInventory(true)
  void orgProviders.loadInstallTargets()
})

// APIBinding deletion is asynchronous. Keep the card current until kcp has
// removed the binding, including when deletion-blocking conditions change.
let deletionPollTimer: ReturnType<typeof setTimeout> | null = null
let deletionPollGeneration = 0
function hasPendingDeletion() {
  return Object.values(providers.bindingsByProvider).some(binding => binding.terminating)
}
function stopDeletionPolling() {
  deletionPollGeneration += 1
  if (deletionPollTimer) clearTimeout(deletionPollTimer)
  deletionPollTimer = null
}
function scheduleDeletionPoll(generation: number) {
  deletionPollTimer = setTimeout(async () => {
    deletionPollTimer = null
    try {
      await providers.refreshBindings()
    } catch {
      // The store exposes refresh errors and retains the last known state.
    } finally {
      if (generation === deletionPollGeneration && hasPendingDeletion()) scheduleDeletionPoll(generation)
    }
  }, 2500)
}
watch(() => `${dialogScope.value}:${hasPendingDeletion()}`, () => {
  stopDeletionPolling()
  if (hasPendingDeletion()) scheduleDeletionPoll(deletionPollGeneration)
}, { immediate: true })
watch(
  () => `${inventoryCurrent.value}:${orgProviders.items.filter(p => p.deleting).map(p => p.name).sort().join(',')}`,
  trackDeletingSelfHostedProviders,
  { immediate: true },
)
onUnmounted(() => {
  inventoryPollingMounted = false
  stopInventoryPolling()
  document.removeEventListener('visibilitychange', onInventoryVisibilityChange)
  stopDeletionPolling()
  stopSelfHostRemovalPolling()
})

watch(tab, () => {
  selfHostRequestGeneration += 1
  activeRegistration.value = null
  instructionName.value = null
  instructionError.value = null
})

async function onDisable(p: ProviderDTO) {
  if (busy.value[p.name] || disableOperations.has(p.name)) return
  const operation = Symbol(p.name)
  const scope = dialogScope.value
  const targetOrgUUID = tenant.orgUUID
  const targetWorkspaceUUID = tenant.workspaceUUID
  const targetWorkspaceMode = tenant.workspaceMode
  const bindingBefore = currentWorkspaceBinding(p)
  const migrationConfirmRequired = selfHostedPlatformBinding(p)
  const staleShadowBinding = providers.isSelfManaged(p) && !!p.shadowsPlatform &&
    providers.isEnabled(p.name) && !bindingBefore
  const isCurrent = () => disableOperations.get(p.name) === operation &&
    dialogScope.value === scope && tenant.orgUUID === targetOrgUUID &&
    tenant.workspaceUUID === targetWorkspaceUUID && tenant.workspaceMode === targetWorkspaceMode

  if (staleShadowBinding) {
    actionError.value = 'Workspace provider details are not current. Refresh the Catalog before disabling this provider.'
    return
  }
  disableOperations.set(p.name, operation)
  actionError.value = null
  try {
    if (migrationConfirmRequired) {
      migrationConfirming.value = { ...migrationConfirming.value, [p.name]: true }
      const confirmed = await confirmDialog({
        title: `Disable the platform copy of ${providerDisplayName(p)}?`,
        message: 'This workspace is currently bound to the platform copy. Disabling removes that binding and can delete resources created through the provider. After disabling completes, use Enable on this Catalog entry to bind the workspace to your organization’s copy. Back up any important resources before proceeding.',
        confirmLabel: 'Disable platform copy',
        danger: true,
      })
      if (!confirmed || !isCurrent()) return

      const currentProvider = providers.byName(p.name)
      if (!currentProvider || !providers.isSelfManaged(currentProvider) || !currentProvider.shadowsPlatform) return
      await providers.refreshBindings()
      if (!isCurrent()) return
      const bindingAfter = currentWorkspaceBinding(currentProvider)
      if (!bindingAfter || bindingIdentity(bindingAfter) !== bindingIdentity(bindingBefore!)) {
        actionError.value = 'This workspace provider binding changed while confirmation was open. Review the current Catalog status before trying again.'
        return
      }
    }
    if (!isCurrent()) return
    busy.value = { ...busy.value, [p.name]: true }
    await providers.disable(p)
  } catch (e) {
    if (isCurrent()) {
      actionError.value = e instanceof Error ? e.message : String(e)
    }
  } finally {
    if (disableOperations.get(p.name) === operation) {
      disableOperations.delete(p.name)
      const confirming = { ...migrationConfirming.value }
      delete confirming[p.name]
      migrationConfirming.value = confirming
      const next = { ...busy.value }
      delete next[p.name]
      busy.value = next
    }
  }
}

function dependencyNotice(p: ProviderDTO): string {
  const missing = providers.missingDependencies(p)
  if (missing.length === 0) return ''
  return `Requires ${providers.dependencyLabels(missing).join(', ')}.`
}
</script>

<template>
  <AppLayout>
    <SelfHostWizard
      v-if="installProvider"
      :key="`${dialogScope}/${installProvider.name}`"
      :name="installProvider.name"
      :display-name="providerDisplayName(installProvider)"
      :resume="installProvider.resume"
      :manual="installProvider.manual"
      @cancel="cancelInstall"
      @manual="resumeManualInstall"
      @completed="finishInstall"
    />
    <div v-else>
      <header class="mb-6">
        <h1 class="text-xl font-semibold text-text-primary flex items-center gap-2">
          <Puzzle class="h-5 w-5 text-accent" :stroke-width="1.75" />
          Providers
        </h1>
        <p class="mt-1 text-sm text-text-muted">
          Enable capabilities in a workspace or run a provider for your organization.
        </p>
      </header>

      <div v-if="providers.error" class="mb-4 rounded-lg border border-danger/30 bg-danger-subtle px-3 py-2 text-sm text-danger flex items-start gap-2">
        <AlertCircle class="h-4 w-4 flex-shrink-0 mt-0.5" :stroke-width="1.75" />
        <span>{{ providers.error }}</span>
      </div>

      <div v-if="actionError" class="mb-4 rounded-lg border border-danger/30 bg-danger-subtle px-3 py-2 text-sm text-danger flex items-start gap-2">
        <AlertCircle class="h-4 w-4 flex-shrink-0 mt-0.5" :stroke-width="1.75" />
        <span>{{ actionError }}</span>
      </div>

      <!-- Catalog vs Self-Hosting. Hidden entirely when the hub has no
           org-provider support, so the tab never leads to a dead surface. -->
      <nav v-if="!inventoryCurrent || orgProviders.supported" class="k-tabs mb-5" aria-label="Provider sections">
        <button
          v-for="t in tabs"
          :key="t.key"
          type="button"
          class="k-tab"
          :class="{ 'k-tab--active': tab === t.key }"
          :aria-current="tab === t.key ? 'page' : undefined"
          @click="setTab(t.key)"
        >
          {{ t.label }}
          <span
            v-if="t.key === 'self-hosting' && inventoryCurrent && orgProviders.items.length"
            class="k-tab__count"
          >{{ orgProviders.items.length }}</span>
        </button>
      </nav>

      <div v-if="providers.loading && !providers.loaded" class="text-sm text-text-muted">
        Loading providers&hellip;
      </div>

      <div v-else-if="providers.items.length === 0 && tab !== 'self-hosting'" class="rounded-lg border border-border-subtle bg-surface-raised/60 p-6 text-center text-text-muted">
        No providers installed yet.
        <div class="mt-2 text-xs">
          See <code>docs/providers.md</code> and <code>providers/quickstart/</code> for an example.
        </div>
      </div>

      <!-- ===== Self-Hosting tab ===== -->
      <div v-else-if="tab === 'self-hosting'" class="space-y-6">
        <p v-if="!inventoryCurrent && orgProviders.loading" role="status" class="text-sm text-text-muted">Loading self-hosted providers…</p>
        <div v-if="inventoryScope === currentTenantScope && orgProviders.error" role="alert" class="text-sm text-danger">
          Could not load self-hosted providers. Last known results are preserved.
          <button type="button" class="k-btn k-btn--ghost" :disabled="orgProviders.loading" @click="refreshOrgProviderInventory">Retry</button>
        </div>
        <p class="text-sm text-text-muted">
          Run a provider inside your own cluster instead of using the platform's copy. railgrid
          prepares an isolated organization workspace and can install the provider through a
          connected Edge. Your workspaces enable your copy separately, like any other provider.
        </p>
        <p class="-mt-3 text-[11px] text-text-muted">
          The cluster must be connected to railgrid as an edge first. railgrid reaches a
          self-hosted provider over that cluster's outbound tunnel — there is no route
          into your network from the platform side.
        </p>

        <div
          v-if="selfHostError"
          class="rounded-lg border border-danger/30 bg-danger-subtle px-3 py-2 text-sm text-danger flex items-start gap-2"
          role="alert"
          aria-live="assertive"
        >
          <AlertCircle class="h-4 w-4 flex-shrink-0 mt-0.5" :stroke-width="1.75" />
          <span>{{ selfHostError }}</span>
        </div>
        <p v-if="selfHostNotice" class="text-sm text-success" role="status" aria-live="polite">{{ selfHostNotice }}</p>

        <!-- No cluster the hub can reach → say so, and say what to do about it.
             The Self-Host buttons below are disabled off the same condition, so
             this block is the explanation for them, not a separate warning. -->
        <div
          v-if="orgProviders.installTargetsLoaded && !canSelfHost"
          class="rounded-lg border border-warning/30 bg-warning-subtle px-3 py-2.5 text-sm text-warning flex items-start gap-2"
        >
          <AlertTriangle class="h-4 w-4 flex-shrink-0 mt-0.5" :stroke-width="1.75" />
          <div class="min-w-0">
            <p>{{ orgProviders.installTargetsReason || 'no connected cluster to install into' }}</p>
            <button
              v-if="orgProviders.installTargetsError"
              type="button"
              class="mt-1 inline-flex items-center gap-1 font-medium underline disabled:cursor-wait disabled:opacity-60"
              :disabled="orgProviders.installTargetsLoading"
              @click="orgProviders.loadInstallTargets"
            >
              <Loader2 v-if="orgProviders.installTargetsLoading" class="h-3 w-3 animate-spin" :stroke-width="2" />
              <RefreshCw v-else class="h-3 w-3" :stroke-width="2" />
              Retry check
            </button>
            <router-link
              v-else-if="edgesSelfHostState === 'ready'"
              :to="scopePath('/providers/edges')"
              class="mt-1 inline-flex items-center gap-1 text-[11px] font-medium underline"
            >
              Connect a Kubernetes cluster
              <ExternalLink class="h-3 w-3" :stroke-width="2" />
            </router-link>
            <div v-else-if="edgesSelfHostState === 'unready'" class="mt-1 text-[11px] leading-relaxed">
              <p>Edges is installed but is not ready. Open its catalog entry for status, or ask a platform administrator to repair it.</p>
              <button
                type="button"
                class="mt-1 font-medium underline"
                @click="showEdgesCatalogEntry"
              >
                View Edges in catalog
              </button>
            </div>
            <p v-else-if="edgesSelfHostState === 'absent'" class="mt-1 text-[11px] leading-relaxed">
              Edges is not installed in this catalog. Ask a platform administrator to install it before self-hosting a provider.
            </p>
          </div>
        </div>

        <!-- What this org already runs itself. -->
        <section v-if="inventoryCurrent && orgProviders.items.length">
          <h2 ref="selfHostedHeading" tabindex="-1" class="mb-2 text-xs font-semibold uppercase tracking-wider text-text-secondary">
            Self-hosted providers
          </h2>
          <ul class="space-y-2">
            <li
              v-for="p in orgProviders.items"
              :key="p.name"
              :aria-busy="!!selfHostRemoval[p.name] || p.deleting || undefined"
              class="flex flex-wrap items-center gap-3 rounded-xl border border-border-subtle bg-surface-raised/60 p-4"
            >
              <div class="flex h-9 w-9 flex-shrink-0 items-center justify-center rounded-lg border border-border-subtle bg-surface-overlay">
                <Server class="h-4 w-4 text-accent" :stroke-width="1.75" />
              </div>
              <div class="min-w-0 flex-[1_1_12rem]">
                <div class="flex flex-wrap items-center gap-2">
                  <h3 class="truncate text-sm font-semibold text-text-primary">
                    {{ providerDisplayName(p) }}
                  </h3>
                  <span
                    class="rounded-sm px-1.5 py-px text-[9px] font-semibold uppercase tracking-wider"
                    :class="
                      p.ready && !p.deleting
                        ? 'border border-success/30 bg-success-subtle text-success'
                        : 'border border-warning/30 bg-warning-subtle text-warning'
                    "
                  >
                    {{ p.deleting || selfHostRemoval[p.name] === 'requesting' || selfHostRemoval[p.name] === 'deleting' ? 'Removing' : p.ready ? 'Ready' : p.registered ? 'Not ready' : 'Awaiting install' }}
                  </span>
                  <span
                    v-if="p.upgradeAvailable && !p.deleting"
                    class="rounded-sm border border-warning/30 bg-warning-subtle px-1.5 py-px text-[9px] font-semibold uppercase tracking-wider text-warning"
                  >
                    Update available
                  </span>
                </div>
                <p class="mt-0.5 truncate font-mono text-[10px] text-text-muted">
                  {{ p.name }}
                </p>
                <p class="mt-1 flex flex-wrap gap-x-3 gap-y-1 text-[10px] text-text-muted">
                  <span>
                    Hosting Edge: <strong class="font-medium text-text-secondary">{{ p.hostingEdge?.name || 'Unavailable' }}</strong>
                    <template v-if="p.hostingEdge"> · Workspace: {{ p.hostingEdge.workspaceDisplayName || p.hostingEdge.workspace }}</template>
                  </span>
                  <span>
                    Version: {{ p.installedVersion || p.version || 'Not reported' }}<span
                      v-if="p.upgradeAvailable && p.availableVersion"
                    > · published {{ p.availableVersion }}</span>
                  </span>
                  <span>Health: {{ p.deleting ? 'Removing' : p.ready ? 'Ready' : p.registered ? 'Not ready' : 'Awaiting install' }}</span>
                </p>
                <!-- The gap between "workspace exists" and "chart installed" is
                     where a half-finished install sits; say so explicitly. -->
                <p v-if="p.deleting || selfHostRemoval[p.name] === 'requesting' || selfHostRemoval[p.name] === 'deleting'" class="mt-1 text-[11px] text-text-muted" role="status">
                  Removing the organization workspace and API registration. The Helm release in your cluster is not removed.
                </p>
                <p v-else-if="selfHostRemoval[p.name] === 'confirming'" class="mt-1 text-[11px] text-text-muted" role="status">
                  Waiting for removal confirmation.
                </p>
                <p v-else-if="!p.registered" class="mt-1 text-[11px] text-text-muted">
                  The workspace is ready but the provider has not registered itself yet.
                  Continue installation to run the commands and watch for readiness.
                </p>
                <p v-else-if="!p.ready" class="mt-1 text-[11px] text-text-muted">
                  The provider registered, but is not ready. Resume to check its status and installation.
                </p>
                <p v-else-if="p.upgradeAvailable && canManageSelfHostedCredentials && !p.deleting" class="mt-1 text-[11px] text-text-muted">
                  A newer chart is available.
                </p>
                <p v-else-if="p.upgradeAvailable && !p.deleting" class="mt-1 text-[11px] text-text-muted">
                  A newer chart is available.
                </p>
              </div>
              <div class="ml-auto flex flex-shrink-0 items-center gap-2">
                <button
                  v-if="!p.ready && !p.deleting"
                  type="button"
                  class="k-btn k-btn--ghost px-2.5 py-1 text-[11px] text-text-muted transition-colors hover:text-accent disabled:opacity-60"
                  :disabled="!!selfHostBusy[p.name] || !!selfHostRemoval[p.name]"
                  @click="startInstall(p)"
                >
                  <RefreshCw class="h-3 w-3" :stroke-width="2" />
                  Continue installation
                </button>
                <button
                  v-if="canManageSelfHostedCredentials"
                  type="button"
                  class="k-btn k-btn--danger px-2.5 py-1 text-[11px] disabled:opacity-60"
                  :disabled="confirmingRemoval || !!selfHostBusy[p.name] || !!selfHostRemoval[p.name] || !!p.deleting"
                  @click="removeSelfHosted(p.name)"
                >
                  <Loader2 v-if="selfHostRemoval[p.name] || p.deleting" class="h-3 w-3 animate-spin" :stroke-width="2" />
                  <Trash2 v-else class="h-3 w-3" :stroke-width="2" />
                  {{ selfHostRemoval[p.name] === 'confirming' ? 'Confirming…' : selfHostRemoval[p.name] || p.deleting ? 'Removing…' : 'Remove' }}
                </button>
              </div>
              <div
                v-if="instructionName === p.name"
                :id="`self-hosted-instructions-${p.name}`"
                class="mt-4 w-full basis-full border-t border-border-subtle pt-4"
              >
                <div class="mb-3 flex items-start justify-between gap-3">
                  <div>
                    <h4 class="text-sm font-semibold text-text-primary">
                      {{ activeRegistration?.provider.name === p.name && activeRegistration.provider.upgradeAvailable ? 'Manual upgrade instructions' : 'Manual commands and credentials' }}
                    </h4>
                    <p class="mt-1 text-[11px] text-text-muted">
                      Run these commands on this provider’s hosting cluster.
                    </p>
                  </div>
                  <button type="button" class="k-btn k-btn--ghost p-1" aria-label="Close installation details" @click="closeInstructions(p.name)">
                    <X class="h-4 w-4" :stroke-width="2" aria-hidden="true" />
                  </button>
                </div>
                <p v-if="selfHostBusy[p.name]" role="status" class="text-sm text-text-muted">Loading installation details…</p>
                <div v-else-if="instructionError" class="text-sm text-danger" role="alert">
                  {{ instructionError }}
                  <button type="button" class="k-btn k-btn--ghost ml-2" @click="showInstructions(p.name)">Retry</button>
                </div>
                <SelfHostInstructions v-else-if="activeRegistration?.provider.name === p.name" :registration="activeRegistration" />
              </div>
            </li>
          </ul>
        </section>

        <!-- What could be self-hosted but isn't yet. -->
        <section>
          <h2 class="mb-2 text-xs font-semibold uppercase tracking-wider text-text-secondary">
            Available to self-host
          </h2>
          <div
            v-if="availableToSelfHost.length === 0"
            class="rounded-lg border border-border-subtle bg-surface-raised/60 p-6 text-center text-sm text-text-muted"
          >
            {{
              inventoryCurrent && orgProviders.items.length
                ? 'You are already self-hosting every provider that offers it.'
                : 'No provider in this catalog publishes a self-hosting recipe yet.'
            }}
          </div>
          <ul v-else class="grid grid-cols-[repeat(auto-fill,minmax(240px,320px))] justify-start gap-3">
            <li
              v-for="p in availableToSelfHost"
              :key="p.name"
              class="rounded-xl border border-border-subtle bg-surface-raised/60 p-4 transition-colors hover:border-accent/30"
            >
              <div class="flex items-start gap-3">
                <div class="flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-lg border border-border-subtle bg-surface-overlay">
                  <img v-if="p.iconURL" :src="p.iconURL" alt="" class="h-6 w-6" @error="(e) => ((e.target as HTMLImageElement).style.display = 'none')" />
                  <Puzzle v-else class="h-5 w-5 text-text-muted" :stroke-width="1.75" />
                </div>
                <div class="min-w-0 flex-1">
                  <h3 class="truncate text-sm font-semibold text-text-primary">{{ p.displayName }}</h3>
                  <p class="mt-0.5 truncate font-mono text-[10px] text-text-muted">
                    {{ p.name }}<span v-if="p.version"> · {{ p.version }}</span>
                  </p>
                </div>
              </div>
              <p v-if="p.description" class="mt-3 line-clamp-3 text-[11px] leading-relaxed text-text-muted">
                {{ p.description }}
              </p>
              <div class="mt-4 flex items-center gap-2">
                <button
                  type="button"
                  class="k-btn k-btn--primary px-2.5 py-1 text-[11px] disabled:cursor-not-allowed disabled:opacity-50"
                  :disabled="!!selfHostBusy[p.name] || !canSelfHost"
                  :title="canSelfHost ? undefined : (orgProviders.installTargetsReason ?? 'no connected cluster to install into')"
                  @click="startInstall(p)"
                >
                  <Loader2 v-if="selfHostBusy[p.name]" class="h-3 w-3 animate-spin" :stroke-width="2" />
                  <Server v-else class="h-3 w-3" :stroke-width="2" />
                  Self-host
                </button>
                <a
                  v-if="p.serving?.selfHosting?.docsURL"
                  :href="p.serving?.selfHosting?.docsURL"
                  target="_blank"
                  rel="noreferrer noopener"
                  class="inline-flex items-center gap-1 text-[11px] text-text-muted hover:text-accent"
                >
                  Docs
                  <ExternalLink class="h-3 w-3" :stroke-width="2" />
                </a>
              </div>
            </li>
          </ul>
        </section>
      </div>

      <!-- ===== Catalog tab ===== -->
      <div v-else>
        <!-- Search + category filter. The grid stays flat (one card per
             provider); categories are a filter here and a chip on each card
             rather than a per-category section header. -->
        <div class="mb-4 flex flex-wrap items-center gap-3">
          <div class="relative w-full sm:w-80 sm:max-w-full">
            <Search class="pointer-events-none absolute left-2.5 top-1/2 h-4 w-4 -translate-y-1/2 text-text-muted" :stroke-width="1.75" />
            <label for="provider-catalog-search" class="sr-only">Search providers</label>
            <input
              id="provider-catalog-search"
              v-model="search"
              type="search"
              aria-label="Search providers"
              placeholder="Search providers…"
              class="k-input w-full bg-surface-raised/60 py-1.5 pl-8 pr-3 text-sm"
            />
          </div>
          <div class="flex flex-wrap items-center gap-1.5" role="group" aria-label="Filter providers by category">
            <button
              type="button"
              class="k-btn k-btn--ghost rounded-sm px-2.5 py-1 text-[11px] font-medium transition-colors"
              :aria-pressed="selectedCategory === null"
              :class="
                selectedCategory === null
                  ? 'border-accent/40 bg-accent/10 text-accent'
                  : 'border-border-subtle text-text-muted hover:border-accent/30'
              "
              @click="selectedCategory = null"
            >
              All
            </button>
            <button
              v-for="chip in categoryChips"
              :key="chip.name"
              type="button"
              class="k-btn k-btn--ghost inline-flex items-center gap-1 rounded-sm px-2.5 py-1 text-[11px] font-medium transition-colors"
              :aria-pressed="selectedCategory === chip.name"
              :class="
                selectedCategory === chip.name
                  ? 'border-accent/40 bg-accent/10 text-accent'
                  : 'border-border-subtle text-text-muted hover:border-accent/30'
              "
              @click="selectedCategory = selectedCategory === chip.name ? null : chip.name"
            >
              <component :is="categoryIcon(chip.icon)" class="h-3 w-3" :stroke-width="2" />
              {{ chip.name }}
            </button>
          </div>
        </div>

        <div
          v-if="filteredCards.length === 0"
          class="rounded-lg border border-border-subtle bg-surface-raised/60 p-6 text-center text-sm text-text-muted"
        >
          No providers match your search.
        </div>

        <ul v-else class="grid grid-cols-[repeat(auto-fill,minmax(240px,320px))] justify-start gap-3">
          <template v-for="(p, i) in orderedCards" :key="p.name">
          <!-- Full-width section divider inside the same grid, so the cards
               below it keep their column alignment. -->
          <li v-if="sectionHeaderFor(p, i)" class="col-span-full mt-2 first:mt-0">
            <h2 class="text-xs font-semibold uppercase tracking-wider text-text-secondary">
              {{ sectionHeaderFor(p, i)!.title }}
            </h2>
            <p class="mt-0.5 text-[11px] text-text-muted">{{ sectionHeaderFor(p, i)!.subtitle }}</p>
          </li>
          <li
            class="rounded-xl border border-border-subtle bg-surface-raised/60 p-4 transition-colors hover:border-accent/30"
          >
          <div class="flex items-start gap-3">
            <div class="flex h-10 w-10 flex-shrink-0 items-center justify-center rounded-lg border border-border-subtle bg-surface-overlay">
              <img v-if="p.iconURL" :src="p.iconURL" alt="" class="h-6 w-6" @error="(e) => ((e.target as HTMLImageElement).style.display = 'none')" />
              <Puzzle v-else class="h-5 w-5 text-text-muted" :stroke-width="1.75" />
            </div>
            <div class="min-w-0 flex-1">
              <div class="flex items-center gap-2">
                <h2 class="truncate text-sm font-semibold text-text-primary">{{ p.displayName }}</h2>
                <span
                  class="rounded-sm px-1.5 py-px text-[9px] font-semibold uppercase tracking-wider"
                  :class="providerStatusClass(p)"
                >
                  {{ providerStatusLabel(p) }}
                </span>
              </div>
              <p class="mt-0.5 truncate font-mono text-[10px] text-text-muted">{{ p.name }}<span v-if="p.version"> · {{ p.version }}</span></p>
            </div>
          </div>

          <!-- CatalogEntry.spec.description. Clamped so a long blurb can't make
               one card in the grid taller than its row neighbours. -->
          <p v-if="p.description" class="mt-3 line-clamp-3 text-[11px] leading-relaxed text-text-muted">
            {{ p.description }}
          </p>

          <!-- A disable that kcp cannot finish. The binding is gone from the
               user's point of view the moment they click Disable, but kcp
               first cascade-deletes every CR of the bound APIs — and a CR
               finalizer whose controller is gone holds that open forever.
               kcp's condition message names the exact finalizer/resources, so
               show it verbatim; it is the only actionable clue anywhere. -->
          <div
            v-if="providers.isDisabling(p.name) && providers.deletionBlocked(p.name)"
            class="mt-3 rounded-md border border-warning/30 bg-warning-subtle p-2.5"
          >
            <div class="flex items-center gap-1.5 text-[10px] font-semibold text-warning">
              <AlertTriangle class="h-3 w-3 flex-shrink-0" :stroke-width="2" />
              Disable is stuck
            </div>
            <p class="mt-1 text-[10px] leading-relaxed text-warning">
              {{ providers.deletionBlocked(p.name) }}
            </p>
            <p class="mt-1.5 text-[10px] leading-relaxed text-text-muted">
              Some of this provider's resources are still waiting to be cleaned
              up. If this doesn't resolve on its own, delete the resources listed
              above (or remove their finalizers) to let the disable finish.
            </p>
          </div>

          <!-- An enabled provider that declares hub capabilities nobody here
               accepted (new in its catalog entry, or declined) works without
               them until someone reviews the request. -->
          <div
            v-if="providers.isEnabled(p.name) && providers.pendingHubAccess(p.name).length"
            class="mt-3 rounded-md border border-border-subtle bg-surface-overlay/40 p-2.5"
          >
            <p class="text-[10px] leading-relaxed text-text-muted">
              Asks for access it doesn't have here yet:
              {{ providers.pendingHubAccess(p.name).map((h) => `${h.capability} (${h.scope})`).join(', ') }}.
            </p>
            <button
              type="button"
              class="k-btn k-btn--ghost mt-1 px-1.5 py-0.5 text-[10px] text-accent"
              @click="openEnableDialog(p)"
            >
              Review access
            </button>
          </div>

          <!-- Permission claims the hub will not settle on its own. The hub
               accepts a provider's own requirements as its catalog entry
               changes; what remains is a composition awaiting consent, a claim
               the tenant rejected, or a scope kcp will not change in place. -->
          <div
            v-if="providers.isEnabled(p.name) && providers.claimState(p.name)"
            class="mt-3 rounded-md border border-border-subtle bg-surface-overlay/40 p-2.5"
          >
            <p
              v-if="providers.claimState(p.name)?.pending?.length || providers.claimState(p.name)?.rejected?.length"
              class="text-[10px] leading-relaxed text-text-muted"
            >
              Permissions not in force here:
              {{
                [...(providers.claimState(p.name)?.pending ?? []), ...(providers.claimState(p.name)?.rejected ?? [])]
                  .map((c) => (c.group ? `${c.group}/${c.resource}` : c.resource))
                  .join(', ')
              }}.
            </p>
            <p v-if="providers.claimState(p.name)?.mismatch" class="text-[10px] leading-relaxed text-text-muted">
              The provider narrowed what it asks for since it was enabled here. Disable and re-enable to apply the
              new scope.
            </p>
            <button
              v-if="providers.claimState(p.name)?.pending?.length || providers.claimState(p.name)?.rejected?.length"
              type="button"
              class="k-btn k-btn--ghost mt-1 px-1.5 py-0.5 text-[10px] text-accent"
              @click="openEnableDialog(p)"
            >
              Review access
            </button>
          </div>

          <!-- A pending composition is the visible cause of "enabled, but it
               never builds anything": the provider's reconciler is refused the
               identity rules for these kinds until an admin accepts them. -->
          <div
            v-if="providers.isEnabled(p.name) && providers.pendingCompositions(p.name).length"
            class="mt-3 rounded-md border border-border-subtle bg-surface-overlay/40 p-2.5"
          >
            <p class="text-[10px] leading-relaxed text-text-muted">
              Waiting to manage resources here:
              {{ providers.pendingCompositions(p.name).map((c) => `${c.group}/${c.resource}`).join(', ') }}.
              Until a workspace or organization admin accepts, it cannot create them.
            </p>
            <button
              type="button"
              class="k-btn k-btn--ghost mt-1 px-1.5 py-0.5 text-[10px] text-accent"
              @click="openEnableDialog(p)"
            >
              Review access
            </button>
          </div>

          <div class="mt-3 flex flex-wrap items-center gap-2 text-[10px] text-text-muted">
            <button
              type="button"
              class="k-btn k-btn--ghost inline-flex items-center gap-1 px-1.5 py-0.5 text-[10px] transition-colors hover:text-accent"
              @click="selectedCategory = selectedCategory === p.categoryName ? null : p.categoryName"
            >
              <component :is="categoryIcon(p.categoryIcon)" class="h-3 w-3" :stroke-width="2" />
              {{ p.categoryName }}
            </button>
            <!-- Repeated on the card, not just the section header: a search or
                 category filter can leave a self-managed card sitting far from
                 its heading. -->
            <span
              v-if="providers.isSelfManaged(p)"
              class="rounded-md border border-accent/30 bg-accent/10 px-1.5 py-0.5 text-accent"
              title="Registered and operated by your organization"
            >
              Self-hosted
            </span>
            <span
              v-if="providers.isSelfManaged(p) && p.shadowsPlatform"
              class="rounded-md border border-warning/30 bg-warning-subtle px-1.5 py-0.5 text-warning"
              title="Your organization’s entry shadows the platform entry, but existing workspace bindings may still use either copy."
            >
              Overrides platform provider
            </span>
            <span v-if="p.serving?.ui" class="rounded-md border border-border-subtle px-1.5 py-0.5">UI</span>
            <!-- serving.backend is published as an empty object: its presence is
                 the whole answer, so test for the section, not its contents. -->
            <span v-if="p.serving?.backend" class="rounded-md border border-border-subtle px-1.5 py-0.5">Backend</span>
            <span v-if="p.export?.name" class="rounded-md border border-border-subtle px-1.5 py-0.5">API</span>
          </div>

          <div
            v-if="!providers.isEnabled(p.name) && dependencyNotice(p)"
            class="mt-3 rounded-lg border border-warning/30 bg-warning-subtle px-3 py-2 text-[11px] text-warning"
          >
            {{ dependencyNotice(p) }}
          </div>

          <div class="mt-4 flex items-center gap-2">
            <!-- Open: only when ready and has UI. Enableable providers
                 (those declaring an APIExport) must also be enabled for
                 this user. Builtin providers go to their in-tree Vue
                 route; third-party load via /providers/{name} →
                 ProviderFrame. -->
            <router-link
              v-if="p.serving?.ui && p.ready && (!p.export?.name || (providers.isEnabled(p.name) && (!providers.isSelfManaged(p) || workspaceProviderCopy(p) === 'organization')))"
              :to="scopePath(p.serving?.ui?.builtinRoute ? `/${p.serving.ui.builtinRoute}` : `/providers/${p.name}`)"
              class="k-btn k-btn--ghost inline-flex items-center gap-1 px-2.5 py-1 text-[11px] font-medium text-accent transition-colors hover:bg-accent-subtle"
            >
              Open
              <ExternalLink class="h-3 w-3" :stroke-width="2" />
            </router-link>

            <!-- Readiness gates new bindings, but never removal of an existing
                 binding: an outage is exactly when Disable may be needed. -->
            <template v-if="p.export?.name">
              <!-- Mid-deletion: neither Enable (name still taken) nor Disable
                   (already deleting) is actionable, so say what's happening. -->
              <span
                v-if="providers.isDisabling(p.name)"
                class="inline-flex items-center gap-1 rounded-lg border border-warning/30 bg-warning-subtle px-2.5 py-1 text-[11px] font-medium text-warning"
              >
                <Loader2 class="h-3 w-3 animate-spin" :stroke-width="2" />
                Disabling&hellip;
              </span>
              <button
                v-else-if="bindingAction(p) === 'enable'"
                type="button"
                class="k-btn k-btn--ghost inline-flex items-center gap-1 px-2.5 py-1 text-[11px] font-medium text-success transition-colors hover:border-success/40 hover:bg-success-subtle disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="!!busy[p.name] || providers.hasMissingDependencies(p)"
                :title="dependencyNotice(p)"
                @click="openEnableDialog(p)"
              >
                <Loader2 v-if="busy[p.name]" class="h-3 w-3 animate-spin" :stroke-width="2" />
                <Plus v-else class="h-3 w-3" :stroke-width="2" />
                Enable
              </button>
              <button
                v-else-if="bindingAction(p) === 'disable'"
                type="button"
                class="k-btn k-btn--ghost inline-flex items-center gap-1 px-2.5 py-1 text-[11px] font-medium text-text-muted transition-colors hover:border-danger/30 hover:text-danger disabled:cursor-not-allowed disabled:opacity-60"
                :disabled="!!busy[p.name] || !!migrationConfirming[p.name] || !canDisableFromThisProjection(p)"
                :title="canDisableFromThisProjection(p) ? undefined : 'Waiting for current workspace binding details.'"
                @click="onDisable(p)"
              >
                <Loader2 v-if="busy[p.name] || migrationConfirming[p.name]" class="h-3 w-3 animate-spin" :stroke-width="2" />
                <X v-else class="h-3 w-3" :stroke-width="2" />
                {{ migrationConfirming[p.name] ? 'Confirming…' : 'Disable' }}
              </button>
            </template>

            <span v-if="!p.ready" class="text-[11px] text-text-muted/70">
              {{ p.readinessMessage || 'Provider is unavailable.' }}
            </span>
          </div>
          </li>
          </template>
        </ul>
      </div>
    </div>

    <ProviderEnableDialog
      :provider="dialogProvider"
      :org-role="tenant.activeOrg?.role"
      :workspace-role="tenant.activeWorkspace?.role"
      :busy="dialogProvider ? !!busy[dialogProvider.name] : false"
      :error="dialogProvider ? actionError : null"
      @cancel="closeEnableDialog"
      @confirm="onDialogConfirm"
    />
  </AppLayout>
</template>
