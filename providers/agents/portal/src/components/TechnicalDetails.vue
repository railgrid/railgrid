<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { Check, ChevronDown, Copy } from 'lucide-vue-next'
import { sanitizeTechnicalDiagnostic } from '../failure-presentation'

const props = defineProps<{ diagnostic?: string; compact?: boolean; copyable?: boolean }>()
const safeDiagnostic = computed(() => sanitizeTechnicalDiagnostic(props.diagnostic))
const copyState = ref<'idle' | 'copying' | 'copied' | 'failed'>('idle')
let copyGeneration = 0
let copyTimer: ReturnType<typeof setTimeout> | undefined
function resetCopy(): void {
  copyGeneration += 1
  clearTimeout(copyTimer)
  copyTimer = undefined
  copyState.value = 'idle'
}
watch(safeDiagnostic, resetCopy, { flush: 'sync' })
onBeforeUnmount(resetCopy)

async function copyDetails(): Promise<void> {
  if (copyState.value === 'copying') return
  const diagnostic = safeDiagnostic.value
  resetCopy()
  const generation = copyGeneration
  copyState.value = 'copying'
  try {
    await navigator.clipboard.writeText(diagnostic)
    if (generation !== copyGeneration) return
    copyState.value = 'copied'
    copyTimer = setTimeout(() => { copyState.value = 'idle'; copyTimer = undefined }, 2000)
  } catch {
    if (generation === copyGeneration) copyState.value = 'failed'
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
          :aria-disabled="copyState === 'copying' || undefined"
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
