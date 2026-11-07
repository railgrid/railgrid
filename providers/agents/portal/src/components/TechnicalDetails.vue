<script setup lang="ts">
import { computed, ref, useId, watch } from 'vue'
import { Check, ChevronDown, Copy } from 'lucide-vue-next'
import { sanitizeTechnicalDiagnostic } from '../failure-presentation'

const props = defineProps<{ diagnostic?: string; compact?: boolean; copyable?: boolean; inline?: boolean }>()
const safeDiagnostic = computed(() => sanitizeTechnicalDiagnostic(props.diagnostic))
const expanded = ref(false)
const bodyId = useId()
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
    :is="inline ? 'div' : 'details'"
    v-if="safeDiagnostic || (inline && $slots.actions)"
    class="k-resource-technical"
    :class="{ 'k-resource-technical--compact': compact, 'agents-technical--inline': inline }"
  >
    <div v-if="inline" class="agents-technical__actions">
      <slot name="actions" />
      <button
        v-if="safeDiagnostic"
        class="k-ai-message-metadata__button agents-technical__toggle"
        type="button"
        :aria-expanded="expanded"
        :aria-controls="bodyId"
        @click="expanded = !expanded"
      >
        Details
        <ChevronDown :size="14" :stroke-width="1.75" aria-hidden="true" />
      </button>
    </div>
    <summary v-else class="k-resource-technical__summary">
      <span class="k-resource-technical__summary-label">Technical details</span>
      <ChevronDown class="k-resource-technical__chevron" :size="14" :stroke-width="1.75" aria-hidden="true" />
    </summary>
    <div v-if="safeDiagnostic" v-show="!inline || expanded" :id="bodyId" class="k-resource-technical__body">
      <template v-if="copyable">
        <button
          class="agents-technical__copy"
          :class="inline ? 'k-ai-message-metadata__button' : 'k-icon-action'"
          type="button"
          aria-label="Copy details"
          :data-k-tip="copyState === 'copied' ? 'Copied' : 'Copy details'"
          :aria-busy="copyState === 'copying' || undefined"
          :disabled="copyState === 'copying'"
          @click="copyDetails"
        >
          <Check v-if="copyState === 'copied'" :stroke-width="1.75" aria-hidden="true" />
          <Copy v-else :stroke-width="1.75" aria-hidden="true" />
          <span v-if="inline">{{ copyState === 'copied' ? 'Copied' : 'Copy' }}</span>
        </button>
        <p class="agents-technical__feedback" role="status">
          {{ copyState === 'copied' ? 'Technical details copied.' : copyState === 'failed' ? (inline ? 'Could not copy details. Select the diagnostic text to copy it.' : 'Could not copy details. Select the text below to copy it.') : '' }}
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

.agents-technical__actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  column-gap: 16px;
  color: var(--color-text-secondary);
  font-size: 12px;
}

.agents-technical__toggle,
.agents-technical--inline .agents-technical__copy {
  gap: 5px;
  padding-inline: 0;
  min-height: 28px;
}

.agents-technical__toggle[aria-expanded='true'] svg { transform: rotate(180deg); }

.agents-technical--inline .k-resource-technical__body {
  display: grid;
  grid-template-columns: minmax(0, 1fr) auto;
  gap: 4px 12px;
  align-items: start;
  padding: 8px 12px;
  border: 1px solid var(--color-border-subtle);
  border-radius: 8px;
  background: var(--color-surface-overlay);
}

.agents-technical--inline .k-resource-technical__pre {
  grid-area: 1 / 1;
  padding: 4px 0;
  background: transparent;
  color: var(--color-text-primary);
  font-size: 12px;
}

.agents-technical--inline .agents-technical__copy {
  grid-area: 1 / 2;
  align-self: start;
  color: var(--color-text-secondary);
  font-size: 12px;
}

.agents-technical--inline .agents-technical__copy svg { width: 14px; height: 14px; }
.agents-technical--inline .agents-technical__feedback { grid-column: 1 / -1; }
.agents-technical--inline .agents-technical__feedback:not(:empty) { grid-row: 2; }

@media (hover: none), (any-pointer: coarse) {
  .agents-technical__toggle,
  .agents-technical--inline .agents-technical__copy { min-height: 44px; }
}
</style>
