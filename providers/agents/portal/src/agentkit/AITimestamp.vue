<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AITimestamp owns only timestamp presentation. Providers retain the
  authoritative timestamp and decide whether a message should expose one.
  The semantic <time> is preserved while the relative label can be expanded
  to the full locale value with hover, focus, or click.
-->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { ensureAgentUIStyles } from '../agentkit/styles'
import { useReactiveNow } from './clock'
import { formatFullTime, formatRelativeTime, isValidTimestamp } from './timestamp'

const props = withDefaults(defineProps<{
  /** Provider-owned timestamp value, normally an RFC3339/ISO string. */
  value?: string | null
  /** RelativeTimeFormat mode; Studio uses `always` for message timestamps. */
  numeric?: Intl.RelativeTimeFormatNumeric
  /** Optional caller label; the full timestamp remains the fallback. */
  ariaLabel?: string
}>(), {
  value: null,
  numeric: 'always',
  ariaLabel: '',
})

const expanded = ref(false)
const now = useReactiveNow()
const relative = computed(() => formatRelativeTime(props.value, props.numeric, now.value))
const full = computed(() => formatFullTime(props.value))
const visible = computed(() => isValidTimestamp(props.value) && Boolean(relative.value && full.value))
const accessibleLabel = computed(() => props.ariaLabel.trim() || full.value)

function toggle(): void {
  expanded.value = !expanded.value
}

ensureAgentUIStyles()
</script>

<template>
  <span v-if="visible" class="k-ai-timestamp">
    <button
      type="button"
      class="k-ai-timestamp__button"
      :title="full"
      :aria-label="accessibleLabel"
      @click="toggle"
    >
      <time class="k-ai-timestamp__time" :datetime="value || undefined">{{ expanded ? full : relative }}</time>
    </button>
    <span v-if="!expanded" class="k-ai-timestamp__tooltip" role="tooltip" aria-hidden="true">{{ full }}</span>
  </span>
</template>
