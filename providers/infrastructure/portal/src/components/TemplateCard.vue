<script setup lang="ts">
import { computed } from 'vue'
import { Globe2, Lock } from 'lucide-vue-next'
import type { Template } from '../types'

const props = defineProps<{ template: Template }>()
defineEmits<{ (e: 'select', name: string): void }>()

// Say up front whether this thing will have a URL. Users otherwise provision,
// wait, and go looking for a link that is never coming — and 'public' is the
// unremarkable case, so only the other two get a visible supporting label.
const exposure = computed(() => {
  switch (props.template.exposure || 'internal') {
    case 'internal':
      return { kind: 'internal' as const, label: 'Internal — no public URL' }
    case 'optional':
      return { kind: 'optional' as const, label: 'Internal by default — OIDC-gated URL optional' }
    default:
      return null
  }
})
</script>

<template>
  <button
    type="button"
    class="template-card k-card"
    @click="$emit('select', template.name)"
  >
    <div class="template-card-head">
      <div class="template-card-title">{{ template.displayName || template.name }}</div>
      <span v-if="template.cloud" class="k-badge k-badge--muted">{{ template.cloud }}</span>
    </div>
    <p class="template-card-desc">{{ template.description }}</p>
    <span v-if="exposure" class="exposure-note">
      <Lock v-if="exposure.kind === 'internal'" :size="14" :stroke-width="1.75" aria-hidden="true" />
      <Globe2 v-else :size="14" :stroke-width="1.75" aria-hidden="true" />
      {{ exposure.label }}
    </span>
    <div class="template-card-foot">
      <span class="kind">{{ template.kind }}</span>
      <span v-if="template.version" class="version">v{{ template.version }}</span>
    </div>
  </button>
</template>
