<!--
  CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
  under providers/*/portal/src/portalkit/; edit here and run
  `make sync-portalkit`.

  ResourceTimestamp keeps the source value in native time semantics while
  presenting a compact relative label and an accessible exact date disclosure.
-->
<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { ensureRailgridUIStyles } from '../portalkit/styles'
import ResourceTableActionTooltip from './ResourceTableActionTooltip.vue'
import { useReactiveTimestampNow } from './timestampClock'
import { formatFullTimestamp, formatRelativeTimestamp, timestampSortValue } from './timestamp'

ensureRailgridUIStyles()

const props = withDefaults(defineProps<{
  /** Source timestamp, normally an RFC3339/ISO string from the resource API. */
  value?: string | null
  /** Caller-owned description, such as "Release created". */
  label?: string
  /** Locale passed to Intl.RelativeTimeFormat and Intl.DateTimeFormat. */
  locale?: string
  /** Optional IANA time zone passed to Intl.DateTimeFormat. */
  timeZone?: string
}>(), {
  value: null,
  label: 'Timestamp',
})

const now = useReactiveTimestampNow()
const button = ref<HTMLButtonElement | null>(null)
const expanded = ref(false)
const timestampMs = computed(() => timestampSortValue(props.value))
const visible = computed(() => timestampMs.value !== null)
const dateTime = computed(() => timestampMs.value === null ? undefined : new Date(timestampMs.value).toISOString())
const relative = computed(() => formatRelativeTimestamp(props.value, { locale: props.locale, nowMs: now.value }))
const full = computed(() => formatFullTimestamp(props.value, { locale: props.locale, timeZone: props.timeZone }))
const accessibleSourceLabel = computed(() => props.label.trim() || 'Timestamp')
const exactLabel = computed(() => `${accessibleSourceLabel.value}: ${full.value}`)
const accessibleLabel = computed(() => `${accessibleSourceLabel.value}: ${relative.value} — ${full.value}`)
const unavailableLabel = computed(() => `${accessibleSourceLabel.value}: unavailable`)
const displayValue = computed(() => expanded.value ? full.value : relative.value)

watch(() => props.value, () => { expanded.value = false })

function toggle(): void {
  expanded.value = !expanded.value
}
</script>

<template>
  <span v-if="visible" class="k-resource-timestamp">
    <button
      ref="button"
      class="k-resource-timestamp__button"
      type="button"
      :aria-label="accessibleLabel"
      :aria-pressed="expanded"
      @click.stop="toggle"
    >
      <time class="k-resource-timestamp__time k-cell-mono" :datetime="dateTime">{{ displayValue }}</time>
    </button>
    <ResourceTableActionTooltip :anchor="button" :label="exactLabel" />
  </span>
  <span v-else class="k-resource-timestamp__missing" role="img" :aria-label="unavailableLabel">—</span>
</template>
