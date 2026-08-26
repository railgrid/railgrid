<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import { Activity, ArrowLeft, Database, Ellipsis, Link2, RefreshCw, Settings2 } from 'lucide-vue-next'
import { api } from '../api'
import ConditionsPanel from '../portalkit/ConditionsPanel.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue'
import ResourceStatCards, { type ResourceStatCard } from '../portalkit/ResourceStatCards.vue'
import StatusBadge from '../portalkit/StatusBadge.vue'
import { confirmDialog } from '../portalkit/confirm'
import type { ErrorResponse, Warehouse } from '../types'
import {
  createAdaptiveRefreshTimer,
  createLatestRefreshController,
  createOperationLocks,
  FAST_REFRESH_MS,
  operationKey,
  STABLE_REFRESH_MS,
  type AdaptiveRefreshTimer,
  type LatestRefreshController,
  type ResourceRefreshMode,
} from '../refresh'

const props = defineProps<{ name: string }>()
const emit = defineEmits<{ (e: 'back'): void }>()

const warehouse = ref<Warehouse | null>(null)
const loading = ref(false)
const refreshMode = ref<ResourceRefreshMode>('foreground')
const loaded = ref(false)
const error = ref<string | null>(null)
const editing = ref(false)
const editWarehouseID = ref('')
const saving = ref(false)
const deleting = ref(false)
const saveError = ref<string | null>(null)
const mutationError = ref<string | null>(null)
const editIDInput = ref<HTMLInputElement | null>(null)
const saveErrorRef = ref<HTMLElement | null>(null)
const actionsMenu = ref<HTMLDetailsElement | null>(null)
let poll!: AdaptiveRefreshTimer
let refresh!: LatestRefreshController
const operations = createOperationLocks()

const ready = computed(() => warehouse.value?.conditions.find(c => c.type === 'Ready'))
const reconciled = computed(() =>
  !!warehouse.value &&
  warehouse.value.observedGeneration !== undefined &&
  warehouse.value.generation !== undefined &&
  warehouse.value.observedGeneration >= warehouse.value.generation,
)

const hint = computed(() => {
  const wh = warehouse.value
  if (!wh) return ''
  if (wh.status === 'Ready') return ''
  if (!wh.conditions.length || !reconciled.value) {
    return 'Waiting for the warehouse controller to validate the Databricks warehouse. This usually takes a few seconds after creation.'
  }
  switch (ready.value?.reason) {
    case 'ConnectionUnavailable':
      return `Connection "${wh.connectionRef}" could not be read. Check that it still exists in this workspace.`
    case 'CredentialUnavailable':
      return `The credential for connection "${wh.connectionRef}" could not be read. Check the connection's Secret.`
    case 'ValidationFailed':
      return 'Databricks rejected the warehouse lookup. The warehouse ID may be wrong, deleted, or not visible to this token.'
    default:
      return ready.value?.message || wh.message || 'The warehouse is not ready yet.'
  }
})

type StatTone = 'default' | 'success' | 'warning' | 'danger'

function statTone(status: string | undefined): StatTone {
  if (!status) return 'default'
  if (status === 'Ready') return 'success'
  if (/fail|error|attention|unavailable/i.test(status)) return 'danger'
  return 'warning'
}

const readState = computed<boolean | null>(() => {
  if (loaded.value) return true
  if (error.value) return false
  return loading.value ? false : null
})

const statCards = computed<ResourceStatCard[]>(() => [
  {
    id: 'status',
    label: 'Status',
    value: deleting.value ? 'Deleting' : warehouse.value?.status || '—',
    detail: deleting.value ? 'Deletion in progress' : hint.value || undefined,
    icon: Activity,
    tone: statTone(deleting.value ? 'Deleting' : warehouse.value?.status),
  },
  {
    id: 'state',
    label: 'Databricks state',
    value: warehouse.value?.state || '—',
    icon: Database,
    mono: true,
  },
  {
    id: 'connection',
    label: 'Connection',
    value: warehouse.value?.connectionRef || '—',
    icon: Link2,
    mono: true,
  },
  {
    id: 'warehouse-id',
    label: 'Warehouse ID',
    value: warehouse.value?.warehouseID || '—',
    icon: Database,
    mono: true,
  },
])

function errMessage(e: unknown): string {
  const err = e as ErrorResponse
  return err.reason ? `${err.reason}: ${err.message}` : err.message || String(e)
}

function requestRefresh(mode: ResourceRefreshMode): void {
  if (mode === 'foreground') {
    refreshMode.value = 'foreground'
    loading.value = true
  }
  refresh.request(mode)
}

function load(): void {
  requestRefresh('foreground')
}

function pollCadence(): number {
  const stable = loaded.value && !error.value && !deleting.value && !!warehouse.value &&
    warehouse.value.status === 'Ready' && operationPhase(warehouse.value.name) !== 'deleting'
  return stable ? STABLE_REFRESH_MS : FAST_REFRESH_MS
}

