<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Edit here, then run
     `make sync-portalkit` to update the self-contained portal copies. -->
<script setup lang="ts">
import { Check, Copy } from 'lucide-vue-next'
import { computed, ref } from 'vue'
import { ensureRailgridUIStyles } from '../portalkit/styles'
import { useClipboardFeedback } from './useClipboardFeedback'

ensureRailgridUIStyles()

const props = defineProps<{
  value: string
  label: string
  tooltip?: string
  selectAll?: boolean
  scope?: unknown
}>()

const button = ref<HTMLButtonElement | null>(null)
defineExpose({ focus: (options?: FocusOptions) => button.value?.focus(options) })

const { copy, copiedField, pendingField, errors } = useClipboardFeedback(
  () => [props.value, props.label, props.scope],
)
const copyError = computed(() => errors.value.code)
</script>

<template>
  <span class="k-copy-value">
    <span class="k-copy-value__line">
      <code class="k-copy-value__text" :class="{ 'k-copy-value__text--select-all': selectAll }">{{ value }}</code>
      <button
        ref="button"
        type="button"
        class="k-icon-action k-icon-action--quiet"
        :aria-label="label"
        :aria-disabled="pendingField !== null || undefined"
        :aria-busy="pendingField !== null || undefined"
        :data-k-tip="copiedField ? 'Copied' : (tooltip || label)"
        @click.stop="copy(value, 'code', 'Could not copy. Select the value to copy it.')"
      >
        <Check v-if="copiedField" class="k-copy-value__copied" :stroke-width="1.75" aria-hidden="true" />
        <Copy v-else :stroke-width="1.75" aria-hidden="true" />
      </button>
    </span>
    <span class="k-copy-value__status" :class="{ 'k-copy-value__status--error': copyError }" role="status" aria-atomic="true">{{ copyError || (copiedField ? 'Copied' : '') }}</span>
  </span>
</template>
