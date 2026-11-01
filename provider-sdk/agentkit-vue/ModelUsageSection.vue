<!-- Canonical usage section; providers own their reporting data and read states. -->
<script setup lang="ts">
import { ensureAgentUIStyles } from '../agentkit/styles'
ensureAgentUIStyles()
defineProps<{ provider: string; available?: boolean }>()
</script>
<template>
  <section class="k-resource-page k-resource-page--collection k-model-usage" aria-label="Usage and cost">
    <header class="k-resource-page__header k-model-usage__heading"><div class="k-resource-page__heading"><h3 class="k-resource-page__title">Usage &amp; cost</h3><p class="k-resource-page__subtitle">{{ provider }} usage in this workspace.</p></div><slot name="controls" /></header>
    <div class="k-model-usage__body">
    <slot v-if="available" />
    <div v-else class="k-model-usage__unavailable"><h4>Usage reporting isn’t available yet</h4><p>Spend will appear here when reporting is available.</p></div>
    </div>
  </section>
</template>
