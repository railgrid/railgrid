<script lang="ts">
import type {
  AvailableProjectIntegration as CardAvailableProjectIntegration,
  ProjectIntegration as CardProjectIntegration,
  ProjectIntegrationsResponse as CardProjectIntegrationsResponse,
  ProjectProviderActionGrant as CardProjectProviderActionGrant,
  ProjectProviderResourceReference as CardProjectProviderResourceReference,
  ProviderAction as CardProviderAction,
} from './types'

export interface ProjectIntegrationCardEntry {
  key: string
  state: 'available' | 'connected'
  environment: string
  alias: string
  provider: string
  kind: string
  resourceRef?: CardProjectProviderResourceReference
  phase?: string
  integration?: CardProjectIntegration
  allowedActions: CardProjectProviderActionGrant[]
  catalogActions: CardProviderAction[]
}

export type ProjectIntegrationConsentState = 'not-required' | 'pending' | 'granted' | 'revoked' | 'changed' | 'unavailable'

export type ProjectIntegrationConsentRequest =
  | {
    method: 'create'
    body: {
      alias: string
      provider: string
      kind: 'providerReference'
      resourceRef: CardProjectProviderResourceReference
      allowedActions: CardProjectProviderActionGrant[]
      consentAccepted: true
    }
  }
  | {
    method: 'patch'
    alias: string
    body: {
      allowedActions: CardProjectProviderActionGrant[]
      consentAccepted: true
    }
  }

const catalogDigestPattern = /^sha256:[a-f0-9]{64}$/

function integrationResourceIdentity(
  environment: string,
  provider: string,
  resourceRef?: CardProjectProviderResourceReference,
): string | null {
  if (!resourceRef?.name || !resourceRef.apiVersion || !resourceRef.kind || !resourceRef.resource) return null
  return JSON.stringify([environment, provider, resourceRef.apiVersion, resourceRef.kind, resourceRef.resource, resourceRef.name])
}

export function projectIntegrationCardEntries(
  items: CardProjectIntegration[],
  available: CardAvailableProjectIntegration[],
): ProjectIntegrationCardEntry[] {
  const connectedIdentities = new Set(
    items
      .map((item) => integrationResourceIdentity(item.environment, item.provider, item.resourceRef))
      .filter((identity): identity is string => identity !== null),
  )
  const candidateByBinding = new Map<string, CardAvailableProjectIntegration>()
  for (const candidate of available) {
    const identity = integrationResourceIdentity(candidate.environment, candidate.provider, candidate.resourceRef)
    const alias = candidate.alias.trim().toLowerCase()
    if (!identity || !alias) continue
    const key = JSON.stringify([identity, alias])
    if (!candidateByBinding.has(key)) candidateByBinding.set(key, candidate)
  }
  const availableIdentities = new Set<string>()
  const connectedCards: ProjectIntegrationCardEntry[] = items.map((integration) => {
    const identity = integrationResourceIdentity(integration.environment, integration.provider, integration.resourceRef)
    const candidateKey = identity
      ? JSON.stringify([identity, integration.alias.trim().toLowerCase()])
      : ''
    const candidate = candidateKey ? candidateByBinding.get(candidateKey) : undefined
    return {
      key: JSON.stringify(['connected', integration.environment, integration.alias, integration.provider]),
      state: 'connected',
      environment: integration.environment,
      alias: integration.alias,
      provider: integration.provider,
      kind: integration.kind,
      resourceRef: integration.resourceRef,
      phase: integration.phase,
      integration,
      allowedActions: integration.allowedActions ?? [],
      catalogActions: candidate?.actions ?? [],
    }
  })
  const availableCards: ProjectIntegrationCardEntry[] = []
  for (const candidate of available) {
    const identity = integrationResourceIdentity(candidate.environment, candidate.provider, candidate.resourceRef)
    if (identity && (connectedIdentities.has(identity) || availableIdentities.has(identity))) continue
    if (identity) availableIdentities.add(identity)
    availableCards.push({
      key: JSON.stringify(['available', candidate.environment, candidate.alias, candidate.provider]),
      state: 'available',
      environment: candidate.environment,
      alias: candidate.alias,
      provider: candidate.provider,
      kind: candidate.kind,
      resourceRef: candidate.resourceRef,
      allowedActions: [],
      catalogActions: candidate.actions ?? [],
    })
  }
  return [...connectedCards, ...availableCards]
}

