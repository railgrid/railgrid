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
  <div class="flex flex-col gap-3">
    <h3 class="text-[11px] font-semibold uppercase tracking-wide text-text-secondary">Conditions</h3>
    <p v-if="observedGeneration !== undefined && !reconciled" class="m-0 text-[12px] text-warning">
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
      <template #type="{ value }"><span class="font-semibold text-text-primary">{{ value }}</span></template>
      <template #status="{ value }"><StatusBadge :status="String(value)" :tone="conditionTone(String(value))" /></template>
      <template #messageLabel="{ value }"><span class="block max-w-[40ch] whitespace-normal break-words">{{ value }}</span></template>
      <template #sinceLabel="{ value }"><span class="text-text-muted">{{ value }}</span></template>
    </ResourceTable>
  </div>
</template>
