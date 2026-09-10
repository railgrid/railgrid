<!-- Canonical usage section; providers own their reporting data and read states. -->
<script setup lang="ts">
import { ensureFarosUIStyles } from './styles'
ensureFarosUIStyles()
defineProps<{ provider: string; available?: boolean }>()
</script>
<template>
  <section class="k-model-usage" aria-label="Usage and cost">
    <header class="k-model-usage__heading"><div><h3>Usage &amp; cost</h3><p>{{ provider }} usage in this workspace.</p></div><slot name="controls" /></header>
    <slot v-if="available" />
    <div v-else class="k-model-usage__unavailable"><h4>Usage reporting isn’t available yet</h4><p>Spend will appear here when reporting is available.</p></div>
  </section>
</template>
