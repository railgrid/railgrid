<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies under providers/*/portal/src/portalkit/; edit here and run `make sync-portalkit`. -->
<script setup lang="ts">
import { computed } from 'vue'
import ResourceTable from './ResourceTable.vue'
import StatusBadge from './StatusBadge.vue'

export interface ConditionInfo {
  type: string
  status: string
  reason?: string
  message?: string
  lastTransitionTime?: string
}

export type ConditionSemantics = {
  polarity: 'positive' | 'negative'
  severity?: 'warning' | 'danger'
}

const props = defineProps<{
  conditions: ConditionInfo[]
  generation?: number
  observedGeneration?: number
  emptyText?: string
  /** Hide the heading when a named parent section already supplies it. */
  showTitle?: boolean
  /** Declare fault conditions explicitly; existing positive conditions retain their behavior. */
  semantics?: Record<string, ConditionSemantics>
}>()

const reconciled = computed(() =>
  props.observedGeneration === undefined ||
  props.generation === undefined ||
  props.observedGeneration >= props.generation,
)

const rows = computed<Array<Record<string, unknown>>>(() =>
  props.conditions.map(condition => ({
    ...condition,
    reasonLabel: condition.reason || '-',
    messageLabel: condition.message || '-',
    sinceLabel: condition.lastTransitionTime || '-',
  })),
)

function conditionTone(condition: Pick<ConditionInfo, 'type' | 'status'>): 'success' | 'warning' | 'danger' | 'muted' {
  if (condition.status !== 'True' && condition.status !== 'False') return 'muted'
  const semantics = props.semantics?.[condition.type]
  const healthy = semantics?.polarity === 'negative' ? condition.status === 'False' : condition.status === 'True'
  return healthy ? 'success' : semantics?.severity || 'warning'
}
</script>

<template>
  <div class="k-conditions">
    <h3 v-if="showTitle !== false" class="k-conditions__title">Conditions</h3>
    <p v-if="observedGeneration !== undefined && !reconciled" class="k-conditions__stale">
      Controller has not caught up - spec generation {{ generation }}, observed {{ observedGeneration }}.
    </p>
    <ResourceTable
      :columns="[
        { key: 'type', label: 'Type' },
        { key: 'status', label: 'Status' },
        { key: 'reasonLabel', label: 'Reason' },
        { key: 'messageLabel', label: 'Message', primary: true },
        { key: 'sinceLabel', label: 'Since' },
      ]"
      :rows="rows"
      aria-label="Resource conditions"
      :interactive="false"
      :empty-text="emptyText || 'No conditions yet. The controller has not reconciled this resource.'"
    >
      <template #type="{ value }"><span class="k-conditions__type">{{ value }}</span></template>
      <template #status="{ value, row }"><StatusBadge :status="String(value)" :tone="conditionTone({ type: String(row.type), status: String(value) })" /></template>
      <template #messageLabel="{ value }"><span class="k-conditions__message">{{ value }}</span></template>
      <template #sinceLabel="{ value }"><span class="k-conditions__muted k-cell-mono">{{ value }}</span></template>
    </ResourceTable>
  </div>
</template>
