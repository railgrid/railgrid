<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Check, ChevronDown, Copy } from 'lucide-vue-next'
import { sanitizeTechnicalDiagnostic } from '../failure-presentation'

const props = defineProps<{ diagnostic?: string; compact?: boolean; copyable?: boolean }>()
const safeDiagnostic = computed(() => sanitizeTechnicalDiagnostic(props.diagnostic))
const copyState = ref<'idle' | 'copying' | 'copied' | 'failed'>('idle')
watch(safeDiagnostic, () => { copyState.value = 'idle' })

async function copyDetails(): Promise<void> {
  const diagnostic = safeDiagnostic.value
  copyState.value = 'copying'
  try {
    await navigator.clipboard.writeText(diagnostic)
    if (diagnostic === safeDiagnostic.value) copyState.value = 'copied'
  } catch {
    if (diagnostic === safeDiagnostic.value) copyState.value = 'failed'
  }
}
</script>

<template>
  <details
    v-if="safeDiagnostic"
    class="k-resource-technical"
    :class="{ 'k-resource-technical--compact': compact }"
  >
    <summary class="k-resource-technical__summary">
      <span class="k-resource-technical__summary-label">Technical details</span>
      <ChevronDown class="k-resource-technical__chevron" :size="14" :stroke-width="1.75" aria-hidden="true" />
    </summary>
    <div class="k-resource-technical__body">
      <template v-if="copyable">
        <button
          class="k-icon-action agents-technical__copy"
          type="button"
          aria-label="Copy details"
          :data-k-tip="copyState === 'copied' ? 'Copied' : 'Copy details'"
          :aria-busy="copyState === 'copying' || undefined"
          :disabled="copyState === 'copying'"
          @click="copyDetails"
        >
          <Check v-if="copyState === 'copied'" :stroke-width="1.75" aria-hidden="true" />
          <Copy v-else :stroke-width="1.75" aria-hidden="true" />
        </button>
        <p class="agents-technical__feedback" role="status">
          {{ copyState === 'copied' ? 'Technical details copied.' : copyState === 'failed' ? 'Could not copy details. Select the text below to copy it.' : '' }}
        </p>
      </template>
      <pre class="k-resource-technical__pre" tabindex="0" aria-label="Technical error details">{{ safeDiagnostic }}</pre>
    </div>
  </details>
</template>

<style scoped>
.agents-technical__copy { align-self: flex-end; }
.agents-technical__feedback { margin: 0; color: var(--color-text-secondary); font-size: 12px; }
.agents-technical__feedback:empty { display: none; }
</style>
