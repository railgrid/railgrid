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

const props = defineProps<{
  conditions: ConditionInfo[]
  generation?: number
  observedGeneration?: number
  emptyText?: string
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

function conditionTone(status: string): 'success' | 'warning' | 'muted' {
  if (status === 'True') return 'success'
  if (status === 'False') return 'warning'
  return 'muted'
}
</script>

<template>
  <div class="conditions-panel">
    <h3 class="conditions-title">Conditions</h3>
    <p v-if="observedGeneration !== undefined && !reconciled" class="conditions-stale">
      Controller has not caught up - spec generation {{ generation }}, observed {{ observedGeneration }}.
    </p>
    <ResourceTable
      :columns="[
        { key: 'type', label: 'Type' },
        { key: 'status', label: 'Status' },
        { key: 'reasonLabel', label: 'Reason' },
        { key: 'messageLabel', label: 'Message' },
        { key: 'sinceLabel', label: 'Since' },
      ]"
      :rows="rows"
      :interactive="false"
      :empty-text="emptyText || 'No conditions yet. The controller has not reconciled this resource.'"
    >
      <template #type="{ value }"><span class="conditions-type">{{ value }}</span></template>
      <template #status="{ value }"><StatusBadge :status="String(value)" :tone="conditionTone(String(value))" /></template>
      <template #messageLabel="{ value }"><span class="conditions-message">{{ value }}</span></template>
      <template #sinceLabel="{ value }"><span class="conditions-muted">{{ value }}</span></template>
    </ResourceTable>
  </div>
</template>
