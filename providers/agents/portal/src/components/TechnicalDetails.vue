<script setup lang="ts">
import { computed } from 'vue'
import { ChevronDown } from 'lucide-vue-next'
import { sanitizeTechnicalDiagnostic } from '../failure-presentation'

const props = defineProps<{ diagnostic?: string }>()
const safeDiagnostic = computed(() => sanitizeTechnicalDiagnostic(props.diagnostic))
</script>

<template>
  <details v-if="safeDiagnostic" class="k-resource-technical">
    <summary class="k-resource-technical__summary">
      <span class="k-resource-technical__summary-label">Technical details</span>
      <ChevronDown class="k-resource-technical__chevron" :size="14" :stroke-width="1.75" aria-hidden="true" />
    </summary>
    <div class="k-resource-technical__body"><pre class="k-resource-technical__pre" tabindex="0" aria-label="Technical error details">{{ safeDiagnostic }}</pre></div>
  </details>
</template>
