<!--
  CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
  under providers/*/portal/src/portalkit/; edit here and run
  `make sync-portalkit`.
-->
<script setup lang="ts">
import { ArrowRight, Check, CircleDot } from 'lucide-vue-next'
import { computed, useId } from 'vue'
import { ensureFarosUIStyles } from '../portalkit/styles'

ensureFarosUIStyles()

export interface FirstRunStep {
  label: string
  description: string
}

const props = withDefaults(defineProps<{
  title: string
  description: string
  primaryLabel: string
  secondaryLabel?: string
  steps: readonly FirstRunStep[]
  currentStep?: number
  journeyLabel?: string
}>(), {
  secondaryLabel: '',
  currentStep: 0,
  journeyLabel: 'Getting started',
})

const emit = defineEmits<{
  (event: 'primary'): void
  (event: 'secondary'): void
}>()

const id = useId().replace(/[^a-zA-Z0-9_-]/g, '-')
const titleID = `k-first-run-${id}-title`
const boundedCurrentStep = computed(() => Math.max(0, Math.min(props.currentStep, props.steps.length - 1)))
</script>

<template>
  <section class="k-first-run" :aria-labelledby="titleID">
    <div class="k-first-run__lead">
      <span class="k-first-run__icon" aria-hidden="true"><slot name="icon" /></span>
      <div class="k-first-run__copy">
        <h3 :id="titleID">{{ title }}</h3>
        <p>{{ description }}</p>
      </div>
      <div class="k-first-run__actions">
        <button class="k-btn k-btn--primary" type="button" @click="emit('primary')">
          {{ primaryLabel }} <ArrowRight :stroke-width="1.75" aria-hidden="true" />
        </button>
        <button v-if="secondaryLabel" class="k-btn k-btn--ghost" type="button" @click="emit('secondary')">
          {{ secondaryLabel }}
        </button>
      </div>
    </div>

    <ol v-if="steps.length" class="k-first-run__journey" :aria-label="journeyLabel">
      <li
        v-for="(step, index) in steps"
        :key="`${index}-${step.label}`"
        :class="['k-first-run__step', {
          'is-complete': index < boundedCurrentStep,
          'is-current': index === boundedCurrentStep,
        }]"
        :aria-current="index === boundedCurrentStep ? 'step' : undefined"
      >
        <span class="k-first-run__marker" aria-hidden="true">
          <Check v-if="index < boundedCurrentStep" :stroke-width="2" />
          <CircleDot v-else-if="index === boundedCurrentStep" :stroke-width="1.75" />
          <span v-else>{{ index + 1 }}</span>
        </span>
        <span class="k-first-run__step-copy">
          <span class="k-first-run__step-status">
            {{ index < boundedCurrentStep ? 'Completed step' : index === boundedCurrentStep ? 'Current step' : 'Upcoming step' }}:
          </span>
          <strong>{{ step.label }}</strong>
          <small>{{ step.description }}</small>
        </span>
      </li>
    </ol>
  </section>
</template>
