<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import {
  AlertTriangle,
  Check,
  Clipboard,
  KeyRound,
  Link2,
  Loader2,
  Plus,
  RefreshCw,
  ShieldCheck,
  Trash2,
  Undo2,
} from 'lucide-vue-next'
import { api } from './api'
import { confirmDialog } from './portalkit/confirm'
import StatusBadge from './portalkit/StatusBadge.vue'
import {
  buildProjectIntegrationCreatePayload,
  buildProjectIntegrationRevokePayload,
  readyProviderActions,
} from './projectIntegrations'
import type {
  FarosContext,
  ProjectIntegration,
  ProviderAction,
  ProviderItem,
} from './types'

const props = withDefaults(defineProps<{
  ctx: FarosContext | null
  projectName: string
  providers: ProviderItem[]
  providersLoading?: boolean
}>(), {
  providersLoading: false,
})

/**
 * Temporary compatibility mode: assistant turns materialize provider actions
 * automatically, so this portal keeps the legacy grant controls available in
 * source but does not expose them to users.
 *
 * TODO: remove this guard when explicit consent and least-privilege grants are
 * restored.
 */
const automaticProviderAccessOnly = true

const integrations = ref<ProjectIntegration[]>([])
const loading = ref(false)
const busy = ref(false)
const error = ref<string | null>(null)
const formError = ref<string | null>(null)
const notice = ref<string | null>(null)
const providerName = ref('')
const actionID = ref('')
const alias = ref('')
const resourceName = ref('')
const consentAccepted = ref(false)

const selectableActions = computed(() => readyProviderActions(props.providers))
const selectableProviders = computed(() => {
  const seen = new Set<string>()
  return selectableActions.value
    .map(({ provider }) => provider)
    .filter((provider) => {
      if (seen.has(provider.name)) return false
      seen.add(provider.name)
      return true
    })
})
const selectedProvider = computed(() => selectableProviders.value.find((provider) => provider.name === providerName.value))
const selectedAction = computed<ProviderAction | undefined>(() =>
  selectedProvider.value?.actions?.find((action) => action.id === actionID.value && !action.deprecation?.deprecated),
)
const selectedActionResource = computed(() => selectedAction.value?.boundResource)
const selectedActionRequiresConsent = computed(() => selectedAction.value?.consent?.required === true)
const canCreate = computed(() => {
  const payload = selectedProvider.value && selectedAction.value
    ? buildProjectIntegrationCreatePayload(
      selectedProvider.value,
      selectedAction.value,
      alias.value,
      resourceName.value,
      consentAccepted.value,
    )
    : null
  return !!props.projectName && !!payload && (!selectedActionRequiresConsent.value || consentAccepted.value) && !busy.value
})

watch(
  () => props.projectName,
  (projectName) => {
    integrations.value = []
    error.value = null
    notice.value = null
    resetForm()
    if (projectName) void loadIntegrations()
  },
  { immediate: true },
)

watch(
  selectableProviders,
  (providers) => {
    if (!providers.some((provider) => provider.name === providerName.value)) {
      providerName.value = providers[0]?.name ?? ''
    }
  },
  { immediate: true },
)

watch(
  selectedProvider,
  (provider) => {
    const actions = provider?.actions?.filter((action) => !action.deprecation?.deprecated && action.schemaDigest) ?? []
    if (!actions.some((action) => action.id === actionID.value)) actionID.value = actions[0]?.id ?? ''
    consentAccepted.value = false
  },
  { immediate: true },
)

watch(actionID, () => {
  consentAccepted.value = false
  formError.value = null
})

const selectedProviderActions = computed(() => selectedProvider.value?.actions?.filter((action) =>
  !action.deprecation?.deprecated && /^sha256:[a-f0-9]{64}$/.test(action.schemaDigest),
) ?? [])