poll = createAdaptiveRefreshTimer(() => requestRefresh('background'), pollCadence)

function operationLocked(name: string): boolean {
  return operations.isLocked(operationKey('warehouse', name))
}

function operationPhase(name: string) {
  return operations.phase(operationKey('warehouse', name))
}

function goBack() {
  if (deleting.value || (warehouse.value && operationLocked(warehouse.value.name))) return
  emit('back')
}

function startEdit() {
  if (!warehouse.value) return
  if (operationLocked(warehouse.value.name)) return
  editWarehouseID.value = warehouse.value.warehouseID
  saveError.value = null
  editing.value = true
  void nextTick(() => editIDInput.value?.focus())
}

async function focusSaveError(message: string) {
  saveError.value = message
  await nextTick()
  saveErrorRef.value?.focus()
}

async function saveEdit() {
  if (!warehouse.value) return
  const nextID = editWarehouseID.value.trim()
  if (!nextID) {
    await focusSaveError('Warehouse ID is required.')
    return
  }
  const lock = operationKey('warehouse', warehouse.value.name)
  if (!operations.acquire(lock, 'saving')) {
    await focusSaveError(`Warehouse "${warehouse.value.name}" already has an operation in progress.`)
    return
  }
  saving.value = true
  saveError.value = null
  mutationError.value = null
  try {
    await api.saveWarehouse({
      name: warehouse.value.name,
      connectionRef: warehouse.value.connectionRef,
      warehouseID: nextID,
    })
    editing.value = false
    load()
  } catch (e) {
    await focusSaveError(errMessage(e))
  } finally {
    saving.value = false
    operations.release(lock)
  }
}

async function remove() {
  const current = warehouse.value
  if (!current || deleting.value) return
  const ok = await confirmDialog({
    title: `Delete warehouse "${current.name}"?`,
    message: 'Tables that reference this warehouse will stop refreshing schema metadata.',
    confirmLabel: 'Delete',
    danger: true,
  })
  if (!ok) return
  const lock = operationKey('warehouse', current.name)
  if (!operations.acquire(lock, 'deleting')) {
    mutationError.value = `Warehouse "${current.name}" already has an operation in progress.`
    return
  }
  deleting.value = true
  mutationError.value = null
  try {
    await api.deleteWarehouse(current.name)
    operations.tombstone(lock, current.uid)
    emit('back')
  } catch (e) {
    mutationError.value = errMessage(e)
  } finally {
    deleting.value = false
    operations.release(lock)
  }
}

function deleteFromMenu() {
  actionsMenu.value?.removeAttribute('open')
  void remove()
}

refresh = createLatestRefreshController(async (requestID, mode) => {
  refreshMode.value = mode
  if (mode === 'foreground') loading.value = true
  try {
    const next = await api.getWarehouse(props.name)
    if (refresh.isCurrent(requestID)) {
      warehouse.value = next
      loaded.value = true
      error.value = null
    }
  } catch (e) {
    if (!refresh.isCurrent(requestID)) return
    const err = e as ErrorResponse
    error.value = err.reason === 'TenantMissing' ? null : errMessage(e)
  } finally {
    if (refresh.isCurrent(requestID)) {
      if (mode === 'foreground') loading.value = false
      poll.schedule()
    }
  }
})

watch(() => props.name, () => {
  warehouse.value = null
  loaded.value = false
  deleting.value = false
  editing.value = false
  error.value = null
  mutationError.value = null
  refresh.invalidate()
  load()
})

onMounted(() => {
  load()
})
onUnmounted(() => {
  poll.stop()
  refresh.stop()
})
</script>

