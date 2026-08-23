<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { ArrowLeft } from 'lucide-vue-next'
import StatusBadge from '../portalkit/StatusBadge.vue'
import ViewValue from '../components/ViewValue.vue'
import { api, isContextChangedError } from '../api'
import ConditionsPanel, { type ConditionInfo } from '../portalkit/ConditionsPanel.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import { confirmDialog } from '../portalkit/confirm'
import { createLatestRefreshController, sameResourceIdentity, type ResourceTombstones } from '../refresh'
import { resolve } from '../view'
import { REASON_INSTANCE_NOT_FOUND, type Instance, type TemplateView } from '../types'

const props = defineProps<{ instanceName: string; tombstones: ResourceTombstones }>()
const emit = defineEmits<{ (e: 'navigate', view: string): void }>()

const inst = ref<Instance | null>(null)
const view = ref<TemplateView | null>(null)
const loading = ref(false)
const loaded = ref(false)
const error = ref<string | null>(null)
const deleting = ref(false)
const deleteError = ref<string | null>(null)
let pollHandle: number | null = null
let active = true
let navigatingAway = false
let acceptedDeletingIdentity: { name: string; uid?: string } | null = null

const DELETING_MESSAGE = 'Deletion is in progress while provisioned resources are cleaned up.'

function instanceIsDeleting(instance: Instance): boolean {
  return Boolean(instance.deletionTimestamp) || props.tombstones.has(instance.name, instance.uid)
}

const deletionInProgress = computed(() => Boolean(inst.value && instanceIsDeleting(inst.value)))
const displayedPhase = computed(() => deletionInProgress.value ? 'Deleting' : inst.value?.phase ?? '')
const displayedMessage = computed(() => {
  if (!inst.value) return undefined
  return deletionInProgress.value ? DELETING_MESSAGE : inst.value.message
})

function acceptedDeletingInstance(instance: Instance): boolean {
  if (!acceptedDeletingIdentity || acceptedDeletingIdentity.name !== instance.name) return false
  return acceptedDeletingIdentity.uid === undefined || instance.uid === undefined ||
    acceptedDeletingIdentity.uid === instance.uid
}

const conditions = computed<ConditionInfo[]>(() => (inst.value?.conditions ?? []).map(condition => ({
  type: condition.type,
  status: condition.status,
  reason: condition.reason,
  message: condition.message,
  lastTransitionTime: condition.time,
})))
const conditionObservedGeneration = computed(() => {
  if (inst.value?.observedGeneration !== undefined) return inst.value.observedGeneration
  return undefined
})
const childRows = computed<Array<Record<string, unknown>>>(() => (inst.value?.children ?? []).map(child => ({
  ...child,
  rowID: `${child.apiVersion}/${child.kind}/${child.namespace}/${child.name}`,
  namespaceLabel: child.namespace || '—',
  phaseLabel: child.phase || '—',
})))

function errorMessage(error: unknown, fallback: string): string {
  const value = error as { reason?: string; message?: string }
  return value.reason ? `${value.reason}: ${value.message || fallback}` : value.message || fallback
}

const refresh = createLatestRefreshController(async requestID => {
  loading.value = true
  try {
    const instance = await api.getInstance(props.instanceName)
    if (!refresh.isCurrent(requestID)) return
    if (props.tombstones.has(instance.name, instance.uid) && !acceptedDeletingInstance(instance)) {
      navigatingAway = true
      emit('navigate', 'instances')
      return
    }
    if (instance.deletionTimestamp) {
      props.tombstones.add(instance.name, instance.uid)
      acceptedDeletingIdentity = { name: instance.name, uid: instance.uid }
    } else if (acceptedDeletingIdentity && !acceptedDeletingInstance(instance)) {
      acceptedDeletingIdentity = null
    }
    let nextView = view.value
    try {
      nextView = (await api.getTemplate(instance.template)).template.view ?? null
    } catch (caught) {
      if (isContextChangedError(caught)) throw caught
      // Presentation metadata is secondary to the instance read. Keep the
      // last successful detail view during a transient template failure.
    }
    if (!refresh.isCurrent(requestID)) return
    inst.value = instance
    view.value = nextView
    loaded.value = true
    error.value = null
  } catch (caught) {
    if (!refresh.isCurrent(requestID) || isContextChangedError(caught)) return
    const reason = (caught as { reason?: string }).reason
    if (reason === REASON_INSTANCE_NOT_FOUND &&
      (props.tombstones.has(props.instanceName) || (inst.value && instanceIsDeleting(inst.value)))) {
      navigatingAway = true
      emit('navigate', 'instances')
      return
    }
    error.value = errorMessage(caught, 'failed to get instance')
  } finally {
    if (refresh.isCurrent(requestID)) loading.value = false
  }
})

function load(): Promise<void> {
  return refresh.request()
}

watch(
  () => props.instanceName,
  () => {
    refresh.invalidate()
    navigatingAway = false
    acceptedDeletingIdentity = null
    inst.value = null
    view.value = null
    loaded.value = false
    error.value = null
    void load()
  },
  { immediate: true },
)

