<script setup lang="ts">
import { computed, onActivated, onMounted, onUnmounted, ref } from 'vue'
import { api } from '../api'
import type { Connection, ErrorResponse } from '../types'
import ResourceTable from '../portalkit/ResourceTable.vue'
import ResourceTableDeleteButton from '../portalkit/ResourceTableDeleteButton.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { confirmDialog } from '../portalkit/confirm'
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
const mutationError = ref<string | null>(null)
const loading = ref(false)
const loaded = ref(false)
const operations = createOperationLocks()
const columns = [
  { key: 'name', label: 'Name' },
  { key: 'owner', label: 'Owner' },
  { key: 'login', label: 'Login' },
  { key: 'status', label: 'Status' },
  { key: 'actions', label: '' },
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
  oauthError.value = null
  try {
    const config = await api.oauthConfig()
    if (!mounted) return
    oauthEnabled.value = config.enabled
  } catch (e) {
    if (!mounted) return
    oauthEnabled.value = false
    oauthError.value = errMessage(e)
  } finally {
    if (mounted) oauthLoaded.value = true
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
  mutationError.value = null
  try {
    await api.deleteConnection(connection.name)
    props.deletions.acknowledge(deletionScope, connection.name, connection.uid)
    load()
  } catch (e) {
    mutationError.value = errMessage(e)
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
    error.value = null
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
        <h2 class="page-title">Connections</h2>
        <p class="page-meta">A connection binds your workspace to a git account. Repositories are created under it.</p>
      </div>
      <div class="code-form-actions">
        <button v-if="oauthEnabled" class="k-btn k-btn--primary" :disabled="!loaded" @click="emit('create', 'github')">
          Connect with GitHub
        </button>
        <button class="k-btn" :class="oauthEnabled ? 'k-btn--ghost' : 'k-btn--primary'" :disabled="!loaded" @click="emit('create', 'token')">
          Add token manually
        </button>
      </div>
    </header>

    <p v-if="oauthError" class="error read-error" role="alert">
      <span>GitHub sign-in configuration could not be loaded: {{ oauthError }}</span>
      <button class="k-btn k-btn--ghost" type="button" @click="loadOAuthConfig">Retry GitHub sign-in</button>
    </p>
    <p v-else-if="oauthLoaded && !oauthEnabled" class="muted">
      Tip: a platform admin can enable one-click “Connect with GitHub” by configuring the provider’s GitHub OAuth app.
    </p>

    <p v-if="mutationError" class="error mutation-error" role="alert" aria-live="assertive">{{ mutationError }}</p>
    <ResourceTable
      :columns="columns"
      :rows="rows"
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
      <template #name="{ value, row }"><span v-if="row.deleting">{{ value }}</span><button v-else class="k-btn k-btn--ghost k-table-resource-link" type="button" @click.stop="openConnection(row)">{{ value }}</button></template>
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
