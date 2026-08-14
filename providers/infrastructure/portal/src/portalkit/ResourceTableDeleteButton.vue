<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Do not edit vendored copies
     under providers/*/portal/src/portalkit/; edit here and run
     `make sync-portalkit`.

     Compact, accessible delete trigger for ResourceTable action cells. The
     destructive confirmation remains the caller's responsibility via
     confirmDialog({ danger: true }). -->
<script setup lang="ts">
import { computed } from 'vue'
import { Loader2, Trash2 } from 'lucide-vue-next'
import deleteButtonStyles from './ResourceTableDeleteButton.css?raw'

// Standalone provider portals register one IIFE main.js and do not load Vite's
// extracted SFC CSS asset. Inject this small canonical recipe explicitly so the
// control renders identically in the real provider host, not only in Vite dev.
const STYLE_ID = 'faros-portalkit-resource-table-delete-css'
if (typeof document !== 'undefined') {
  let style = document.getElementById(STYLE_ID) as HTMLStyleElement | null
  if (!style) {
    style = document.createElement('style')
    style.id = STYLE_ID
    document.head.appendChild(style)
  }
  if (style.textContent !== deleteButtonStyles) style.textContent = deleteButtonStyles
}

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
    class="pk-resource-delete"
    :class="{ 'is-busy': busy }"
    type="button"
    :title="accessibleLabel"
    :aria-label="accessibleLabel"
    :aria-busy="busy || undefined"
    :disabled="disabled || busy"
    @click.stop="emit('click', $event)"
  >
    <Loader2 v-if="busy" class="pk-resource-delete-icon is-spinning" :stroke-width="1.75" aria-hidden="true" />
    <Trash2 v-else class="pk-resource-delete-icon" :stroke-width="1.75" aria-hidden="true" />
  </button>
</template>
