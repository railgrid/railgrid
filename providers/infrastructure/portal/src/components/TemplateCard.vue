<script setup lang="ts">
import type { Template } from '../types'
defineProps<{ template: Template }>()
defineEmits<{ (e: 'select', name: string): void }>()
</script>

<template>
  <button class="template-card" @click="$emit('select', template.name)">
    <div class="template-card-head">
      <div class="template-card-title">{{ template.displayName || template.name }}</div>
      <span v-if="template.cloud" class="cloud-pill">{{ template.cloud }}</span>
    </div>
    <p class="template-card-desc">{{ template.description }}</p>
    <div class="template-card-foot">
      <span class="kind">{{ template.kind }}</span>
      <span v-if="template.version" class="version">v{{ template.version }}</span>
    </div>
  </button>
</template>
