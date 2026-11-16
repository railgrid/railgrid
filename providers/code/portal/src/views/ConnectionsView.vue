<script setup lang="ts">
import { ArrowRight, Link2 } from 'lucide-vue-next'
import { computed, nextTick, onActivated, onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api'
import { CODE_JOURNEY_STEPS, codeFirstRunModel } from '../journey'
import type { Connection, ErrorResponse } from '../types'
import FirstRunGuide from '../portalkit/FirstRunGuide.vue'
import InlineNotification from '../portalkit/InlineNotification.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { confirmDialog } from '../portalkit/confirm'
import { toast } from '../portalkit/toast'
import { portalHref } from '../portalkit/navigation'
import {
  FAST_REFRESH_MS,
  STABLE_REFRESH_MS,
  createAdaptiveRefreshTimer,
  createLatestRefreshController,
  createOperationLocks,
  operationKey,
  type LatestRefreshController,
  type ResourceDeletions,
  type ResourceRefreshMode,
} from '../refresh'

const props = defineProps<{ deletions: ResourceDeletions }>()
const emit = defineEmits<{
  (e: 'open', name: string): void
  (e: 'create', method: 'token' | 'github'): void
}>()
const deletionScope = 'connection'

const connections = ref<Connection[]>([])
const error = ref<string | null>(null)
const mutationErrors = ref<Record<string, { name: string; message: string }>>({})
const loading = ref(false)
const loaded = ref(false)
const operations = createOperationLocks()
const columns = [
  { key: 'name', label: 'Name', primary: true },
  { key: 'owner', label: 'Owner' },
  { key: 'login', label: 'Login' },
  { key: 'status', label: 'Status' },
  { key: 'actions', label: '', ariaLabel: 'Actions' },
]
const rows = computed<Array<Record<string, unknown>>>(() => connections.value
  .map(connection => {
    const deleting = isDeleting(connection)
    return { ...connection, deleting, status: deleting ? 'Deleting' : connection.validated ? 'ready' : 'pending', actions: '' }
  }))

function isDeleting(connection: Pick<Connection, 'name' | 'uid' | 'deletionTimestamp'>): boolean {
  return !!connection.deletionTimestamp || props.deletions.has(deletionScope, connection.name, connection.uid)
}

// GitHub OAuth ("Connect with GitHub") — enabled when the provider is configured.
const oauthEnabled = ref(false)
const oauthLoaded = ref(false)
const oauthError = ref<string | null>(null)
const oauthLoading = ref(false)
const oauthWarning = ref<HTMLElement | null>(null)
const showFirstRun = computed(() => loaded.value && oauthLoaded.value && connections.value.length === 0)
const firstRunWarning = ref<HTMLElement | null>(null)
const pageTitle = ref<HTMLElement | null>(null)
const firstRun = computed(() => codeFirstRunModel('connection', false, oauthEnabled.value))
let mounted = false
let refresh!: LatestRefreshController
const refreshMode = ref<ResourceRefreshMode>('foreground')
const poller = createAdaptiveRefreshTimer(() => load('background'), () => {
  if (!loaded.value || error.value) return FAST_REFRESH_MS
  const pending = connections.value.some(connection => (
    !!connection.deletionTimestamp || !connection.validated || (
      connection.generation !== undefined &&
      (connection.observedGeneration === undefined || connection.observedGeneration < connection.generation)
    )
  ))
  return pending ? FAST_REFRESH_MS : STABLE_REFRESH_MS
})

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

async function loadOAuthConfig(): Promise<void> {
  if (oauthLoading.value) return
  oauthLoading.value = true
  try {
    const config = await api.oauthConfig()
    if (!mounted) return
    oauthEnabled.value = config.enabled
    const retryHadFocus = typeof document !== 'undefined' && !!oauthWarning.value?.contains(document.activeElement)
    oauthError.value = null
    if (retryHadFocus) void nextTick(() => {
      if (document.activeElement === document.body) pageTitle.value?.focus({ preventScroll: true })
    })
  } catch (e) {
    if (!mounted) return
    oauthEnabled.value = false
    oauthError.value = errMessage(e)
  } finally {
    if (mounted) {
      oauthLoaded.value = true
      oauthLoading.value = false
    }
  }
}

function load(mode: ResourceRefreshMode = 'foreground') {
  if (mode === 'foreground') {
    refreshMode.value = 'foreground'
    loading.value = true
  }
  refresh.request(mode)
}

function openConnection(row: Record<string, unknown>) {
  const resourceName = String(row.name)
  if (!row.deleting && !operations.isLocked(operationKey('connection', resourceName))) emit('open', resourceName)
}

function openConnectionLink(event: MouseEvent, row: Record<string, unknown>): void {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  openConnection(row)
}

function createConnectionHref(method: 'token' | 'github'): string {
  return portalHref(`/ui/providers/code/create/connection/${method}`)
}

function followCreateLink(event: MouseEvent, method: 'token' | 'github'): void {
  if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
  event.preventDefault()
  emit('create', method)
}

function clearMutationError(name: string) {
  const next = { ...mutationErrors.value }
  delete next[name]
  mutationErrors.value = next
}
async function dismissMutationError(name: string) {
  clearMutationError(name)
  await nextTick()
  pageTitle.value?.focus({ preventScroll: true })
}

async function remove(row: Record<string, unknown>) {
  const connection = row as unknown as Connection
  if (isDeleting(connection)) return
  const ok = await confirmDialog({
    title: `Delete connection "${connection.name}"?`,
    message: 'Repositories using it will stop reconciling.',
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok || !mounted) return
  const lock = operationKey('connection', connection.name)
  if (!operations.acquire(lock, 'deleting')) return
  try {
    await api.deleteConnection(connection.name)
    if (!mounted) return
    clearMutationError(`${connection.name}:${connection.uid || ""}`)
    props.deletions.acknowledge(deletionScope, connection.name, connection.uid)
    toast('info', `Connection deletion requested for ${connection.name}.`)
    load()
  } catch (e) {
    if (mounted && (e as ErrorResponse)?.reason !== 'ContextChanged') mutationErrors.value = { ...mutationErrors.value, [`${connection.name}:${connection.uid || ""}`]: { name: connection.name, message: errMessage(e) } }
  } finally {
    operations.release(lock)
  }
}

refresh = createLatestRefreshController(async (requestID, mode) => {
  refreshMode.value = mode
  loading.value = true
  try {
    const next = await api.listConnections()
    if (!refresh.isCurrent(requestID)) return
    connections.value = next
    next.filter(item => item.deletionTimestamp).forEach(item => props.deletions.acknowledge(deletionScope, item.name, item.uid))
    props.deletions.reconcile(deletionScope, next)
    loaded.value = true
    const retryHadFocus = typeof document !== 'undefined' && !!firstRunWarning.value?.contains(document.activeElement)
    error.value = null
    if (retryHadFocus) void nextTick(() => {
      if (document.activeElement === document.body) pageTitle.value?.focus({ preventScroll: true })
    })
  } catch (e) {
    if (!refresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (refresh.isCurrent(requestID)) {
      loading.value = false
      poller.schedule()
    }
  }
})

onMounted(() => {
  mounted = true
  void loadOAuthConfig()
})
onActivated(() => {
  load()
  poller.schedule()
})
onUnmounted(() => {
  mounted = false
  poller.stop()
  refresh.stop()
})
</script>

<template>
  <section class="page">
    <header class="page-head">
      <div>
        <h2 ref="pageTitle" class="page-title" tabindex="-1">Connections</h2>
        <p class="page-meta">A connection binds your workspace to a git account. Repositories are created under it.</p>
      </div>
      <div v-if="!showFirstRun" class="code-form-actions">
        <a v-if="oauthEnabled && loaded" class="k-btn k-btn--primary" :href="createConnectionHref('github')" @click="followCreateLink($event, 'github')">
          Connect with GitHub
        </a>
        <button v-else-if="oauthEnabled" class="k-btn k-btn--primary" type="button" disabled>Connect with GitHub</button>
        <a v-if="loaded" class="k-btn" :class="oauthEnabled ? 'k-btn--ghost' : 'k-btn--primary'" :href="createConnectionHref('token')" @click="followCreateLink($event, 'token')">
          Add token manually
        </a>
        <button v-else class="k-btn" :class="oauthEnabled ? 'k-btn--ghost' : 'k-btn--primary'" type="button" disabled>Add token manually</button>
      </div>
    </header>

    <p v-if="oauthError" ref="oauthWarning" class="error read-error" role="alert">
      <span>GitHub sign-in configuration could not be loaded: {{ oauthError }}</span>
      <button class="k-btn k-btn--ghost" type="button" :aria-disabled="oauthLoading" @click="loadOAuthConfig">{{ oauthLoading ? 'Retrying GitHub sign-in…' : 'Retry GitHub sign-in' }}</button>
    </p>
    <p v-else-if="oauthLoaded && !oauthEnabled" class="muted">
      Tip: a platform admin can enable one-click “Connect with GitHub” by configuring the provider’s GitHub OAuth app.
    </p>

    <InlineNotification v-for="(failure, key) in mutationErrors" :key="key" tone="error" :title="`Could not delete connection ${failure.name}`" :message="`${failure.message} Try again using the connection delete action.`" dismissible @dismiss="dismissMutationError(String(key))" />
    <div v-if="showFirstRun && error" ref="firstRunWarning">
      <InlineNotification tone="warning" message="Connections could not be refreshed. Showing the last successful empty result." action-label="Retry" action-busy-label="Retrying…" :action-busy="loading" @action="load()" />
    </div>
    <FirstRunGuide
      v-if="showFirstRun"
      :title="firstRun.title"
      :description="firstRun.description"
      :primary-label="firstRun.primary.label"
      :secondary-label="firstRun.secondary?.label"
      :steps="CODE_JOURNEY_STEPS"
      :current-step="firstRun.currentStep"
      journey-label="Code setup path"
    >
      <template #icon><Link2 :stroke-width="1.5" /></template>
      <template #actions>
        <a class="k-btn k-btn--primary" :href="createConnectionHref(oauthEnabled ? 'github' : 'token')" @click="followCreateLink($event, oauthEnabled ? 'github' : 'token')">{{ firstRun.primary.label }} <ArrowRight :stroke-width="1.75" aria-hidden="true" /></a>
        <a v-if="firstRun.secondary" class="k-btn k-btn--ghost" :href="createConnectionHref('token')" @click="followCreateLink($event, 'token')">{{ firstRun.secondary.label }}</a>
      </template>
    </FirstRunGuide>

    <ResourceTable
      v-else
      :columns="columns"
      :rows="rows"
      aria-label="Git connections"
      searchable
      search-placeholder="Search connections…"
      :filters="[{ key: 'owner', label: 'Owner' }, { key: 'status', label: 'Status', allLabel: 'Any status' }]"
      paginated
      :page-size="10"
      row-key="name"
      :loaded="loaded"
      :loading="loading"
      :refresh-mode="refreshMode"
      :error="error"
      :stale="loaded && !!error"
      retryable
      empty-text="No connections yet."
      :row-aria-label="(row) => `Open connection ${String(row.name)}`"
      @retry="load"
      @row-click="openConnection"
    >
      <template #name="{ value, row }"><span v-if="row.deleting || operations.isLocked(operationKey('connection', String(row.name)))">{{ value }}</span><a v-else class="k-btn k-btn--ghost k-table-resource-link" :href="portalHref(`/ui/providers/code/connections/${encodeURIComponent(String(row.name))}`)" @click.stop="openConnectionLink($event, row)">{{ value }}</a></template>
      <template #owner="{ value }">{{ value }}</template>
      <template #login="{ value }">{{ value || '—' }}</template>
      <template #status="{ row }"><StatusBadge :status="String(row.status)" :tone="row.deleting ? 'warning' : null" :title="String(row.message || '')" /></template>
      <template #actions="{ row }">
        <div class="code-row-actions">
          <ResourceTableDeleteButton
            :label="`Delete connection ${String(row.name)}`"
            :busy-label="`Deleting connection ${String(row.name)}…`"
            :busy="Boolean(row.deleting) || operations.phase(operationKey('connection', String(row.name))) === 'deleting'"
            :disabled="Boolean(row.deleting) || operations.isLocked(operationKey('connection', String(row.name)))"
            @click="remove(row)"
          />
        </div>
      </template>
    </ResourceTable>
  </section>
</template>
