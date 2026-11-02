<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
     under providers/*/portal/src/portalkit/; edit here and run
     `make sync-portalkit`.

     Persistent disclosure toggle for a ResourceTable row whose detail renders
     in the table's `after-row` slot. Unlike the hover-revealed row actions it
     stays visible at rest, because a collapsed row gives no other hint that
     there is more to see. The caller owns the expanded state and the detail
     row; this button only names and announces it. -->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { ChevronRight } from 'lucide-vue-next'
import ResourceTableActionTooltip from './ResourceTableActionTooltip.vue'
import { ensureRailgridUIStyles } from '../portalkit/styles'

// Standalone provider portals load the exact canonical recipe through the
// shared helper; the host portal already imports the same railgrid-ui.css file.
ensureRailgridUIStyles()
const button = ref<HTMLButtonElement | null>(null)

const props = withDefaults(defineProps<{
  /** Whether the row's detail is shown. */
  expanded: boolean
  /** Accessible resource-specific action while collapsed, for example "Show limits for free". */
  label: string
  /** Accessible action while expanded. Falls back to the collapsed label. */
  expandedLabel?: string
  /** The detail row's id, announced through aria-controls while it is shown. */
  controls?: string
  disabled?: boolean
}>(), {
  expandedLabel: '',
  controls: '',
  disabled: false,
})

const accessibleLabel = computed(() => (props.expanded && props.expandedLabel) || props.label)

const emit = defineEmits<{
  click: [event: MouseEvent]
}>()
</script>

<template>
  <button
    ref="button"
    class="k-table-action k-table-action--expand"
    type="button"
    :aria-label="accessibleLabel"
    :aria-expanded="expanded ? 'true' : 'false'"
    :aria-controls="expanded && controls ? controls : undefined"
    :disabled="disabled"
    @click.stop="emit('click', $event)"
  >
    <ChevronRight class="k-table-action__icon k-table-action__icon--expand" :stroke-width="1.75" aria-hidden="true" />
    <ResourceTableActionTooltip :anchor="button" :label="accessibleLabel" />
  </button>
</template>
