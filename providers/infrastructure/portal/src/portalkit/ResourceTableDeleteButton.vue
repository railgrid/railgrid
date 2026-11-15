<!-- CANONICAL SOURCE — edit here, then run make sync-portalkit. -->
<script setup lang="ts">
import { Trash2 } from 'lucide-vue-next'
import ResourceTableActionButton from './ResourceTableActionButton.vue'

withDefaults(defineProps<{
  label: string
  busyLabel?: string
  busy?: boolean
  disabled?: boolean
}>(), { busy: false, disabled: false })

const emit = defineEmits<{ click: [event: MouseEvent] }>()
</script>

<template>
  <ResourceTableActionButton class="k-table-action--delete" :icon="Trash2" tone="danger" :label="label" :busy-label="busyLabel" :busy="busy" :disabled="disabled" @click="emit('click', $event)" />
</template>