async function loadIntegrations() {
  if (!props.projectName) return
  loading.value = true
  error.value = null
  try {
    integrations.value = await api.listProjectIntegrations(props.ctx, props.projectName)
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

function resetForm() {
  alias.value = ''
  resourceName.value = ''
  consentAccepted.value = false
  formError.value = null
}

function clearNotice() {
  notice.value = null
}

async function createIntegration() {
  clearNotice()
  formError.value = null
  const provider = selectedProvider.value
  const action = selectedAction.value
  if (!provider || !action) {
    formError.value = 'Select a Ready provider and versioned action.'
    return
  }
  const payload = buildProjectIntegrationCreatePayload(
    provider,
    action,
    alias.value,
    resourceName.value,
    consentAccepted.value,
  )
  if (!payload) {
    formError.value = 'Enter an integration alias and the exact provider resource name.'
    return
  }
  if (selectedActionRequiresConsent.value && !consentAccepted.value) {
    formError.value = 'Review and accept the provider action consent before creating this grant.'
    return
  }
  busy.value = true
  try {
    await api.createProjectIntegration(props.ctx, props.projectName, payload)
    resetForm()
    notice.value = `Integration ${payload.alias} created.`
    await loadIntegrations()
  } catch (err) {
    formError.value = err instanceof Error ? err.message : String(err)
  } finally {
    busy.value = false
  }
}

async function revokeAction(integration: ProjectIntegration, actionName: string, actionVersion: string) {
  const actionLabel = `${actionName}/${actionVersion}`
  const confirmed = await confirmDialog({
    title: `Revoke ${actionLabel}?`,
    message: `New invocations of ${actionLabel} will be denied for ${integration.alias}. The grant audit remains visible.`,
    confirmLabel: 'Revoke action',
    danger: true,
  })
  if (!confirmed) return
  const payload = buildProjectIntegrationRevokePayload(integration, actionName, actionVersion)
  if (!payload) return
  clearNotice()
  formError.value = null
  busy.value = true
  try {
    await api.patchProjectIntegration(props.ctx, props.projectName, integration.alias, payload)
    notice.value = `${actionLabel} revoked.`
    await loadIntegrations()
  } catch (err) {
    formError.value = err instanceof Error ? err.message : String(err)
  } finally {
    busy.value = false
  }
}

async function removeIntegration(integration: ProjectIntegration) {
  const confirmed = await confirmDialog({
    title: `Remove ${integration.alias}?`,
    message: `This removes the integration binding and all of its action grants. The provider resource itself is not changed.`,
    confirmLabel: 'Remove integration',
    danger: true,
  })
  if (!confirmed) return
  clearNotice()
  formError.value = null
  busy.value = true
  try {
    await api.removeProjectIntegration(props.ctx, props.projectName, integration.alias)
    notice.value = `Integration ${integration.alias} removed.`
    await loadIntegrations()
  } catch (err) {
    formError.value = err instanceof Error ? err.message : String(err)
  } finally {
    busy.value = false
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
            Ready providers are connected automatically to every accessible matching resource. Provider credentials and URLs stay server-side.
          </p>
        </div>
      </div>
      <button
        type="button"
        class="inline-flex h-8 items-center gap-1.5 rounded-md border border-border-subtle bg-surface px-3 text-[12px] font-medium text-text-secondary transition hover:bg-surface-hover hover:text-text-primary disabled:cursor-not-allowed disabled:opacity-60"
        :disabled="loading || busy || !projectName"
        title="Refresh integrations"
        @click="loadIntegrations"
      >
        <Loader2 v-if="loading" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
        <RefreshCw v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
        Refresh
      </button>
    </header>

    <div v-if="error" class="flex items-start gap-2 rounded-xl border border-danger/30 bg-danger-subtle px-3 py-2.5 text-[12px] leading-5 text-danger" role="alert" aria-live="assertive">
      <AlertTriangle class="mt-0.5 h-3.5 w-3.5 shrink-0" :stroke-width="2" aria-hidden="true" />
      <span>{{ error }}</span>
    </div>
    <div v-if="formError" class="flex items-start gap-2 rounded-xl border border-danger/30 bg-danger-subtle px-3 py-2.5 text-[12px] leading-5 text-danger" role="alert" aria-live="assertive">
      <AlertTriangle class="mt-0.5 h-3.5 w-3.5 shrink-0" :stroke-width="2" aria-hidden="true" />
      <span>{{ formError }}</span>
    </div>
    <div v-if="notice" class="flex items-start gap-2 rounded-xl border border-success/30 bg-success-subtle px-3 py-2.5 text-[12px] leading-5 text-success" role="status" aria-live="polite">
      <Check class="mt-0.5 h-3.5 w-3.5 shrink-0" :stroke-width="2" aria-hidden="true" />
      <span>{{ notice }}</span>
    </div>

    <section class="k-card grid gap-3 border-accent/20 bg-accent-subtle/40 p-4" aria-labelledby="automatic-integrations-title">
      <div class="flex items-start gap-2">
        <ShieldCheck class="mt-0.5 h-4 w-4 shrink-0 text-accent" :stroke-width="1.75" aria-hidden="true" />
        <div class="min-w-0">
          <h3 id="automatic-integrations-title" class="text-[13px] font-semibold text-text-primary">Automatic provider access</h3>
          <p class="mt-0.5 text-[11px] leading-4 text-text-secondary">
            Before each assistant turn, current non-deprecated actions from Ready provider catalogs are materialized for resources you can access. These bindings are read-only here and never mutate the provider resource.
          </p>
          <p class="mt-1 text-[11px] leading-4 text-text-muted">
            Temporary compatibility mode accepts consent-required actions automatically; audit and revocation state remain visible below.
          </p>
        </div>
      </div>
    </section>

    <section v-if="!automaticProviderAccessOnly" class="k-card grid gap-3 p-4">
      <div class="flex items-start gap-2">
        <Plus class="mt-0.5 h-4 w-4 shrink-0 text-accent" :stroke-width="1.75" aria-hidden="true" />
        <div>
          <h3 class="text-[13px] font-semibold text-text-primary">Add an action grant</h3>
          <p class="mt-0.5 text-[11px] leading-4 text-text-muted">Only Ready providers and current, non-deprecated catalog actions are selectable.</p>
        </div>
      </div>

      <div v-if="providersLoading" class="flex items-center gap-2 rounded-xl border border-border-subtle bg-surface p-3 text-[12px] text-text-muted" role="status" aria-live="polite">
        <Loader2 class="h-4 w-4 animate-spin text-accent" :stroke-width="1.75" />
        Loading provider action catalog...
      </div>
      <div v-else-if="selectableProviders.length === 0" class="flex items-start gap-2 rounded-xl border border-dashed border-border-subtle bg-surface p-3 text-[12px] leading-5 text-text-muted" role="status">
        <ShieldCheck class="mt-0.5 h-4 w-4 shrink-0 text-text-muted" :stroke-width="1.75" aria-hidden="true" />
        No Ready provider actions are available in this workspace yet.
      </div>
      <form v-else class="grid gap-4" @submit.prevent="createIntegration">
        <div class="grid gap-3 md:grid-cols-2">
          <label class="grid gap-1.5" for="integration-provider">
            <span class="text-[10px] font-semibold uppercase tracking-wide text-text-muted">Provider</span>
            <select
              id="integration-provider"
              v-model="providerName"
              class="k-input h-9"
              :disabled="busy"
            >
              <option value="" disabled>Select a Ready provider</option>
              <option v-for="provider in selectableProviders" :key="provider.name" :value="provider.name">
                {{ provider.displayName || provider.name }}
              </option>
            </select>
          </label>
          <label class="grid gap-1.5" for="integration-action">
            <span class="text-[10px] font-semibold uppercase tracking-wide text-text-muted">Versioned action</span>
            <select
              id="integration-action"
              v-model="actionID"
              class="k-input h-9 font-mono"
              :disabled="busy || !selectedProvider"
            >
              <option value="" disabled>Select an action</option>
              <option v-for="action in selectedProviderActions" :key="action.id" :value="action.id">
                {{ action.displayName || action.id }} · {{ action.id }}
              </option>
            </select>
          </label>
          <label class="grid gap-1.5" for="integration-alias">
            <span class="text-[10px] font-semibold uppercase tracking-wide text-text-muted">Integration alias</span>
            <input
              id="integration-alias"
              v-model="alias"
              class="k-input h-9"
              placeholder="sales"
              autocomplete="off"
              :disabled="busy"
            />
          </label>
          <label class="grid gap-1.5" for="integration-resource-name">
            <span class="text-[10px] font-semibold uppercase tracking-wide text-text-muted">Exact resource name</span>
            <input
              id="integration-resource-name"
              v-model="resourceName"
              class="k-input h-9 font-mono"
              placeholder="orders"
              autocomplete="off"
              :disabled="busy"
            />
          </label>
        </div>

        <section v-if="selectedAction" class="grid gap-3 rounded-xl border border-accent/20 bg-accent-subtle/40 p-3" aria-labelledby="integration-review-title">
          <div class="flex items-start gap-2">
            <KeyRound class="mt-0.5 h-4 w-4 shrink-0 text-accent" :stroke-width="1.75" aria-hidden="true" />
            <div class="min-w-0">
              <h4 id="integration-review-title" class="text-[12px] font-semibold text-text-primary">Review action contract</h4>
              <p class="mt-0.5 text-[11px] leading-4 text-text-secondary">The digest below is copied from the Ready provider catalog and cannot be edited here.</p>
            </div>
          </div>
          <dl class="grid gap-2 text-[12px] sm:grid-cols-2">
            <div>
              <dt class="text-[10px] font-semibold uppercase tracking-wide text-text-muted">Bound resource</dt>
              <dd class="mt-0.5 font-mono text-text-primary">
                {{ selectedActionResource?.apiVersion }}/{{ selectedActionResource?.kind }}/{{ selectedActionResource?.resource }}
              </dd>
            </div>
            <div>
              <dt class="text-[10px] font-semibold uppercase tracking-wide text-text-muted">Action policy</dt>
              <dd class="mt-0.5 flex flex-wrap gap-1.5">
                <span class="k-badge k-badge--muted">
                  {{ selectedAction.readOnly ? 'Read-only' : 'May mutate' }}
                </span>
                <span class="k-badge k-badge--muted">Risk: {{ selectedAction.risk || 'unspecified' }}</span>
                <span class="k-badge k-badge--muted">Consent: {{ selectedActionRequiresConsent ? 'Required' : 'Not required' }}</span>
              </dd>
            </div>
            <div class="sm:col-span-2">
              <dt class="text-[10px] font-semibold uppercase tracking-wide text-text-muted">Schema digest</dt>
              <dd class="mt-0.5 flex items-start gap-2 rounded-lg border border-border-subtle bg-surface px-2.5 py-2 font-mono text-[11px] leading-4 text-text-primary break-all">
                <span class="min-w-0 flex-1">{{ selectedAction.schemaDigest }}</span>
                <Clipboard class="mt-0.5 h-3.5 w-3.5 shrink-0 text-text-muted" :stroke-width="1.75" aria-label="Immutable schema digest" />
              </dd>
            </div>
          </dl>
          <label v-if="selectedActionRequiresConsent" class="flex items-start gap-2 rounded-lg border border-warning/30 bg-warning-subtle px-2.5 py-2 text-[12px] leading-5 text-text-secondary">
            <input v-model="consentAccepted" type="checkbox" class="mt-1 h-3.5 w-3.5 accent-accent" :disabled="busy" />
            <span>
              <span class="font-medium text-text-primary">{{ selectedAction.consent?.prompt || 'I approve this provider action for the selected resource.' }}</span>
              <span v-if="selectedAction.consent?.scope" class="block text-[11px] text-text-muted">Scope: {{ selectedAction.consent.scope }}</span>
            </span>
          </label>
        </section>

        <div class="flex flex-wrap items-center justify-between gap-2 border-t border-border-subtle pt-3">
          <p class="text-[11px] leading-4 text-text-muted">No credentials, provider URLs, or backend coordinates are accepted by this form.</p>
          <button
            type="submit"
            class="k-btn k-btn--primary h-9"
            :disabled="!canCreate"
          >
            <Loader2 v-if="busy" class="h-3.5 w-3.5 animate-spin" :stroke-width="1.75" />
            <Plus v-else class="h-3.5 w-3.5" :stroke-width="1.75" />
            Create grant
          </button>
        </div>
      </form>
    </section>

    <section class="k-card grid gap-3 p-4">
      <div class="flex items-center justify-between gap-3">
        <div>
          <h3 class="text-[13px] font-semibold text-text-primary">Current provider access</h3>
          <p class="mt-0.5 text-[11px] text-text-muted">Automatic bindings are read-only here. Audit and revocation state remain visible.</p>
        </div>
        <span class="k-badge k-badge--muted">{{ integrations.length }}</span>
      </div>

      <div v-if="loading && integrations.length === 0" class="grid gap-2" role="status" aria-live="polite">
        <div v-for="i in 3" :key="i" class="shimmer h-16 rounded-xl border border-border-subtle bg-surface" />
      </div>
      <div v-else-if="!loading && integrations.length === 0" class="flex min-h-28 items-center justify-center rounded-xl border border-dashed border-border-subtle bg-surface p-4 text-center text-[12px] text-text-muted">
        No provider integrations have been granted for this project.
      </div>
      <div v-else class="grid gap-2">
        <article v-for="integration in integrations" :key="`${integration.environment}:${integration.alias}`" class="k-card k-card--flat grid gap-3 p-3">
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div class="min-w-0">
              <div class="flex flex-wrap items-center gap-2">
                <h4 class="font-mono text-[13px] font-semibold text-text-primary">{{ integration.alias }}</h4>
                <span class="k-badge">{{ providerDisplayName(integration.provider) }}</span>
                <span class="k-badge k-badge--muted">Automatic</span>
                <StatusBadge v-if="integration.phase" :status="integration.phase" />
              </div>
              <p class="mt-1 font-mono text-[11px] text-text-secondary">
                {{ integration.resourceRef?.apiVersion }}/{{ integration.resourceRef?.kind }}/{{ integration.resourceRef?.resource }}/{{ integration.resourceRef?.name || 'unknown' }}
              </p>
            </div>
            <button
              v-if="!automaticProviderAccessOnly"
              type="button"
              class="k-btn k-btn--danger h-8"
              :disabled="busy"
              @click="removeIntegration(integration)"
            >
              <Trash2 class="h-3.5 w-3.5" :stroke-width="1.75" />
              Remove
            </button>
          </div>
          <div class="grid gap-2">
            <div v-for="grant in integration.allowedActions" :key="`${grant.name}/${grant.version}`" class="grid gap-2 rounded-lg border border-border-subtle bg-surface-raised p-2.5 sm:grid-cols-[minmax(0,1fr)_auto] sm:items-center">
              <div class="min-w-0">
                <div class="flex flex-wrap items-center gap-2">
                  <span class="font-mono text-[12px] font-medium text-text-primary">{{ grant.name }}/{{ grant.version }}</span>
                  <StatusBadge :status="grant.revoked ? 'Revoked' : 'Granted'" :tone="grant.revoked ? 'danger' : 'success'" />
                </div>
                <code class="mt-1 block break-all text-[10px] leading-4 text-text-muted">{{ grant.schemaDigest }}</code>
                <div class="mt-1 text-[10px] text-text-muted">
                  Granted by {{ grant.grantedBy || 'server' }} · {{ formatTimestamp(grant.grantedAt) }}
                </div>
                <div v-if="grant.revoked" class="mt-1 text-[10px] text-danger">
                  Revoked by {{ grant.revokedBy || 'server' }} · {{ formatTimestamp(grant.revokedAt) }}
                </div>
              </div>
              <button
                v-if="!automaticProviderAccessOnly && !grant.revoked"
                type="button"
                class="k-btn k-btn--ghost h-8 text-warning"
                :disabled="busy"
                @click="revokeAction(integration, grant.name, grant.version)"
              >
                <Undo2 class="h-3.5 w-3.5" :stroke-width="1.75" />
                Revoke
              </button>
              <span v-else-if="grant.revoked" class="k-badge k-badge--muted h-8">
                <ShieldCheck class="h-3.5 w-3.5" :stroke-width="1.75" />
                Access blocked
              </span>
            </div>
          </div>
        </article>
      </div>
    </section>
  </div>
</template>
