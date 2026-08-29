<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
     under providers/*/portal/src/portalkit/; edit here and run
     `make sync-portalkit`.

     Compact, accessible edit trigger for ResourceTable action cells. -->
<script setup lang="ts">
import { Pencil } from 'lucide-vue-next'
import { ensureFarosUIStyles } from '../portalkit/styles'

// Standalone provider portals load the exact canonical recipe through the
// shared helper; the host portal already imports the same faros-ui.css file.
ensureFarosUIStyles()

withDefaults(defineProps<{
  /** Accessible resource-specific action, for example "Edit table orders". */
  label: string
  disabled?: boolean
}>(), {
  disabled: false,
})

const emit = defineEmits<{
  click: [event: MouseEvent]
}>()
</script>

<template>
  <button
    class="k-table-action k-table-action--edit"
    type="button"
    :data-k-tip="label"
    :aria-label="label"
    :disabled="disabled"
    @click.stop="emit('click', $event)"
  >
    <Pencil class="k-table-action__icon" :stroke-width="1.75" aria-hidden="true" />
  </button>
</template>