export function projectIntegrationConsentState(
  entry: ProjectIntegrationCardEntry,
  action: CardProviderAction,
): ProjectIntegrationConsentState {
  if (action.consent?.required !== true) return 'not-required'
  const actionID = action.id.split('/')
  const validAction = actionID.length === 2
    && actionID[0] === action.name
    && actionID[1] === action.version
    && !!action.name.trim()
    && !!action.version.trim()
    && catalogDigestPattern.test(action.schemaDigest)
    && !action.deprecation?.deprecated
  if (!validAction) return 'unavailable'
  if (entry.state === 'available' && (!entry.alias.trim() || entry.environment !== 'development')) return 'unavailable'
  if (entry.state === 'connected' && !entry.integration) return 'unavailable'

  const grant = entry.allowedActions.find((allowed) =>
    allowed.name === action.name && allowed.version === action.version,
  )
  if (!grant) return 'pending'
  if (grant.revoked) return 'revoked'
  return grant.schemaDigest === action.schemaDigest ? 'granted' : 'changed'
}

/** Build a narrowly scoped request only after the user has checked consent. */
export function buildProjectIntegrationConsentRequest(
  entry: ProjectIntegrationCardEntry,
  action: CardProviderAction,
  consentAccepted: boolean,
): ProjectIntegrationConsentRequest | null {
  const state = projectIntegrationConsentState(entry, action)
  if (!consentAccepted || state === 'granted' || state === 'unavailable' || state === 'not-required') return null

  const [name, version] = action.id.split('/')
  const selectedGrant: CardProjectProviderActionGrant = {
    name,
    version,
    schemaDigest: action.schemaDigest,
  }
  if (entry.state === 'connected' && entry.integration) {
    const allowedActions = entry.allowedActions
      .filter((grant) => grant.name !== name || grant.version !== version)
      .map((grant) => ({
        name: grant.name,
        version: grant.version,
        schemaDigest: grant.schemaDigest,
        ...(grant.revoked === undefined ? {} : { revoked: grant.revoked }),
      }))
    allowedActions.push(selectedGrant)
    return {
      method: 'patch',
      alias: entry.integration.alias,
      body: { allowedActions, consentAccepted: true },
    }
  }

  if (entry.state !== 'available' || !entry.resourceRef || !entry.alias.trim() || !entry.provider.trim()) return null
  return {
    method: 'create',
    body: {
      alias: entry.alias,
      provider: entry.provider,
      kind: 'providerReference',
      resourceRef: entry.resourceRef,
      allowedActions: [selectedGrant],
      consentAccepted: true,
    },
  }
}

/** Project bindings remain authoritative even when read-only discovery fails. */
export function projectIntegrationItemsAfterResponse(
  _previous: CardProjectIntegration[],
  response: CardProjectIntegrationsResponse,
): CardProjectIntegration[] {
  return response.items
}

export function projectIntegrationErrorMessage(error: unknown, fallback: string): string {
  const message = error instanceof Error
    ? error.message
    : typeof error === 'string'
      ? error
      : ''
  return message.trim() || fallback
}

export function projectIntegrationMatchesSearch(
  entry: ProjectIntegrationCardEntry,
  query: string,
  providers: Array<{ name: string; displayName?: string }>,
): boolean {
  const search = query.trim().toLowerCase()
  if (!search) return true
  const provider = providers.find((candidate) => candidate.name === entry.provider)
  return [entry.resourceRef?.name, entry.alias, entry.provider, provider?.displayName, entry.resourceRef?.kind, entry.kind]
    .some((value) => value?.toLowerCase().includes(search))
}
</script>

<script setup lang="ts">
import AIWorkbenchPane from './agentkit/AIWorkbenchPane.vue'
import AIWorkbenchSection from './agentkit/AIWorkbenchSection.vue'
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import {
  ChevronRight,
  KeyRound,
  Loader2,
  RefreshCw,
  Search,
  ShieldCheck,
  Undo2,
  X,
} from 'lucide-vue-next'
import { api } from './api'
import { confirmDialog } from './portalkit/confirm'
import InlineNotification from './portalkit/InlineNotification.vue'
import StatusBadge from './portalkit/StatusBadge.vue'
import {
  buildProjectIntegrationRevokePayload,
  projectIntegrationsAuthorityKey,
  projectIntegrationsRequestIsCurrent,
} from './projectIntegrations'
import type {
  AvailableProjectIntegration,
  RailgridContext,
  ProjectIntegration,
  ProjectIntegrationsDiscovery,
  ProviderAction,
  ProviderItem,
} from './types'

const props = withDefaults(defineProps<{
  ctx: RailgridContext | null
  projectName: string
  providers: ProviderItem[]
  providersLoading?: boolean
}>(), {
  providersLoading: false,
})

const integrations = ref<ProjectIntegration[]>([])
const availableIntegrations = ref<AvailableProjectIntegration[]>([])
const discovery = ref<ProjectIntegrationsDiscovery | null>(null)
const integrationsLoaded = ref(false)
const loading = ref(false)
const busy = ref(false)
const error = ref<string | null>(null)
const formError = ref<string | null>(null)
const notice = ref<string | null>(null)
const search = ref('')
const searchInput = ref<HTMLInputElement | null>(null)
const consentReviewKey = ref('')
const acceptedConsentActionKey = ref('')
const consentError = ref<{ key: string; message: string } | null>(null)
let integrationsRequestSerial = 0
let integrationMutationSerial = 0

