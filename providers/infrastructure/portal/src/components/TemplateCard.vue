<script setup lang="ts">
import { computed } from 'vue'
import { Globe2, Lock } from 'lucide-vue-next'
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
    type="button"
    class="template-card k-card"
    @click="$emit('select', template.name)"
  >
    <div class="template-card-head">
      <div class="template-card-title">{{ template.displayName || template.name }}</div>
      <span v-if="template.cloud" class="k-badge k-badge--muted">{{ template.cloud }}</span>
      <span v-if="exposure" class="exposure-icon" :title="exposure.title" :aria-label="exposure.title" role="img">
        <!-- internal: closed padlock — never public -->
        <Lock v-if="exposure.kind === 'internal'" :size="14" :stroke-width="1.75" aria-hidden="true" />
        <!-- optional: dashed globe — may be published if the instance asks -->
        <Globe2 v-else :size="14" :stroke-width="1.75" aria-hidden="true" />
      </span>
    </div>
    <p class="template-card-desc">{{ template.description }}</p>
    <div class="template-card-foot">
      <span class="kind">{{ template.kind }}</span>
      <span v-if="template.version" class="version">v{{ template.version }}</span>
    </div>
  </button>
</template>
