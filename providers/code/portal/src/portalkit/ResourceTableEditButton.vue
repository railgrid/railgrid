<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
     under providers/*/portal/src/portalkit/; edit here and run
     `make sync-portalkit`.

     Compact, accessible edit trigger for ResourceTable action cells. -->
<script setup lang="ts">
import { Pencil } from 'lucide-vue-next'
import editButtonStyles from './ResourceTableEditButton.css?raw'

// Standalone provider portals register one IIFE main.js and do not load Vite's
// extracted SFC CSS asset. Inject this small canonical recipe explicitly so the
// control renders identically in the real provider host, not only in Vite dev.
const STYLE_ID = 'faros-portalkit-resource-table-edit-css'
if (typeof document !== 'undefined') {
  let style = document.getElementById(STYLE_ID) as HTMLStyleElement | null
  if (!style) {
    style = document.createElement('style')
    style.id = STYLE_ID
    document.head.appendChild(style)
  }
  if (style.textContent !== editButtonStyles) style.textContent = editButtonStyles
}

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
    class="pk-resource-edit"
    type="button"
    :title="label"
    :aria-label="label"
    :disabled="disabled"
    @click.stop="emit('click', $event)"
  >
    <Pencil class="pk-resource-edit-icon" :stroke-width="1.75" aria-hidden="true" />
  </button>
</template>
