<!--
  CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
  under providers/*/portal/src/portalkit/; edit here and run
  `make sync-portalkit`.
-->
<script setup lang="ts">
import { Info } from 'lucide-vue-next'
import { useId } from 'vue'
import { ensureFarosUIStyles } from '../portalkit/styles'

ensureFarosUIStyles()

export interface CreateGuidanceValue {
  label: string
  value: string
  technical?: boolean
}

withDefaults(defineProps<{
  title: string
  description: string
  prerequisites?: string[]
  values?: CreateGuidanceValue[]
  nextSteps?: string[]
  valuesHeading?: string
}>(), {
  prerequisites: () => [],
  values: () => [],
  nextSteps: () => [],
  valuesHeading: 'What Faros will create',
})

const id = useId().replace(/[^a-zA-Z0-9_-]/g, '-')
const titleID = `k-create-guidance-${id}-title`
const prerequisiteID = `k-create-guidance-${id}-prerequisites`
const valuesID = `k-create-guidance-${id}-values`
const nextStepsID = `k-create-guidance-${id}-next-steps`
</script>

<template>
  <aside class="k-create-guidance" :aria-labelledby="titleID">
    <div class="k-create-guidance__heading">
      <Info :size="16" :stroke-width="1.75" aria-hidden="true" />
      <h3 :id="titleID">{{ title }}</h3>
    </div>
    <p class="k-create-guidance__description">{{ description }}</p>

    <section v-if="prerequisites.length" class="k-create-guidance__section" :aria-labelledby="prerequisiteID">
      <h4 :id="prerequisiteID">Prerequisites</h4>
      <ul>
        <li v-for="prerequisite in prerequisites" :key="prerequisite">{{ prerequisite }}</li>
      </ul>
    </section>

    <section v-if="values.length" class="k-create-guidance__section" :aria-labelledby="valuesID">
      <h4 :id="valuesID">{{ valuesHeading }}</h4>
      <dl class="k-create-guidance__values">
        <template v-for="item in values" :key="item.label">
          <dt>{{ item.label }}</dt>
          <dd><code v-if="item.technical">{{ item.value }}</code><span v-else>{{ item.value }}</span></dd>
        </template>
      </dl>
    </section>

    <section v-if="nextSteps.length" class="k-create-guidance__section" :aria-labelledby="nextStepsID">
      <h4 :id="nextStepsID">Next steps</h4>
      <ol>
        <li v-for="step in nextSteps" :key="step">{{ step }}</li>
      </ol>
    </section>
  </aside>
</template>
