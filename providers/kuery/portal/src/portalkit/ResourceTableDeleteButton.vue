<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
     under providers/*/portal/src/portalkit/; edit here and run
     `make sync-portalkit`.

     Compact, accessible delete trigger for ResourceTable action cells. The
     destructive confirmation remains the caller's responsibility via
     confirmDialog({ danger: true }). -->
<script setup lang="ts">
import { computed } from 'vue'
import { Loader2, Trash2 } from 'lucide-vue-next'
import { ensureFarosUIStyles } from '../portalkit/styles'

// Standalone provider portals load the exact canonical recipe through the
// shared helper; the host portal already imports the same faros-ui.css file.
ensureFarosUIStyles()

const props = withDefaults(defineProps<{
  /** Accessible resource-specific action, for example "Delete connection". */
  label: string
  /** Accessible in-flight action. Defaults to "Deleting…". */
  busyLabel?: string
  busy?: boolean
  disabled?: boolean
}>(), {
  busyLabel: 'Deleting…',
  busy: false,
  disabled: false,
})

const accessibleLabel = computed(() => props.busy ? props.busyLabel : props.label)

const emit = defineEmits<{
  click: [event: MouseEvent]
}>()
</script>

<template>
  <button
    class="k-table-action k-table-action--delete"
    :class="{ 'k-table-action--busy': busy }"
    type="button"
    :data-k-tip="accessibleLabel"
    :aria-label="accessibleLabel"
    :aria-busy="busy || undefined"
    :disabled="disabled || busy"
    @click.stop="emit('click', $event)"
  >
    <Loader2 v-if="busy" class="k-table-action__icon k-table-action__icon--spinning" :stroke-width="1.75" aria-hidden="true" />
    <Trash2 v-else class="k-table-action__icon" :stroke-width="1.75" aria-hidden="true" />
  </button>
</template>
