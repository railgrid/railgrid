<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIWorkbenchPane gives workbench tabs one neutral canvas and document header.
  Providers own the pane content, controls, and scroll details inside the body.
-->
<script setup lang="ts">
import { ensureAgentUIStyles } from '../agentkit/styles'

withDefaults(defineProps<{
  variant?: 'document' | 'tool'
  title?: string
  description?: string
}>(), {
  variant: 'document',
  title: undefined,
  description: undefined,
})

ensureAgentUIStyles()
</script>

<template>
  <section
    class="k-ai-workbench-canvas k-ai-workbench-pane"
    :class="`k-ai-workbench-pane--${variant}`"
  >
    <header
      v-if="variant === 'document' && (title || description || $slots.actions)"
      class="k-ai-workbench-pane__header"
    >
      <div class="k-ai-workbench-pane__heading">
        <h2 v-if="title" class="k-ai-workbench-pane__title">{{ title }}</h2>
        <p v-if="description" class="k-ai-workbench-pane__description">{{ description }}</p>
      </div>
      <div v-if="$slots.actions" class="k-ai-workbench-pane__actions">
        <slot name="actions" />
      </div>
    </header>
    <div class="k-ai-workbench-pane__body">
      <slot />
    </div>
  </section>
</template>