async function executeDelete() {
  if (deleting.value || deletionInProgress.value || !inst.value) return
  const expectedInstance = inst.value
  deleteError.value = null
  const confirmed = await confirmDialog({
    title: `Delete instance "${props.instanceName}"?`,
    message: `This permanently deletes "${props.instanceName}" and the resources (and bridged credentials Secret) it provisioned. This cannot be undone.`,
    confirmLabel: 'Delete instance',
    danger: true,
  })
  if (!confirmed || !active || !sameResourceIdentity(expectedInstance, inst.value) || instanceIsDeleting(inst.value)) return

  const deletingInstance = inst.value
  deleting.value = true
  try {
    await api.deleteInstance(props.instanceName)
    if (active) {
      props.tombstones.add(deletingInstance.name, deletingInstance.uid)
      navigatingAway = true
      emit('navigate', 'instances')
    }
  } catch (caught) {
    if (active && !isContextChangedError(caught)) deleteError.value = errorMessage(caught, 'delete failed')
  } finally {
    deleting.value = false
  }
}

onMounted(() => {
  pollHandle = window.setInterval(() => {
    if (!navigatingAway) void load()
  }, 10000)
})
onUnmounted(() => {
  active = false
  if (pollHandle !== null) window.clearInterval(pollHandle)
  refresh.stop()
})
</script>

<template>
  <section class="page instance-detail" :aria-busy="loading">
    <button
      type="button"
      class="k-btn k-btn--ghost"
      :disabled="deleting"
      @click="emit('navigate', 'instances')"
    >
      <ArrowLeft :size="14" aria-hidden="true" />
      <span>Back to instances</span>
    </button>

    <div v-if="!loaded && loading" class="page-loading-shell" role="status" aria-live="polite" aria-busy="true">
      <span>Loading instance…</span>
      <div class="shimmer page-loading-line page-loading-line-short" aria-hidden="true" />
      <div class="shimmer page-loading-panel" aria-hidden="true" />
      <div class="shimmer page-loading-panel" aria-hidden="true" />
    </div>
    <div v-else-if="!loaded && error" class="read-error" role="alert" aria-live="assertive">
      <span>{{ error }}</span>
      <button type="button" class="read-retry" @click="load">Retry</button>
    </div>

    <template v-else-if="inst">
      <span v-if="loading" class="sr-only" role="status" aria-live="polite">Updating instance…</span>
      <div v-if="error" class="stale-banner" role="alert" aria-live="assertive">
        <span>Showing the last successful result. {{ error }}</span>
        <button type="button" class="read-retry" @click="load">Retry</button>
      </div>
      <div v-if="deleteError" class="mutation-error" role="alert" aria-live="assertive">
        <span>{{ deleteError }}</span>
        <button type="button" class="read-retry" @click="deleteError = null">Dismiss</button>
      </div>

      <header class="instance-detail-head">
        <div>
          <div class="instance-detail-title">
            <h2 class="page-title">{{ inst.name }}</h2>
            <StatusBadge :status="displayedPhase" :tone="displayedPhase === 'Deleting' ? 'warning' : null" />
          </div>
          <p class="page-meta">{{ inst.template }}</p>
        </div>
        <button type="button" class="k-btn k-btn--danger" :disabled="deleting || deletionInProgress" @click="executeDelete">
          {{ deleting || deletionInProgress ? 'Deleting…' : 'Delete' }}
        </button>
      </header>

      <div v-if="displayedMessage" class="instance-message">{{ displayedMessage }}</div>

      <template v-if="view?.detail?.length">
        <div v-for="(group, groupIndex) in view.detail" :key="group.title || groupIndex" class="detail-group">
          <div v-if="group.title" class="detail-group-title">{{ group.title }}</div>
          <dl class="detail-fields">
            <div v-for="field in group.fields" :key="field.label" class="detail-field">
              <dt>{{ field.label }}</dt>
              <dd><ViewValue :value="resolve(field, inst)" :interactive="!deletionInProgress" /></dd>
            </div>
          </dl>
        </div>
      </template>

      <div v-else class="detail-group">
        <div class="detail-group-title">Values</div>
        <pre>{{ JSON.stringify(inst.values, null, 2) }}</pre>
      </div>

      <ConditionsPanel
        :conditions="conditions"
        :generation="inst.generation"
        :observed-generation="conditionObservedGeneration"
        empty-text="No conditions yet. The infrastructure controller has not reconciled this instance."
      />

      <div class="detail-group">
        <div class="detail-group-title">Child resources</div>
        <ResourceTable
          :columns="[
            { key: 'kind', label: 'Kind' },
            { key: 'name', label: 'Name' },
            { key: 'namespaceLabel', label: 'Namespace' },
            { key: 'phaseLabel', label: 'Phase' },
          ]"
          :rows="childRows"
          row-key="rowID"
          :interactive="false"
          empty-text="No child resources have been reported yet."
        >
          <template #kind="{ value }"><span class="k-cell-mono">{{ value }}</span></template>
          <template #name="{ value }"><span class="k-cell-mono">{{ value }}</span></template>
          <template #phaseLabel="{ value }"><StatusBadge :status="String(value)" /></template>
        </ResourceTable>
      </div>
    </template>
  </section>
</template>
