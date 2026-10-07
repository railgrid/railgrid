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
</script>

<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import {
  ChevronDown, KeyRound,
  Link2,
  Loader2,
  RefreshCw,
  ShieldCheck,
  Undo2,
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
const consentReviewKey = ref('')
const acceptedConsentActionKey = ref('')
const consentError = ref<{ key: string; message: string } | null>(null)
let integrationsRequestSerial = 0
let integrationMutationSerial = 0

const integrationCards = computed(() => projectIntegrationCardEntries(integrations.value, availableIntegrations.value))
const hasIntegrationCards = computed(() => integrationCards.value.length > 0)
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
  hasIntegrationCards.value
    ? `Could not refresh provider access. Showing the last confirmed results. ${projectIntegrationErrorMessage(error.value, 'The request returned no error details.')}`
    : `Could not load provider access. ${projectIntegrationErrorMessage(error.value, 'The request returned no error details.')}`
))

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

async function revokeAction(integration: ProjectIntegration, actionName: string, actionVersion: string) {
  const requestAuthority = integrationsAuthority.value
  const interactionSerial = integrationMutationSerial
  const context = props.ctx
  const projectName = props.projectName
  const actionLabel = `${actionName}/${actionVersion}`
  const confirmed = await confirmDialog({
    title: `Revoke ${actionLabel}?`,
    message: `New invocations of ${actionLabel} will be denied for ${integration.alias}. The grant audit remains visible.`,
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
  <div class="flex min-h-full flex-col gap-4 p-4" :aria-busy="loading || busy">
    <header class="flex flex-wrap items-start justify-between gap-3">
      <div class="flex min-w-0 items-start gap-3">
        <div class="flex h-9 w-9 shrink-0 items-center justify-center rounded-xl border border-accent/25 bg-accent-subtle text-accent">
          <Link2 class="h-4 w-4" :stroke-width="1.75" aria-hidden="true" />
        </div>
        <div class="min-w-0">
          <h2 class="text-[16px] font-semibold text-text-primary">Integrations</h2>
          <p class="mt-1 max-w-2xl text-[12px] leading-5 text-text-muted">
            Connect provider resources to this project and manage action access.
          </p>
        </div>
      </div>
      <button
        type="button"
        class="app-studio-touch-target inline-flex h-8 items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="loading || busy || !projectName"
        title="Refresh integrations"
        @click="refreshIntegrations"
      >
        <Loader2 v-if="loading" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
        <RefreshCw v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
        Refresh
      </button>
    </header>

    <InlineNotification
      v-if="error"
      tone="error"
      title="Could not refresh provider access"
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

    <p class="text-[12px] leading-5 text-text-muted">
      Available resources are discovered, not connected. Actions that need approval stay blocked until you approve them.
    </p>

    <section class="min-w-0" aria-label="Provider resources">
      <p class="mb-2 text-[11px] text-text-muted">{{ integrationCards.length }} {{ integrationCards.length === 1 ? 'resource' : 'resources' }}</p>

      <div v-if="loading && !hasIntegrationCards" class="grid gap-2" role="status" aria-live="polite">
        <div v-for="i in 3" :key="i" class="shimmer h-16 rounded-xl border border-border-subtle bg-surface" />
      </div>
      <div v-else-if="!loading && integrationsLoaded && !error && discovery?.state === 'available' && !hasIntegrationCards" class="flex min-h-28 items-center justify-center rounded-xl border border-dashed border-border-subtle bg-surface p-4 text-center text-[12px] text-text-muted">
        No accessible provider resources are available to this project.
      </div>
      <div v-else-if="!loading && !hasIntegrationCards" class="flex min-h-20 items-center justify-center rounded-xl border border-dashed border-border-subtle bg-surface p-4 text-center text-[12px] text-text-muted" role="status">
        No provider resources could be confirmed in this response. Retry discovery to check again.
      </div>
      <div v-else class="min-w-0 divide-y divide-border-subtle border-y border-border-subtle">
        <details v-for="entry in integrationCards" :key="entry.key" class="integration-resource min-w-0">
          <summary class="flex min-w-0 cursor-pointer list-none items-center gap-3 py-3 text-text-primary hover:bg-surface-hover focus-visible:outline-2 focus-visible:outline-accent focus-visible:outline-offset-2">
            <div class="min-w-0 flex-1">
              <h3 class="break-words text-[13px] font-semibold [overflow-wrap:anywhere]">{{ entry.resourceRef?.name || entry.alias }}</h3>
              <p class="mt-0.5 break-words text-[11px] text-text-muted [overflow-wrap:anywhere]">{{ [providerDisplayName(entry.provider), entry.resourceRef?.kind || entry.kind, entry.state === 'connected' ? entry.environment : null].filter(Boolean).join(' · ') }}</p>
            </div>
            <span class="shrink-0 text-[11px]" :class="entry.state === 'connected' ? 'text-success' : 'text-text-muted'">{{ entry.state === 'connected' ? 'Connected' : 'Available' }}</span>
            <ChevronDown class="integration-resource-chevron h-4 w-4 shrink-0 text-text-muted" aria-hidden="true" />
          </summary>
          <div class="grid min-w-0 gap-3 pb-4">
          <StatusBadge v-if="entry.state === 'connected' && entry.phase" class="justify-self-start" :status="entry.phase" />
          <div v-if="entry.state === 'connected'" class="grid min-w-0 gap-2">

            <div v-for="grant in entry.allowedActions" :key="`${grant.name}/${grant.version}`" class="grid min-w-0 gap-2 border-t border-border-subtle py-3 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center">
              <div class="min-w-0">
                <div class="flex min-w-0 flex-wrap items-center gap-2">
                  <span class="min-w-0 break-words text-[12px] font-medium text-text-primary [overflow-wrap:anywhere]">{{ grant.name }}/{{ grant.version }}</span>
                  <StatusBadge :status="grant.revoked ? 'Revoked' : 'Granted'" :tone="grant.revoked ? 'danger' : 'success'" />
                </div>
                <details class="mt-1 text-[11px] text-text-muted">
                  <summary class="cursor-pointer py-1">Grant details</summary>
                <code class="mt-1 block min-w-0 break-all text-[10px] leading-4 text-text-muted [overflow-wrap:anywhere]">{{ grant.schemaDigest }}</code>
                <div class="mt-1 min-w-0 break-words text-[10px] text-text-muted [overflow-wrap:anywhere]">
                  Granted by {{ grant.grantedBy || 'server' }} · {{ formatTimestamp(grant.grantedAt) }}
                </div>
                <div v-if="grant.revoked" class="mt-1 min-w-0 break-words text-[10px] text-danger [overflow-wrap:anywhere]">
                  Revoked by {{ grant.revokedBy || 'server' }} · {{ formatTimestamp(grant.revokedAt) }}
                </div>
                </details>
              </div>
              <button
                v-if="entry.integration && !grant.revoked"
                type="button"
                class="k-btn k-btn--ghost app-studio-touch-target h-8 text-warning"
                :disabled="busy"
                @click="revokeAction(entry.integration, grant.name, grant.version)"
              >
                <Undo2 class="h-3.5 w-3.5" :stroke-width="1.75" />
                Revoke
              </button>

            </div>
          </div>
          <div v-if="catalogActionsForCard(entry).length" class="grid min-w-0 gap-2">
            <p v-if="entry.state === 'connected'" class="min-w-0 break-words text-[11px] leading-4 text-text-muted [overflow-wrap:anywhere]">These catalog actions require separate approval and are not active on this binding yet.</p>
            <div v-for="action in catalogActionsForCard(entry)" :key="`${action.name}/${action.version}`" class="grid min-w-0 gap-2 border-t border-border-subtle py-3">
              <div class="flex flex-wrap items-center justify-between gap-2">
                <div class="flex min-w-0 flex-wrap items-center gap-2">
                  <span class="min-w-0 break-words text-[12px] font-medium text-text-primary [overflow-wrap:anywhere]">{{ action.displayName || action.id }}</span>

                  <span class="k-badge k-badge--muted">{{ action.readOnly ? 'Read-only' : 'May mutate' }}</span>
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
                  {{ isConsentReviewOpen(entry, action) ? 'Close review' : 'Review consent' }}
                </button>
              </div>
              <p v-if="action.description" class="min-w-0 break-words text-[11px] leading-4 text-text-secondary [overflow-wrap:anywhere]">{{ action.description }}</p>
              <details class="text-[11px] text-text-muted">
                <summary class="cursor-pointer py-1">Action details</summary>
                <p class="break-words [overflow-wrap:anywhere]">{{ action.name }}/{{ action.version }} · Risk {{ action.risk || 'unspecified' }} · {{ action.consent?.required ? 'Approval required' : 'No approval required' }}</p>
                <code class="block break-all text-[10px] leading-4">Schema {{ action.schemaDigest }}</code>
              </details>
              <p v-if="projectIntegrationConsentState(entry, action) === 'changed'" class="text-[11px] leading-4 text-warning">The catalog digest changed since this action was granted. Review the current contract before reactivating it.</p>
              <p v-else-if="projectIntegrationConsentState(entry, action) === 'revoked'" class="text-[11px] leading-4 text-text-muted">A previous grant was revoked. A new approval will reactivate access for this action.</p>
              <p v-else-if="projectIntegrationConsentState(entry, action) === 'unavailable'" class="text-[11px] leading-4 text-text-muted">Current catalog metadata is incomplete or deprecated, so this action cannot receive a new grant.</p>

              <section
                v-if="isConsentReviewOpen(entry, action)"
                :id="consentReviewID(entry, action)"
                class="grid min-w-0 gap-3 rounded-lg border border-warning/30 bg-warning-subtle/60 p-3"
                role="group"
                :aria-labelledby="`${consentReviewID(entry, action)}-title`"
              >
                <div class="flex items-start gap-2">
                  <ShieldCheck class="mt-0.5 h-4 w-4 shrink-0 text-warning" :stroke-width="1.75" aria-hidden="true" />
                  <div class="min-w-0">
                    <h5 :id="`${consentReviewID(entry, action)}-title`" class="text-[12px] font-semibold text-text-primary">Review this action before connecting</h5>
                    <p class="mt-1 min-w-0 break-words text-[11px] leading-4 text-text-secondary [overflow-wrap:anywhere]">{{ action.consent?.prompt || 'Approve this provider action for the selected resource.' }}</p>
                    <p v-if="action.consent?.scope" class="mt-1 min-w-0 break-words text-[11px] leading-4 text-text-muted [overflow-wrap:anywhere]">Provider scope: {{ action.consent.scope }}</p>
                  </div>
                </div>
                <dl class="grid min-w-0 gap-2 text-[11px] sm:grid-cols-2">
                  <div class="min-w-0">
                    <dt class="font-semibold text-text-muted">Project and environment</dt>
                    <dd class="mt-0.5 break-words text-text-primary">{{ projectName }} · {{ entry.environment }}</dd>
                  </div>
                  <div class="min-w-0">
                    <dt class="font-semibold text-text-muted">Provider resource</dt>
                    <dd class="mt-0.5 min-w-0 break-all font-mono text-text-primary [overflow-wrap:anywhere]">{{ entry.resourceRef?.apiVersion }}/{{ entry.resourceRef?.kind }}/{{ entry.resourceRef?.resource }}/{{ entry.resourceRef?.name || 'unknown' }}</dd>
                  </div>
                  <div class="min-w-0">
                    <dt class="font-semibold text-text-muted">Action and policy</dt>
                    <dd class="mt-0.5 min-w-0 break-words text-text-primary [overflow-wrap:anywhere]">{{ action.name }}/{{ action.version }} · {{ action.readOnly ? 'Read-only' : 'May mutate' }} · Risk {{ action.risk || 'unspecified' }}</dd>
                  </div>
                  <div class="min-w-0">
                    <dt class="font-semibold text-text-muted">Schema digest</dt>
                    <dd class="mt-0.5 min-w-0 break-all font-mono text-text-primary [overflow-wrap:anywhere]">{{ action.schemaDigest }}</dd>
                  </div>
                </dl>
                <p class="min-w-0 break-words text-[11px] leading-4 text-text-secondary [overflow-wrap:anywhere]">This approval covers only this versioned action on this exact resource in this project. Other resources and actions remain unchanged.</p>
                <label :for="consentCheckboxID(entry, action)" class="k-checkbox-hit flex min-w-0 items-start gap-2 rounded-lg border border-warning/30 bg-surface px-2.5 py-2 text-[12px] leading-5 text-text-secondary">
                  <input
                    :id="consentCheckboxID(entry, action)"
                    type="checkbox"
                    class="mt-1 h-3.5 w-3.5 shrink-0 accent-accent"
                    :checked="isConsentAccepted(entry, action)"
                    :disabled="busy"
                    @change="setConsentAccepted(entry, action, $event)"
                  />
                  <span class="min-w-0 break-words [overflow-wrap:anywhere]"><span class="font-medium text-text-primary">I approve {{ action.name }}/{{ action.version }} for this exact project resource.</span></span>
                </label>
                <InlineNotification
                  v-if="consentError?.key === consentActionKey(entry, action)"
                  tone="error"
                  :message="consentError.message"
                />
                <div class="flex flex-wrap items-center justify-end gap-2 border-t border-warning/20 pt-3">
                  <button type="button" class="k-btn k-btn--ghost app-studio-touch-target h-9" :disabled="busy" @click="closeConsentReview">Cancel</button>
                  <button
                    type="button"
                    class="k-btn k-btn--primary app-studio-touch-target h-9"
                    :disabled="busy || !isConsentAccepted(entry, action) || !canConnectConsentAction(entry, action)"
                    @click="connectConsentAction(entry, action)"
                  >
                    <Loader2 v-if="busy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" aria-hidden="true" />
                    <KeyRound v-else class="h-3.5 w-3.5" :stroke-width="1.75" aria-hidden="true" />
                    {{ busy ? 'Connecting…' : 'Connect action' }}
                  </button>
                </div>
              </section>
            </div>
          </div>
          <p v-else-if="entry.state === 'available' && !entry.catalogActions.length" class="text-[11px] text-text-muted">No current catalog actions were included for this resource.</p>
          <p v-else-if="entry.state === 'connected' && entry.catalogActions.length" class="text-[11px] leading-4 text-text-muted">No additional consent-required actions are waiting for review.</p>
          <p v-else-if="entry.state === 'connected'" class="text-[11px] leading-4 text-text-muted">Current catalog metadata is unavailable for this resource. Refresh discovery to check for actions that need consent.</p>
          <details class="k-resource-technical">
            <summary class="k-resource-technical__summary">Resource details<ChevronDown class="k-resource-technical__chevron h-3.5 w-3.5" aria-hidden="true" /></summary>
            <dl class="k-resource-technical__body text-[11px] text-text-secondary [overflow-wrap:anywhere]">
              <div><dt class="font-semibold">{{ entry.state === 'connected' ? 'Project alias' : 'Suggested alias' }}</dt><dd>{{ entry.alias }}</dd></div>
              <div><dt class="font-semibold">Resource</dt><dd>{{ entry.resourceRef?.apiVersion }}/{{ entry.resourceRef?.kind }}/{{ entry.resourceRef?.resource }}/{{ entry.resourceRef?.name || 'unknown' }}</dd></div>
            </dl>
          </details>
          </div>
        </details>
      </div>
    </section>
  </div>
</template>

<style scoped>
.integration-resource > summary::-webkit-details-marker { display: none; }
.integration-resource[open] > summary .integration-resource-chevron { transform: rotate(180deg); }
</style>
