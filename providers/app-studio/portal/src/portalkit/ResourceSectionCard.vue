<!--
  CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
  under providers/*/portal/src/portalkit/; edit here and run
  `make sync-portalkit`.

  ResourceSectionCard owns the shared bordered-card geometry for a resource
  detail section. Callers own the section title, facts, actions, and body.
-->
<script setup lang="ts">
import { computed } from 'vue'
import { ensureFarosUIStyles } from '../portalkit/styles'

ensureFarosUIStyles()

const props = withDefaults(defineProps<{
  id?: string
  headingId?: string
  eyebrow?: string
  title?: string
  description?: string
}>(), {
  id: '',
  headingId: '',
  eyebrow: '',
  title: '',
  description: '',
})

const headingId = computed(() => props.headingId || (props.id ? `${props.id}-heading` : ''))
</script>

<template>
  <section
    :id="props.id || undefined"
    class="k-resource-section-card"
    :aria-labelledby="props.title && headingId ? headingId : undefined"
    data-k-resource-section-card
  >
    <header
      v-if="props.eyebrow || props.title || props.description || $slots.actions"
      class="k-resource-section-card__header"
    >
      <div v-if="props.eyebrow || props.title || props.description" class="k-resource-section-card__heading">
        <p v-if="props.eyebrow" class="k-resource-section-card__eyebrow">{{ props.eyebrow }}</p>
        <h2 v-if="props.title" :id="headingId || undefined" class="k-resource-section-card__title">{{ props.title }}</h2>
        <p v-if="props.description" class="k-resource-section-card__description">{{ props.description }}</p>
      </div>
      <div v-if="$slots.actions" class="k-resource-section-card__actions">
        <slot name="actions" />
      </div>
    </header>
    <div class="k-resource-section-card__body">
      <slot />
    </div>
  </section>
</template>
