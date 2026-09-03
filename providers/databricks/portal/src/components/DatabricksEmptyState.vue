<script setup lang="ts">
import { ArrowRight, Check, CircleDot, Link2, Table2, Warehouse } from 'lucide-vue-next'
import { computed } from 'vue'
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
const titleID = computed(() => `databricks-${props.kind}-first-run-title`)
const icons = [Link2, Warehouse, Table2] as const
</script>

<template>
  <section class="databricks-first-run" :aria-labelledby="titleID">
    <div class="databricks-first-run__lead">
      <component :is="icons[model.currentStep]" class="databricks-first-run__icon" :stroke-width="1.5" aria-hidden="true" />
      <div class="databricks-first-run__copy">
        <h3 :id="titleID">{{ model.title }}</h3>
        <p>{{ model.description }}</p>
      </div>
      <div class="databricks-first-run__actions">
        <button class="k-btn k-btn--primary" type="button" @click="emit('action', model.primary.action)">
          {{ model.primary.label }} <ArrowRight :stroke-width="1.75" aria-hidden="true" />
        </button>
        <button v-if="model.secondary" class="k-btn k-btn--ghost" type="button" @click="emit('action', model.secondary.action)">
          {{ model.secondary.label }}
        </button>
      </div>
    </div>

    <ol class="databricks-journey" aria-label="Databricks setup path">
      <li
        v-for="(step, index) in DATABRICKS_JOURNEY_STEPS"
        :key="step.label"
        :class="['databricks-journey__step', {
          'databricks-journey__step--complete': index < model.currentStep,
          'databricks-journey__step--current': index === model.currentStep,
        }]"
        :aria-current="index === model.currentStep ? 'step' : undefined"
      >
        <span class="databricks-journey__marker" aria-hidden="true">
          <Check v-if="index < model.currentStep" :stroke-width="2" />
          <CircleDot v-else-if="index === model.currentStep" :stroke-width="1.75" />
          <span v-else>{{ index + 1 }}</span>
        </span>
        <span class="databricks-journey__copy">
          <strong>{{ step.label }}</strong>
          <small>{{ step.description }}</small>
        </span>
      </li>
    </ol>
  </section>
</template>
