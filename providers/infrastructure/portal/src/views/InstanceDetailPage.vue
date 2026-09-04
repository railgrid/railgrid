<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import { Boxes, CalendarClock, FileCode2, RefreshCw } from 'lucide-vue-next'
import StatusBadge from '../portalkit/StatusBadge.vue'
import ViewValue from '../components/ViewValue.vue'
import { api, isContextChangedError } from '../api'
import ActionMenu, { type ActionMenuItem } from '../portalkit/ActionMenu.vue'
import ConditionsPanel, { type ConditionInfo } from '../portalkit/ConditionsPanel.vue'
import ResourcePage from '../portalkit/ResourcePage.vue'
import ResourceBackLink from '../portalkit/ResourceBackLink.vue'
import ResourceSectionCard from '../portalkit/ResourceSectionCard.vue'
import ResourceStatCards, { type ResourceStatCard } from '../portalkit/ResourceStatCards.vue'
import ResourceTable from '../portalkit/ResourceTable.vue'
import { confirmDialog } from '../portalkit/confirm'
import {
  createAdaptiveRefreshTimer,
  createLatestRefreshController,
  FAST_REFRESH_MS,
  sameResourceIdentity,
  STABLE_REFRESH_MS,
  type ResourceRefreshMode,
  type ResourceTombstones,
} from '../refresh'
import { resolve } from '../view'
import { REASON_INSTANCE_NOT_FOUND, type Instance, type TemplateView } from '../types'

const props = defineProps<{ instanceName: string; tombstones: ResourceTombstones }>()
const emit = defineEmits<{ (e: 'navigate', view: string): void }>()

const inst = ref<Instance | null>(null)
const view = ref<TemplateView | null>(null)
const loading = ref(false)
const refreshMode = ref<ResourceRefreshMode>('foreground')
const loaded = ref(false)
const error = ref<string | null>(null)
const deleting = ref(false)
const deleteError = ref<string | null>(null)
let active = true
let mounted = false
let navigatingAway = false
let acceptedDeletingIdentity: { name: string; uid?: string } | null = null

const DELETING_MESSAGE = 'Deletion is in progress while provisioned resources are cleaned up.'

function instanceIsDeleting(instance: Instance): boolean {
  return Boolean(instance.deletionTimestamp) || props.tombstones.has(instance.name, instance.uid)
}

const deletionInProgress = computed(() => deleting.value || Boolean(inst.value && instanceIsDeleting(inst.value)))
const foregroundLoading = computed(() => loading.value && refreshMode.value === 'foreground')
const actionItems = computed<ActionMenuItem[]>(() => [{
  id: 'delete',
  label: deleting.value || deletionInProgress.value ? 'Deleting instance…' : 'Delete instance',
  tone: 'danger',
  disabled: !inst.value || foregroundLoading.value || deleting.value || deletionInProgress.value,
  busy: deleting.value,
}])
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
const detailGroups = computed(() => {
  const instance = inst.value
  if (!instance) return []
  return (view.value?.detail ?? [])
    .map(group => ({
      ...group,
      fields: group.fields
        .map(field => ({ field, value: resolve(field, instance) }))
        .filter(({ value }) => !value.empty),
    }))
    .filter(group => group.fields.length > 0)
})

const readState = computed<boolean | null>(() => {
  if (loaded.value) return true
  if (error.value) return false
  return loading.value ? false : null
})

const statCards = computed<ResourceStatCard[]>(() => [
  {
    id: 'template',
    label: 'Template',
    value: inst.value?.template || '—',
    icon: FileCode2,
    mono: true,
  },
  {
    id: 'children',
    label: 'Child resources',
    value: inst.value?.children?.length ?? 0,
    detail: 'Reported by controller',
    icon: Boxes,
  },
  {
    id: 'created',
    label: 'Created',
    value: inst.value?.createdAt || '—',
    detail: 'Instance creation time',
    icon: CalendarClock,
    mono: true,
  },
])

function errorMessage(error: unknown, fallback: string): string {
  const value = error as { reason?: string; message?: string }
  return value.reason ? `${value.reason}: ${value.message || fallback}` : value.message || fallback
}

function refreshCadence(): number {
  if (!loaded.value || error.value || deletionInProgress.value || inst.value?.phase !== 'Ready') {
    return FAST_REFRESH_MS
  }
  return STABLE_REFRESH_MS
}

const pollTimer = createAdaptiveRefreshTimer(
  () => { if (!navigatingAway) void load('background') },
  refreshCadence,
)

const refresh = createLatestRefreshController(async (requestID, mode) => {
  refreshMode.value = mode
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
    if (refresh.isCurrent(requestID)) {
      loading.value = false
      if (mounted && active && !navigatingAway) pollTimer.schedule()
    }
  }
})

