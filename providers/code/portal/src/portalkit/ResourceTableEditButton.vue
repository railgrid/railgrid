<!-- CANONICAL SOURCE — edit here, then run make sync-portalkit. -->
<script setup lang="ts">
import { Pencil } from 'lucide-vue-next'
import ResourceTableActionButton from './ResourceTableActionButton.vue'

withDefaults(defineProps<{ label: string; disabled?: boolean }>(), { disabled: false })
const emit = defineEmits<{ click: [event: MouseEvent] }>()
</script>

<template>
  <ResourceTableActionButton class="k-table-action--edit" :icon="Pencil" :label="label" :disabled="disabled" @click="emit('click', $event)" />
</template>
