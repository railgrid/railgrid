<script setup lang="ts">
import { computed } from 'vue'
import type { Template } from '../types'

const props = defineProps<{ template: Template }>()
defineEmits<{ (e: 'select', name: string): void }>()

// Say up front whether this thing will have a URL. Users otherwise provision,
// wait, and go looking for a link that is never coming — and 'public' is the
// unremarkable case, so only the other two are worth a pill.
const exposure = computed(() => {
  switch (props.template.exposure || 'internal') {
    case 'internal':
      return { label: 'internal', title: 'No public URL. Reached from inside the platform, authorized per caller.' }
    case 'optional':
      return { label: 'internal by default', title: 'No public URL unless this instance asks for one — and then only behind an OIDC gate.' }
    default:
      return null
  }
})
</script>

<template>
  <button class="template-card" @click="$emit('select', template.name)">
    <div class="template-card-head">
      <div class="template-card-title">{{ template.displayName || template.name }}</div>
      <span v-if="template.cloud" class="cloud-pill">{{ template.cloud }}</span>
      <span v-if="exposure" class="exposure-pill" :title="exposure.title">{{ exposure.label }}</span>
    </div>
    <p class="template-card-desc">{{ template.description }}</p>
    <div class="template-card-foot">
      <span class="kind">{{ template.kind }}</span>
      <span v-if="template.version" class="version">v{{ template.version }}</span>
    </div>
  </button>
</template>

<style scoped>
.exposure-pill {
  font-size: 0.75rem;
  padding: 0.1rem 0.45rem;
  border-radius: 999px;
  border: 1px solid var(--border, #d0d7de);
  color: var(--muted, #57606a);
  white-space: nowrap;
}
</style>