function load(mode: ResourceRefreshMode = 'foreground'): Promise<void> {
  if (mode === 'foreground' && loading.value) refreshMode.value = 'foreground'
  return refresh.request(mode)
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

function selectAction(action: string) {
  if (action === 'delete') void executeDelete()
}

function goBack() {
  if (deleting.value || deletionInProgress.value) return
  emit('navigate', 'instances')
}

onMounted(() => {
  mounted = true
  if (!loading.value) pollTimer.schedule()
})
onUnmounted(() => {
  active = false
  mounted = false
  pollTimer.stop()
  refresh.stop()
})
</script>

<template>
  <section class="instance-detail">
    <ResourceBackLink
      class="instance-detail__back"
      href="/ui/providers/infrastructure/instances"
      :disabled="deleting || deletionInProgress"
      @back="goBack"
    >
      Instances
    </ResourceBackLink>

    <ResourcePage
      :title="inst?.name || instanceName"
      kind="Instance"
      :loaded="readState"
      :loading="loading"
      :refresh-mode="refreshMode"
      :error="error"
      :stale="loaded && !!error"
      retryable
      @retry="load('foreground')"
    >
      <template #meta>
        <span>Infrastructure</span>
        <span aria-hidden="true">·</span>
        <span v-if="inst">Template <code>{{ inst.template }}</code></span>
        <span v-else class="muted">Template metadata unavailable</span>
      </template>
      <template v-if="inst" #status>
        <span class="instance-status-label">Status</span>
        <StatusBadge
          :status="displayedPhase"
          :tone="displayedPhase === 'Deleting' ? 'warning' : null"
          :title="displayedMessage"
        />
      </template>
      <template #actions>
        <div class="instance-detail__actions" role="group" aria-label="Instance actions">
          <button
            class="k-btn k-btn--ghost icon-text"
            type="button"
            :disabled="foregroundLoading || deleting || deletionInProgress"
            :aria-busy="foregroundLoading || undefined"
            @click="load('foreground')"
          >
            <RefreshCw :size="14" :class="{ spin: foregroundLoading }" aria-hidden="true" />
            {{ foregroundLoading ? 'Refreshing…' : 'Refresh' }}
          </button>
          <ActionMenu
            label="More instance actions"
            :items="actionItems"
            :disabled="!inst || foregroundLoading || deleting || deletionInProgress"
            @select="selectAction"
          />
        </div>
      </template>
      <template #summary>
        <ResourceStatCards :cards="statCards" density="compact" aria-label="Instance summary" />
      </template>
      <template #body>
        <p v-if="deleting" class="instance-message" role="status" aria-live="polite">Deleting this instance. The last successful snapshot remains visible until the hub confirms removal.</p>
        <div v-if="deleteError" class="mutation-error" role="alert" aria-live="assertive">
          <span>{{ deleteError }}</span>
          <button type="button" class="k-btn k-btn--ghost" @click="deleteError = null">Dismiss</button>
        </div>

        <div v-if="inst" class="instance-detail__sections">
          <template v-if="detailGroups.length">
            <ResourceSectionCard
              v-for="(group, groupIndex) in detailGroups"
              :id="`instance-detail-group-${groupIndex}`"
              :key="group.title || groupIndex"
              :title="group.title || ''"
            >
              <dl class="detail-fields">
                <div v-for="entry in group.fields" :key="entry.field.label" class="detail-field">
                  <dt>{{ entry.field.label }}</dt>
                  <dd><ViewValue :value="entry.value" :interactive="!deletionInProgress" /></dd>
                </div>
              </dl>
            </ResourceSectionCard>
          </template>

          <ResourceSectionCard v-else id="instance-values" title="Values">
            <pre>{{ JSON.stringify(inst.values, null, 2) }}</pre>
          </ResourceSectionCard>

          <ResourceSectionCard id="instance-conditions" eyebrow="Diagnostics" title="Conditions" description="Controller conditions and generation freshness for this instance.">
            <ConditionsPanel
              :conditions="conditions"
              :generation="inst.generation"
              :observed-generation="conditionObservedGeneration"
              empty-text="No conditions yet. The infrastructure controller has not reconciled this instance."
            />
          </ResourceSectionCard>

          <ResourceSectionCard id="instance-children" eyebrow="Provisioned resources" title="Child resources" description="Resources reported by the infrastructure controller for this instance.">
            <ResourceTable
              :columns="[
                { key: 'kind', label: 'Kind' },
                { key: 'name', label: 'Name', primary: true },
                { key: 'namespaceLabel', label: 'Namespace' },
                { key: 'phaseLabel', label: 'Phase' },
              ]"
              :rows="childRows"
              aria-label="Instance child resources"
              row-key="rowID"
              :interactive="false"
              empty-text="No child resources have been reported yet."
            >
              <template #kind="{ value }"><span class="k-cell-mono">{{ value }}</span></template>
              <template #name="{ value }"><span class="k-cell-mono">{{ value }}</span></template>
              <template #phaseLabel="{ value }"><StatusBadge :status="String(value)" /></template>
            </ResourceTable>
          </ResourceSectionCard>
        </div>
      </template>
    </ResourcePage>
  </section>
</template>