const integrationCards = computed(() => projectIntegrationCardEntries(integrations.value, availableIntegrations.value))
const hasIntegrationCards = computed(() => integrationCards.value.length > 0)
const visibleIntegrationCards = computed(() => integrationCards.value.filter((entry) =>
  projectIntegrationMatchesSearch(entry, search.value, props.providers),
))
const integrationGroups = computed(() => [
  { state: 'connected' as const, label: 'Connected', entries: visibleIntegrationCards.value.filter((entry) => entry.state === 'connected') },
  { state: 'available' as const, label: 'Available', entries: visibleIntegrationCards.value.filter((entry) => entry.state === 'available') },
].filter((group) => group.entries.length))

function clearSearch() {
  search.value = ''
  searchInput.value?.focus()
}
const discoveryNotification = computed(() => {
  const current = discovery.value
  if (!current || current.state === 'available') return null
  const issues = current.issues.map((issue) => {
    const scope = [issue.provider, issue.resource].filter(Boolean).join(' · ')
    return scope ? `${scope}: ${issue.message}` : issue.message
  })
  return {
    tone: current.state === 'partial' ? 'warning' as const : 'error' as const,
    title: current.state === 'partial' ? 'Provider resource discovery is incomplete' : 'Provider resource discovery is unavailable',
    message: issues.length
      ? issues.join(' ')
      : current.state === 'partial'
        ? 'Some provider resources could not be checked. Results may be incomplete.'
        : 'Provider resources could not be checked. Refresh to try again.',
  }
})
const integrationsErrorMessage = computed(() => (
  integrationsLoaded.value
    ? `Could not refresh provider access. Showing the last confirmed results. ${projectIntegrationErrorMessage(error.value, 'The request returned no error details.')}`
    : `Could not load provider access. ${projectIntegrationErrorMessage(error.value, 'The request returned no error details.')}`
))
const integrationsErrorTitle = computed(() => integrationsLoaded.value
  ? 'Could not refresh provider access'
  : 'Could not load provider access')

const integrationsAuthority = computed(() => projectIntegrationsAuthorityKey(props.ctx, props.projectName))

function isCurrentIntegrationsRequest(requestSerial: number, requestAuthority: string): boolean {
  return projectIntegrationsRequestIsCurrent(
    requestSerial,
    requestAuthority,
    integrationsRequestSerial,
    integrationsAuthority.value,
  )
}

function isCurrentIntegrationMutation(requestSerial: number, requestAuthority: string): boolean {
  return requestSerial === integrationMutationSerial && requestAuthority === integrationsAuthority.value
}

watch(
  integrationsAuthority,
  (authority) => {
    // Invalidate every pending read before clearing the old scope. A response
    // from a previous project or tenant must never repopulate this snapshot.
    integrationsRequestSerial += 1
    integrationMutationSerial += 1
    integrations.value = []
    availableIntegrations.value = []
    discovery.value = null
    integrationsLoaded.value = false
    loading.value = false
    busy.value = false
    search.value = ''
    error.value = null
    notice.value = null
    formError.value = null
    closeConsentReview()
    if (props.projectName) void loadIntegrations(authority)
  },
  { immediate: true },
)

async function loadIntegrations(requestAuthority = integrationsAuthority.value) {
  const projectName = props.projectName
  const context = props.ctx
  if (!projectName) return
  const requestSerial = ++integrationsRequestSerial
  loading.value = true
  error.value = null
  try {
    const result = await api.listProjectIntegrations(context, projectName)
    if (!isCurrentIntegrationsRequest(requestSerial, requestAuthority)) return
    integrations.value = projectIntegrationItemsAfterResponse(integrations.value, result)
    availableIntegrations.value = result.available
    discovery.value = result.discovery
    integrationsLoaded.value = true
  } catch (err) {
    if (!isCurrentIntegrationsRequest(requestSerial, requestAuthority)) return
    error.value = projectIntegrationErrorMessage(err, 'The provider access request failed. Retry to check again.')
  } finally {
    if (isCurrentIntegrationsRequest(requestSerial, requestAuthority)) loading.value = false
  }
}

function refreshIntegrations(): void {
  void loadIntegrations()
}

function clearNotice() {
  notice.value = null
}

function consentActionKey(entry: ProjectIntegrationCardEntry, action: ProviderAction): string {
  return JSON.stringify([entry.key, action.id, action.schemaDigest])
}

function consentReviewID(entry: ProjectIntegrationCardEntry, action: ProviderAction): string {
  return `integration-consent-${encodeURIComponent(consentActionKey(entry, action))}`
}

