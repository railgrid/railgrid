<script setup lang="ts">
// Renders one resolved view value with a type-appropriate presentation:
//   text  → plain span
//   link  → clickable anchor (new tab) with an external-link glyph
//   code  → monospace pill with a copy button
//   badge → muted label
// Driven entirely by a ResolvedValue from view.ts, so list cells and detail
// fields render identically.
import { onUnmounted, ref, watch } from 'vue'
import { Check, Copy, ExternalLink } from 'lucide-vue-next'
import type { ResolvedValue } from '../view'

const props = withDefaults(defineProps<{ value: ResolvedValue; interactive?: boolean }>(), {
  interactive: true,
})

const copyState = ref<'idle' | 'copied' | 'error'>('idle')
let copyTimer: ReturnType<typeof setTimeout> | undefined
let copyGeneration = 0
function resetCopy() {
  copyGeneration += 1
  clearTimeout(copyTimer)
  copyState.value = 'idle'
}
watch(() => [props.value.text, props.value.type, props.interactive], resetCopy)
onUnmounted(resetCopy)
async function copy() {
  resetCopy()
  const generation = copyGeneration
  try {
    await navigator.clipboard.writeText(props.value.text)
    if (generation !== copyGeneration) return
    copyState.value = 'copied'
    copyTimer = setTimeout(() => { copyState.value = 'idle' }, 2000)
  } catch {
    if (generation === copyGeneration) copyState.value = 'error'
  }
}
</script>

<template>
  <span v-if="value.empty" class="text-[12px] text-text-muted/50">—</span>

  <span v-else-if="!interactive" class="text-[12px] text-text-secondary">{{ value.text }}</span>

  <a
    v-else-if="value.type === 'link' && value.href"
    :href="value.href"
    target="_blank"
    rel="noopener noreferrer"
    class="view-value-link inline-flex items-center gap-1 text-[12px] font-medium text-accent transition-colors hover:underline"
    @click.stop
  >
    <span class="view-value-text">{{ value.text }}</span>
    <ExternalLink :size="12" :stroke-width="1.75" class="opacity-70" aria-hidden="true" />
  </a>

  <span
    v-else-if="value.type === 'code'"
    class="view-value-code-wrap"
  >
    <span class="view-value-code k-badge k-badge--muted font-mono text-[11px]">
      <span class="view-value-text">{{ value.text }}</span>
      <button
      type="button"
      class="k-icon-action view-value-copy"
      :aria-label="copyState === 'copied' ? 'Value copied' : `Copy ${value.text}`"
      :data-k-tip="copyState === 'copied' ? 'Copied' : 'Copy value'"
      @click.stop="copy"
    >
      <Copy v-if="copyState !== 'copied'" :size="12" :stroke-width="1.75" aria-hidden="true" />
      <Check v-else :size="12" :stroke-width="1.75" class="text-success" aria-hidden="true" />
    </button>
    </span>
    <span class="view-value-copy-feedback" :class="{ 'view-value-copy-feedback--error': copyState === 'error' }" role="status" aria-live="polite">{{ copyState === 'copied' ? 'Copied' : copyState === 'error' ? 'Could not copy. Select the value to copy it.' : '' }}</span>
  </span>

  <span
    v-else-if="value.type === 'badge'"
    class="k-badge k-badge--muted k-badge--label"
  >
    {{ value.text }}
  </span>

  <span v-else class="text-[12px] text-text-secondary">{{ value.text }}</span>
</template>
