<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { Check, ChevronDown, Copy } from 'lucide-vue-next'
import { sanitizeTechnicalDiagnostic } from '../failure-presentation'

const props = defineProps<{ diagnostic?: string; compact?: boolean; copyable?: boolean; embedded?: boolean }>()
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
  <component
    :is="embedded ? 'div' : 'details'"
    v-if="safeDiagnostic"
    class="k-resource-technical"
    :class="{ 'k-resource-technical--compact': compact, 'agents-technical--embedded': embedded }"
  >
    <summary v-if="!embedded" class="k-resource-technical__summary">
      <span class="k-resource-technical__summary-label">Technical details</span>
      <ChevronDown class="k-resource-technical__chevron" :size="14" :stroke-width="1.75" aria-hidden="true" />
    </summary>
    <div class="k-resource-technical__body">
      <template v-if="copyable">
        <button
          class="agents-technical__copy"
          :class="embedded ? 'k-ai-message-metadata__button' : 'k-icon-action'"
          type="button"
          aria-label="Copy details"
          :data-k-tip="copyState === 'copied' ? 'Copied' : 'Copy details'"
          :aria-busy="copyState === 'copying' || undefined"
          :disabled="copyState === 'copying'"
          @click="copyDetails"
        >
          <Check v-if="copyState === 'copied'" :stroke-width="1.75" aria-hidden="true" />
          <Copy v-else :stroke-width="1.75" aria-hidden="true" />
          <span v-if="embedded">{{ copyState === 'copied' ? 'Copied' : 'Copy' }}</span>
        </button>
        <p class="agents-technical__feedback" role="status">
          {{ copyState === 'copied' ? 'Technical details copied.' : copyState === 'failed' ? (embedded ? 'Could not copy details. Select the diagnostic text to copy it.' : 'Could not copy details. Select the text below to copy it.') : '' }}
        </p>
      </template>
      <pre class="k-resource-technical__pre" tabindex="0" aria-label="Technical error details">{{ safeDiagnostic }}</pre>
    </div>
  </component>
</template>

<style scoped>
.agents-technical__copy { align-self: flex-end; }
.agents-technical__feedback { margin: 0; color: var(--color-text-secondary); font-size: 12px; }
.agents-technical__feedback:empty { display: none; }

.agents-technical--embedded .k-resource-technical__body {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 4px 12px;
  align-items: start;
  padding: 8px 12px;
  border: 1px solid var(--color-border-subtle);
  border-radius: 8px;
  background: var(--color-surface-overlay);
}

.agents-technical--embedded .k-resource-technical__pre {
  grid-area: 1 / 1;
  padding: 4px 0;
  background: transparent;
  color: var(--color-text-primary);
  font-size: 12px;
}

.agents-technical--embedded .agents-technical__copy {
  grid-area: 1 / 2;
  align-self: start;
  gap: 5px;
  min-height: 28px;
  color: var(--color-text-secondary);
  font-size: 12px;
}

.agents-technical--embedded .agents-technical__copy svg { width: 14px; height: 14px; }
.agents-technical--embedded .agents-technical__feedback { grid-column: 1 / -1; }
.agents-technical--embedded .agents-technical__feedback:not(:empty) { grid-row: 2; }

@media (hover: none), (any-pointer: coarse) {
  .agents-technical--embedded .agents-technical__copy { min-height: 44px; }
}
</style>