function consentCheckboxID(entry: ProjectIntegrationCardEntry, action: ProviderAction): string {
  return `${consentReviewID(entry, action)}-accepted`
}

function isConsentReviewOpen(entry: ProjectIntegrationCardEntry, action: ProviderAction): boolean {
  return consentReviewKey.value === consentActionKey(entry, action)
}

function isConsentAccepted(entry: ProjectIntegrationCardEntry, action: ProviderAction): boolean {
  return acceptedConsentActionKey.value === consentActionKey(entry, action)
}

function openConsentReview(entry: ProjectIntegrationCardEntry, action: ProviderAction): void {
  const key = consentActionKey(entry, action)
  if (consentReviewKey.value === key) {
    closeConsentReview()
    return
  }
  consentReviewKey.value = key
  acceptedConsentActionKey.value = ''
  consentError.value = null
}

function closeConsentReview(): void {
  consentReviewKey.value = ''
  acceptedConsentActionKey.value = ''
  consentError.value = null
}

function setConsentAccepted(entry: ProjectIntegrationCardEntry, action: ProviderAction, event: Event): void {
  if ((event.target as HTMLInputElement | null)?.checked) {
    acceptedConsentActionKey.value = consentActionKey(entry, action)
  } else {
    acceptedConsentActionKey.value = ''
  }
  consentError.value = null
}

function catalogActionsForCard(entry: ProjectIntegrationCardEntry): ProviderAction[] {
  if (entry.state === 'available') return entry.catalogActions
  return entry.catalogActions.filter((action) =>
    action.consent?.required === true && projectIntegrationConsentState(entry, action) !== 'granted',
  )
}

function consentStatusLabel(entry: ProjectIntegrationCardEntry, action: ProviderAction): string {
  switch (projectIntegrationConsentState(entry, action)) {
    case 'granted': return 'Granted'
    case 'revoked': return 'Previously revoked'
    case 'changed': return 'Catalog contract changed'
    case 'unavailable': return 'Unavailable for new grants'
    default: return 'Consent required'
  }
}

function canConnectConsentAction(entry: ProjectIntegrationCardEntry, action: ProviderAction): boolean {
  return !!props.projectName
    && projectIntegrationConsentState(entry, action) !== 'unavailable'
    && projectIntegrationConsentState(entry, action) !== 'granted'
    && !!buildProjectIntegrationConsentRequest(entry, action, true)
}

async function connectConsentAction(entry: ProjectIntegrationCardEntry, action: ProviderAction): Promise<void> {
  const key = consentActionKey(entry, action)
  if (consentReviewKey.value !== key || acceptedConsentActionKey.value !== key) {
    consentError.value = { key, message: 'Review this action and check the approval box before connecting it.' }
    return
  }
  const request = buildProjectIntegrationConsentRequest(entry, action, true)
  if (!request) {
    consentError.value = { key, message: 'This action can no longer be granted from the current catalog. Refresh discovery and review it again.' }
    return
  }

  const requestAuthority = integrationsAuthority.value
  const context = props.ctx
  const projectName = props.projectName
  const requestSerial = ++integrationMutationSerial
  clearNotice()
  consentError.value = null
  busy.value = true
  try {
    if (request.method === 'create') {
      await api.createProjectIntegration(context, projectName, request.body)
    } else {
      await api.patchProjectIntegration(context, projectName, request.alias, request.body)
    }
    if (!isCurrentIntegrationMutation(requestSerial, requestAuthority)) return
    const actionLabel = action.displayName || `${action.name}/${action.version}`
    notice.value = `${actionLabel} connected for ${entry.alias}. Approval applies only to this project resource.`
    closeConsentReview()
    await loadIntegrations(requestAuthority)
  } catch (err) {
    if (!isCurrentIntegrationMutation(requestSerial, requestAuthority)) return
    consentError.value = {
      key,
      message: projectIntegrationErrorMessage(err, 'The action could not be connected. Review the scope and retry.'),
    }
  } finally {
    if (isCurrentIntegrationMutation(requestSerial, requestAuthority)) busy.value = false
  }
}

