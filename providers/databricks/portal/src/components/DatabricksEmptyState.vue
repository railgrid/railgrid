<script setup lang="ts">
import { Link2, Table2, Warehouse } from 'lucide-vue-next'
import { computed } from 'vue'
import FirstRunGuide from '../portalkit/FirstRunGuide.vue'
import {
  DATABRICKS_JOURNEY_STEPS,
  firstRunModel,
  type DatabricksJourneyAction,
  type DatabricksResourceKind,
} from '../journey'

const props = withDefaults(defineProps<{
  kind: DatabricksResourceKind
  hasConnections?: boolean
  hasWarehouses?: boolean
}>(), {
  hasConnections: false,
  hasWarehouses: false,
})

const emit = defineEmits<{ (event: 'action', action: DatabricksJourneyAction): void }>()
const model = computed(() => firstRunModel(props.kind, props.hasConnections, props.hasWarehouses))
const icons = [Link2, Warehouse, Table2] as const
</script>

<template>
  <FirstRunGuide
    :title="model.title"
    :description="model.description"
    :primary-label="model.primary.label"
    :secondary-label="model.secondary?.label"
    :steps="DATABRICKS_JOURNEY_STEPS"
    :current-step="model.currentStep"
    journey-label="Databricks setup path"
    @primary="emit('action', model.primary.action)"
    @secondary="model.secondary && emit('action', model.secondary.action)"
  >
    <template #icon><component :is="icons[model.currentStep]" :stroke-width="1.5" /></template>
  </FirstRunGuide>
</template>
