<script setup lang="ts">
import { LayoutTemplate } from 'lucide-vue-next'
import { useId } from 'vue'
import { templateSlotLabel, type AgentTemplate } from '../agent-template'

defineProps<{ templates: AgentTemplate[]; selected?: string }>()
const emit = defineEmits<{ select: [name: string] }>()
const id = useId()

function requirements(template: AgentTemplate): string {
  return (template.spec.wiring ?? []).filter(slot => slot.required).map(templateSlotLabel).join(' · ')
}
</script>

<template>
  <div class="agents-template-starters-grid">
    <button
      v-for="template in templates"
      :key="template.metadata.name"
      class="k-card agents-starter-card"
      type="button"
      :aria-labelledby="`${id}-${template.metadata.name}-title`"
      :aria-describedby="`${id}-${template.metadata.name}-details`"
      :aria-current="template.metadata.name === selected ? 'true' : undefined"
      @click="emit('select', template.metadata.name)"
    >
      <span class="agents-card-glyph"><LayoutTemplate aria-hidden="true" /></span>
      <span :id="`${id}-${template.metadata.name}-title`" class="agents-starter-name">{{ template.spec.displayName }}</span>
      <span :id="`${id}-${template.metadata.name}-details`" class="agents-starter-details">
        <span v-if="template.spec.description" class="agents-starter-description">{{ template.spec.description }}</span>
        <span v-if="requirements(template)" class="agents-starter-requirements">Requires {{ requirements(template) }}</span>
      </span>
    </button>
  </div>
</template>