async function revokeAction(entry: ProjectIntegrationCardEntry, integration: ProjectIntegration, actionName: string, actionVersion: string) {
  const requestAuthority = integrationsAuthority.value
  const interactionSerial = integrationMutationSerial
  const context = props.ctx
  const projectName = props.projectName
  const actionLabel = actionDisplayName(entry, actionName, actionVersion)
  const confirmed = await confirmDialog({
    title: `Revoke ${actionLabel}?`,
    message: `This project can no longer use ${actionLabel} with ${integration.alias}. Its access history remains available.`,
    confirmLabel: 'Revoke action',
    danger: true,
  })
  if (!confirmed || interactionSerial !== integrationMutationSerial || requestAuthority !== integrationsAuthority.value) return
  const currentIntegration = integrations.value.find((candidate) =>
    candidate.environment === integration.environment
    && candidate.provider === integration.provider
    && candidate.alias === integration.alias,
  )
  if (!currentIntegration) {
    formError.value = 'This integration changed while the confirmation was open. Refresh the list and try again.'
    return
  }
  const payload = buildProjectIntegrationRevokePayload(currentIntegration, actionName, actionVersion)
  if (!payload) return
  const requestSerial = ++integrationMutationSerial
  clearNotice()
  formError.value = null
  busy.value = true
  try {
    await api.patchProjectIntegration(context, projectName, integration.alias, payload)
    if (!isCurrentIntegrationMutation(requestSerial, requestAuthority)) return
    notice.value = `${actionLabel} revoked.`
    await loadIntegrations(requestAuthority)
  } catch (err) {
    if (!isCurrentIntegrationMutation(requestSerial, requestAuthority)) return
    formError.value = projectIntegrationErrorMessage(err, 'The action grant could not be revoked. Retry the request.')
  } finally {
    if (isCurrentIntegrationMutation(requestSerial, requestAuthority)) busy.value = false
  }
}

function providerDisplayName(name: string): string {
  return props.providers.find((provider) => provider.name === name)?.displayName || name
}

function actionDisplayName(entry: ProjectIntegrationCardEntry, action: ProviderAction | string, version?: string): string {
  const id = typeof action === 'string' ? `${action}/${version || ''}` : action.id
  return (typeof action === 'string'
    ? entry.catalogActions.find((candidate) => candidate.id === id)?.displayName
    : action.displayName) || id
}