<template>
  <section class="databricks-resource-detail">
    <a class="k-btn k-btn--ghost k-back-action" href="/ui/providers/databricks/warehouses" :aria-disabled="deleting || (!!warehouse && operationLocked(warehouse.name))" @click.prevent="goBack"><ArrowLeft :size="14" aria-hidden="true" /> Warehouses</a>

    <ResourcePage
      :title="warehouse?.name || name"
      kind="Warehouse"
      :loaded="readState"
      :loading="loading"
      :refresh-mode="refreshMode"
      :error="error"
      :stale="loaded && !!error"
      retryable
      @retry="load"
    >
      <template #meta>
        <span>Databricks</span>
      </template>
      <template v-if="warehouse" #status><StatusBadge :status="deleting ? 'Deleting' : warehouse.status" :tone="deleting ? 'warning' : null" :title="warehouse.message" /></template>
      <template #actions>
        <div class="databricks-resource-actions" role="group" aria-label="Warehouse actions">
          <button class="k-btn k-btn--ghost icon-text" type="button" :disabled="loading || deleting || (!!warehouse && operationLocked(warehouse.name))" :aria-busy="loading || undefined" @click="load">
            <RefreshCw :size="14" :class="{ spin: loading }" aria-hidden="true" />
            {{ loading ? 'Refreshing…' : 'Refresh' }}
          </button>
          <details ref="actionsMenu" class="databricks-resource-menu">
            <summary class="k-btn k-btn--ghost" aria-label="More warehouse actions">
              <Ellipsis :size="16" aria-hidden="true" />
              <span class="sr-only">More actions</span>
            </summary>
            <div class="databricks-resource-menu-popover">
              <button type="button" class="databricks-resource-menu-item" :disabled="!warehouse || loading || deleting || operationLocked(warehouse?.name || name)" @click="deleteFromMenu">
                {{ operationPhase(warehouse?.name || name) === 'deleting' ? 'Deleting warehouse…' : 'Delete warehouse' }}
              </button>
            </div>
          </details>
        </div>
      </template>
      <template #summary><ResourceStatCards :cards="statCards" density="compact" aria-label="Warehouse summary" /></template>
      <template #body>
        <span v-if="loading" class="sr-only" role="status" aria-live="polite">Updating…</span>
        <p v-if="deleting" class="warning deletion-progress" role="status" aria-live="polite">Deleting this warehouse. The last successful snapshot remains visible until the hub confirms removal.</p>
        <p v-if="mutationError" class="error mutation-error" role="alert" aria-live="assertive">
          <span>{{ mutationError }}</span>
          <button class="k-btn k-btn--ghost" type="button" @click="mutationError = null">Dismiss</button>
        </p>

        <div v-if="warehouse" class="databricks-resource-sections">
          <ResourceSectionCard id="warehouse-overview" eyebrow="Warehouse" title="Overview" description="Connection reference, Databricks state, and reconciliation details.">
            <template #actions>
              <button v-if="!editing" class="k-btn k-btn--ghost icon-text" type="button" :disabled="operationLocked(warehouse.name)" @click="startEdit"><Settings2 :size="14" aria-hidden="true" /> Edit warehouse</button>
            </template>
            <dl class="props">
          <dt>Connection</dt><dd><code>{{ warehouse.connectionRef }}</code></dd>
          <dt>Warehouse ID</dt><dd><code>{{ warehouse.warehouseID }}</code></dd>
          <dt>State</dt><dd>{{ warehouse.state || '—' }}</dd>
          <dt v-if="warehouse.creationTimestamp">Created</dt><dd v-if="warehouse.creationTimestamp">{{ warehouse.creationTimestamp }}</dd>
          <dt v-if="warehouse.observedGeneration !== undefined">Reconciled</dt>
          <dd v-if="warehouse.observedGeneration !== undefined">
            <span v-if="reconciled" class="muted">up to date (generation {{ warehouse.generation }})</span>
            <span v-else class="warning">controller has not caught up (spec {{ warehouse.generation }}, observed {{ warehouse.observedGeneration }})</span>
          </dd>
            </dl>
          </ResourceSectionCard>

          <ResourceSectionCard v-if="editing" id="warehouse-edit" eyebrow="Configuration" title="Edit warehouse" description="Update the Databricks SQL warehouse identifier used for validation.">
        <form class="form" @submit.prevent="saveEdit">
          <div class="field">
            <label class="field-label" for="warehouse-edit-id">Warehouse ID</label>
            <input id="warehouse-edit-id" class="k-input" ref="editIDInput" v-model="editWarehouseID" :disabled="saving" placeholder="abc123def4567890" autocomplete="off" required aria-required="true" aria-describedby="warehouse-edit-id-hint warehouse-edit-error" :aria-invalid="!!saveError" />
            <span id="warehouse-edit-id-hint" class="field-hint">Use the 16-character ID from SQL Warehouses → Connection details (/sql/1.0/warehouses/&lt;id&gt;), not the numeric ?o= workspace ID.</span>
          </div>
        <div class="actions">
            <button class="k-btn k-btn--primary" type="submit" :disabled="saving || operationLocked(warehouse.name)">{{ saving ? 'Saving…' : 'Save' }}</button>
            <button class="k-btn k-btn--ghost" type="button" :disabled="saving" @click="editing = false">Cancel</button>
            <span v-if="saveError" id="warehouse-edit-error" ref="saveErrorRef" class="error" role="alert" aria-live="assertive" tabindex="-1">{{ saveError }}</span>
          </div>
        </form>
          </ResourceSectionCard>

          <ResourceSectionCard id="warehouse-conditions" eyebrow="Diagnostics" title="Conditions" description="Controller conditions and observed generation for this warehouse.">
            <ConditionsPanel
              :conditions="warehouse.conditions"
              :generation="warehouse.generation"
              :observed-generation="warehouse.observedGeneration"
              empty-text="No conditions yet. Warehouse validation has not reported status for this resource."
            />
          </ResourceSectionCard>
        </div>
      </template>
    </ResourcePage>
  </section>
</template>
