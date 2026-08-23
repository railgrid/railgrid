<script setup lang="ts">
import { computed } from 'vue'
import type { Template } from '../types'

const props = defineProps<{ template: Template }>()
defineEmits<{ (e: 'select', name: string): void }>()

// Say up front whether this thing will have a URL. Users otherwise provision,
// wait, and go looking for a link that is never coming — and 'public' is the
// unremarkable case, so only the other two get an icon. The icon alone marks
// "not (necessarily) public"; the tooltip carries the actual explanation.
const exposure = computed(() => {
  switch (props.template.exposure || 'internal') {
    case 'internal':
      return { kind: 'internal' as const, title: 'Internal — no public URL. Reached from inside the platform, authorized per caller.' }
    case 'optional':
      return { kind: 'optional' as const, title: 'Internal by default — no public URL unless the instance asks for one, and then only behind an OIDC gate.' }
    default:
      return null
  }
})
</script>

<template>
  <button
    class="template-card"
    @click="$emit('select', template.name)"
  >
    <div class="template-card-head">
      <div class="template-card-title">{{ template.displayName || template.name }}</div>
      <span v-if="template.cloud" class="k-badge k-badge--muted">{{ template.cloud }}</span>
      <span v-if="exposure" class="exposure-icon" :title="exposure.title" :aria-label="exposure.title" role="img">
        <!-- internal: closed padlock — never public -->
        <svg v-if="exposure.kind === 'internal'" viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
          <rect x="3.5" y="7" width="9" height="6.5" rx="1" />
          <path d="M5.5 7V5a2.5 2.5 0 0 1 5 0v2" />
        </svg>
        <!-- optional: dashed globe — may be published if the instance asks -->
        <svg v-else viewBox="0 0 16 16" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round">
          <circle cx="8" cy="8" r="6" stroke-dasharray="2.4 2" />
          <path d="M2.5 8h11M8 2.2c-3.2 3.4-3.2 8.2 0 11.6M8 2.2c3.2 3.4 3.2 8.2 0 11.6" />
        </svg>
      </span>
    </div>
    <p class="template-card-desc">{{ template.description }}</p>
    <div class="template-card-foot">
      <span class="kind">{{ template.kind }}</span>
      <span v-if="template.version" class="version">v{{ template.version }}</span>
    </div>
  </button>
</template>