function formatTimestamp(value?: string): string {
  if (!value) return 'Not recorded'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

onBeforeUnmount(() => {
  // Prevent a deferred read from committing after this resource view leaves
  // the document. The same serial guard handles project and authority swaps.
  integrationsRequestSerial += 1
  integrationMutationSerial += 1
})
</script>

<template>
  <AIWorkbenchPane title="Integrations" description="Manage what this project can access." :aria-busy="loading || busy">
    <template #actions>
      <button
        type="button"
        class="k-btn k-btn--ghost"
        :disabled="loading || busy || !projectName"
        :aria-busy="loading ? 'true' : undefined"
        title="Refresh integrations"
        @click="refreshIntegrations"
      >
        <Loader2 v-if="loading" class="h-3.5 w-3.5 motion-safe:animate-spin" :stroke-width="1.75" aria-hidden="true" />
        <RefreshCw v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
        {{ loading ? 'Refreshing…' : busy ? 'Updating…' : 'Refresh' }}
      </button>
    </template>


    <InlineNotification
      v-if="error"
      tone="error"
      :title="integrationsErrorTitle"
      :message="integrationsErrorMessage"
      action-label="Retry"
      :action-busy="loading"
      action-busy-label="Refreshing…"
      @action="refreshIntegrations"
    />
    <InlineNotification
      v-if="discoveryNotification"
      :tone="discoveryNotification.tone"
      :title="discoveryNotification.title"
      :message="discoveryNotification.message"
      action-label="Retry discovery"
      :action-busy="loading"
      action-busy-label="Retrying…"
      @action="refreshIntegrations"
    />
    <InlineNotification v-if="formError" tone="error" :message="formError" />
    <InlineNotification v-if="notice" tone="success" :message="notice" />

    <section class="grid min-w-0 gap-4" aria-label="Provider resources">
      <div v-if="hasIntegrationCards" class="k-ai-search k-ai-search--clearable">
        <Search class="k-ai-search__icon" :stroke-width="1.75" aria-hidden="true" />
        <input
          ref="searchInput"
          v-model.trim="search"
          type="search"
          class="k-input k-ai-search__input"
          placeholder="Search name, alias, or provider"
          aria-label="Find a resource"
        />
        <button
          v-if="search"
          class="k-ai-search__clear k-agentkit-touch-target"
          type="button"
          aria-label="Clear resource search"
          @click="clearSearch"
        ><X :size="14" :stroke-width="1.75" aria-hidden="true" /></button>
      </div>

      <div v-if="loading && !hasIntegrationCards" class="grid gap-2" role="status" aria-live="polite">
        <p class="text-[12px] text-text-muted">Loading integrations…</p>
        <div v-for="i in 3" :key="i" aria-hidden="true" class="grid gap-2 border-t border-border-subtle py-3">
          <div class="shimmer h-3.5 w-32 rounded bg-surface-overlay" />
          <div class="shimmer h-3 w-48 max-w-full rounded bg-surface-overlay" />
        </div>
      </div>
      <p v-else-if="error && !integrationsLoaded" class="py-2 text-[13px] text-text-secondary" role="status">
        Provider resources could not be loaded. Use Refresh to try again.
      </p>
      <p v-else-if="hasIntegrationCards && !visibleIntegrationCards.length" class="py-2 text-[13px] text-text-secondary" role="status">
        No resources match your search.
      </p>
      <div v-else-if="integrationsLoaded && !hasIntegrationCards && !error && discovery?.state === 'available'" class="py-2" role="status">
        <h3 class="text-[13px] font-semibold text-text-primary">No resources available yet</h3>
        <p class="mt-1 max-w-[68ch] text-[13px] leading-5 text-text-secondary">Create a resource in a provider or ask your workspace administrator for access, then refresh this list.</p>
      </div>
      <p v-else-if="integrationsLoaded && !hasIntegrationCards" class="py-2 text-[13px] text-text-secondary" role="status">
        No provider resources could be confirmed. Use Refresh to check again.
      </p>
      <p v-else-if="!integrationsLoaded && !loading" class="py-2 text-[13px] text-text-secondary" role="status">
        Select a project to view its provider resources.
      </p>
      <div v-else class="grid min-w-0 gap-4">
        <AIWorkbenchSection v-for="group in integrationGroups" :key="group.state" :title="`${group.label} · ${group.entries.length}`">
          <div class="min-w-0 divide-y divide-border-subtle border-t border-border-subtle">
        <details v-for="entry in group.entries" :key="entry.key" class="integration-disclosure min-w-0">
          <summary class="integration-disclosure__trigger py-3 text-text-primary">
            <span class="min-w-0 flex-1 [overflow-wrap:anywhere]">
              <strong class="block text-[13px] font-semibold">{{ entry.resourceRef?.name || entry.alias }}</strong>
              <span class="mt-0.5 block text-[12px] text-text-secondary">{{ [providerDisplayName(entry.provider), entry.resourceRef?.kind || entry.kind, entry.state === 'connected' ? entry.environment : null].filter(Boolean).join(' · ') }}</span>
            </span>
            <StatusBadge v-if="entry.phase && !['Ready', 'Connected', 'Succeeded'].includes(entry.phase)" :status="entry.phase" />
            <ChevronRight class="integration-disclosure__chevron" :stroke-width="1.75" aria-hidden="true" />
          </summary>
          <div class="grid min-w-0 gap-2 pb-3">
          <div v-if="entry.state === 'connected'" class="grid min-w-0 gap-2">

            <div v-for="grant in entry.allowedActions" :key="`${grant.name}/${grant.version}`" class="flex min-w-0 flex-wrap items-center justify-between gap-2 py-1">
              <div class="min-w-0">
                <div class="flex min-w-0 flex-wrap items-center gap-2">
                  <span class="min-w-0 break-words text-[12px] font-medium text-text-primary [overflow-wrap:anywhere]">{{ actionDisplayName(entry, grant.name, grant.version) }}</span>
                  <StatusBadge v-if="grant.revoked" status="Revoked" tone="danger" />
                  <span v-else class="text-[11px] text-text-muted">Allowed</span>
                </div>

              </div>
              <button
                v-if="entry.integration && !grant.revoked"
                type="button"
                class="k-btn k-btn--text shrink-0 text-danger"
                :disabled="busy"
                @click="revokeAction(entry, entry.integration, grant.name, grant.version)"
              >
                <Undo2 class="h-3.5 w-3.5" :stroke-width="1.75" />
                Revoke
              </button>

            </div>
          </div>
          <div v-if="catalogActionsForCard(entry).length" class="grid min-w-0 gap-2">
            <div v-for="action in catalogActionsForCard(entry)" :key="`${action.name}/${action.version}`" class="grid min-w-0 gap-2 border-t border-border-subtle py-3">
              <div class="flex flex-wrap items-center justify-between gap-2">
                <div class="flex min-w-0 flex-wrap items-center gap-2">
                  <span class="min-w-0 break-words text-[12px] font-medium text-text-primary [overflow-wrap:anywhere]">{{ action.displayName || action.id }}</span>

                  <span class="k-badge k-badge--muted">{{ action.readOnly ? 'Read-only' : 'Can make changes' }}</span>
                  <StatusBadge
                    v-if="action.consent?.required"
                    :status="consentStatusLabel(entry, action)"
                    :tone="projectIntegrationConsentState(entry, action) === 'revoked' ? 'danger' : projectIntegrationConsentState(entry, action) === 'unavailable' ? 'muted' : 'warning'"
                  />

                </div>
                <button
                  v-if="action.consent?.required && projectIntegrationConsentState(entry, action) !== 'unavailable' && projectIntegrationConsentState(entry, action) !== 'granted'"
                  type="button"
                  class="k-btn k-btn--ghost app-studio-touch-target h-8 shrink-0"
                  :disabled="busy || !projectName"
                  :aria-expanded="isConsentReviewOpen(entry, action) ? 'true' : 'false'"
                  :aria-controls="isConsentReviewOpen(entry, action) ? consentReviewID(entry, action) : undefined"
                  @click="openConsentReview(entry, action)"
                >
                  <KeyRound class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
                  {{ isConsentReviewOpen(entry, action) ? 'Close review' : 'Review access' }}
                </button>
              </div>
              <p v-if="action.description" class="min-w-0 break-words text-[12px] leading-5 text-text-secondary [overflow-wrap:anywhere]">{{ action.description }}</p>

              <p v-if="projectIntegrationConsentState(entry, action) === 'changed'" class="text-[12px] leading-5 text-warning">The provider changed the details for this action. Review them before allowing it again.</p>
              <p v-else-if="projectIntegrationConsentState(entry, action) === 'revoked'" class="text-[12px] leading-5 text-text-muted">This action was revoked earlier. Review and allow it again to restore access.</p>
              <p v-else-if="projectIntegrationConsentState(entry, action) === 'unavailable'" class="text-[12px] leading-5 text-text-muted">The provider has incomplete or deprecated details for this action, so it cannot be allowed right now.</p>

              <section
                v-if="isConsentReviewOpen(entry, action)"
                :id="consentReviewID(entry, action)"
                class="grid min-w-0 gap-3 border-y border-warning/30 bg-warning-subtle/30 px-3 py-4"
                role="group"
                :aria-labelledby="`${consentReviewID(entry, action)}-title`"
              >
                <div class="flex items-start gap-2">
                  <ShieldCheck class="mt-0.5 h-4 w-4 shrink-0 text-warning" :stroke-width="1.75" aria-hidden="true" />
                  <div class="min-w-0">
                    <h5 :id="`${consentReviewID(entry, action)}-title`" class="text-[13px] font-semibold text-text-primary">Allow this action for this project?</h5>
                    <p class="mt-1 min-w-0 break-words text-[12px] leading-5 text-text-secondary [overflow-wrap:anywhere]">{{ action.consent?.prompt || `Approve the action shown below for ${entry.alias}.` }}</p>
                    <p v-if="action.consent?.scope" class="mt-1 min-w-0 break-words text-[12px] leading-5 text-text-muted [overflow-wrap:anywhere]">Provider scope: {{ action.consent.scope }}</p>
                  </div>
                </div>
                <dl class="grid min-w-0 gap-2 text-[12px] sm:grid-cols-2">
                  <div class="min-w-0">
                    <dt class="font-semibold text-text-muted">Project</dt>
                    <dd class="mt-0.5 break-words text-text-primary">{{ projectName }} · {{ entry.environment }}</dd>
                  </div>
                  <div class="min-w-0">
                    <dt class="font-semibold text-text-muted">Resource</dt>
                    <dd class="mt-0.5 min-w-0 break-words text-text-primary [overflow-wrap:anywhere]">{{ entry.resourceRef?.name || entry.alias }} · {{ providerDisplayName(entry.provider) }} · {{ entry.resourceRef?.kind || entry.kind }}</dd>
                  </div>
                  <div class="min-w-0">
                    <dt class="font-semibold text-text-muted">Action</dt>
                    <dd class="mt-0.5 min-w-0 break-words text-text-primary [overflow-wrap:anywhere]">{{ actionDisplayName(entry, action) }} · {{ action.readOnly ? 'Read-only' : 'Can make changes' }}</dd>
                  </div>
                </dl>
                <details class="integration-disclosure border-t border-border-subtle">
                  <summary class="integration-disclosure__trigger text-[13px] font-medium text-text-primary">
                    Technical details
                    <ChevronRight class="integration-disclosure__chevron" :stroke-width="1.75" aria-hidden="true" />
                  </summary>
                  <dl class="grid gap-2 pb-2 text-[12px] text-text-secondary [overflow-wrap:anywhere]">
                    <div><dt>Resource coordinate</dt><dd class="break-all font-mono">{{ entry.resourceRef?.apiVersion }}/{{ entry.resourceRef?.kind }}/{{ entry.resourceRef?.resource }}/{{ entry.resourceRef?.name || 'unknown' }}</dd></div>
                    <div><dt>Action ID</dt><dd class="break-all font-mono">{{ action.name }}/{{ action.version }} · Risk {{ action.risk || 'unspecified' }}</dd></div>
                    <div><dt>Schema digest</dt><dd class="break-all font-mono">{{ action.schemaDigest }}</dd></div>
                  </dl>
                </details>
                <p class="min-w-0 break-words text-[12px] leading-5 text-text-secondary [overflow-wrap:anywhere]">This approval applies only to this action on this resource in this project. Other resources and actions are not included.</p>
                <label :for="consentCheckboxID(entry, action)" class="k-checkbox-hit flex min-w-0 items-start gap-2 py-2 text-[12px] leading-5 text-text-secondary">
                  <input
                    :id="consentCheckboxID(entry, action)"
                    type="checkbox"
                    class="k-checkbox mt-1"
                    :checked="isConsentAccepted(entry, action)"
                    :disabled="busy"
                    @change="setConsentAccepted(entry, action, $event)"
                  />
                  <span class="min-w-0 break-words [overflow-wrap:anywhere]"><span class="font-medium text-text-primary">I allow this project to use this action with {{ entry.alias }}.</span></span>
                </label>
                <InlineNotification
                  v-if="consentError?.key === consentActionKey(entry, action)"
                  tone="error"
                  :message="consentError.message"
                />
                <div class="flex flex-wrap items-center justify-end gap-2 border-t border-border-subtle pt-3">
                  <button type="button" class="k-btn k-btn--ghost app-studio-touch-target h-9" :disabled="busy" @click="closeConsentReview">Cancel</button>
                  <button
                    type="button"
                    class="k-btn k-btn--primary app-studio-touch-target h-9"
                    :disabled="busy || !isConsentAccepted(entry, action) || !canConnectConsentAction(entry, action)"
                    @click="connectConsentAction(entry, action)"
                  >
                    <Loader2 v-if="busy" class="h-3.5 w-3.5 motion-safe:animate-spin" :stroke-width="1.75" aria-hidden="true" />
                    <KeyRound v-else class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
                    {{ busy ? 'Allowing…' : 'Allow action' }}
                  </button>
                </div>
              </section>
            </div>
          </div>
          <p v-else-if="entry.state === 'available' && !entry.catalogActions.length" class="text-[12px] text-text-muted">No actions are listed for this resource right now.</p>
          <p v-else-if="entry.state === 'connected' && !entry.catalogActions.length" class="text-[12px] leading-5 text-text-muted">Action details are unavailable. Refresh to check for actions that need approval.</p>
          <details class="integration-disclosure border-t border-border-subtle text-[12px] text-text-secondary">
            <summary class="integration-disclosure__trigger text-[13px] font-medium text-text-primary">
              Resource details and access history
              <ChevronRight class="integration-disclosure__chevron" :stroke-width="1.75" aria-hidden="true" />
            </summary>
            <dl class="grid gap-2 [overflow-wrap:anywhere]">
              <div><dt class="font-semibold">Project alias</dt><dd>{{ entry.alias }}</dd></div>
              <div><dt class="font-semibold">Resource</dt><dd>{{ entry.resourceRef?.apiVersion }}/{{ entry.resourceRef?.kind }}/{{ entry.resourceRef?.resource }}/{{ entry.resourceRef?.name || 'unknown' }}</dd></div>
              <div v-if="entry.phase"><dt class="font-semibold">Status</dt><dd>{{ entry.phase }}</dd></div>
            </dl>
            <div v-for="grant in entry.allowedActions" :key="`${grant.name}/${grant.version}`" class="mt-3 grid gap-1 text-[11px] [overflow-wrap:anywhere]">
              <p class="font-medium text-text-secondary">{{ grant.name }}/{{ grant.version }}</p>
              <code class="break-all">{{ grant.schemaDigest }}</code>
              <p>Granted by {{ grant.grantedBy || 'server' }} · {{ formatTimestamp(grant.grantedAt) }}</p>
              <p v-if="grant.revoked">Revoked by {{ grant.revokedBy || 'server' }} · {{ formatTimestamp(grant.revokedAt) }}</p>
            </div>
            <div v-for="action in entry.catalogActions" :key="action.id" class="mt-3 grid gap-1 text-[11px] [overflow-wrap:anywhere]">
              <p>{{ action.id }} · Risk {{ action.risk || 'unspecified' }} · {{ action.consent?.required ? 'Approval required' : 'No approval required' }}</p>
              <code class="break-all">{{ action.schemaDigest }}</code>
            </div>
          </details>
          </div>
        </details>
          </div>
        </AIWorkbenchSection>
      </div>
    </section>
  </AIWorkbenchPane>
</template>

<style scoped>
.integration-disclosure__trigger {
  display: flex;
  min-width: 0;
  min-height: 44px;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  cursor: pointer;
  list-style: none;
}
.integration-disclosure__trigger::-webkit-details-marker { display: none; }
.integration-disclosure__trigger:hover { background: var(--color-surface-hover); }
.integration-disclosure__trigger:focus-visible { outline: 2px solid var(--color-accent); outline-offset: -2px; }
.integration-disclosure__chevron { flex: none; width: 16px; height: 16px; color: var(--color-text-muted); }
.integration-disclosure__chevron:dir(rtl) { transform: rotate(180deg); }
.integration-disclosure[open] > summary > .integration-disclosure__chevron { transform: rotate(90deg); }
</style>
