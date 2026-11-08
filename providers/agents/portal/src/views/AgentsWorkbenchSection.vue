<script setup lang="ts">
import { useId } from 'vue'
import { useAgentDOMID } from '../vue/runtime'

defineProps<{
  title?: string
  description?: string
  headingId?: string
}>()

const fallbackHeadingId = useAgentDOMID()(`agents-workbench-heading-${useId()}`)
</script>

<template>
  <section class="agents-workbench-section" :aria-labelledby="title ? headingId || fallbackHeadingId : undefined">
    <header v-if="title || description || $slots.actions" class="agents-workbench-section__header">
      <div v-if="title || description" class="agents-workbench-section__heading">
        <h3 v-if="title" :id="headingId || fallbackHeadingId">{{ title }}</h3>
        <p v-if="description">{{ description }}</p>
      </div>
      <div v-if="$slots.actions" class="agents-workbench-section__actions">
        <slot name="actions" />
      </div>
    </header>
    <div class="agents-workbench-section__body">
      <slot />
    </div>
  </section>
</template>

<style scoped>
.agents-workbench-section {
  display: grid;
  min-width: 0;
  gap: 12px;
}
.agents-workbench-section:not(:first-child) {
  padding-block-start: 16px;
  border-block-start: 1px solid var(--color-border-subtle);
}
.agents-workbench-section__header {
  display: flex;
  align-items: start;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: 8px 16px;
}
.agents-workbench-section__heading {
  min-width: 0;
  flex: 1 1 12rem;
}
.agents-workbench-section__heading h3 {
  margin: 0;
  font-size: 15px;
  font-weight: 600;
  line-height: 1.4;
}
.agents-workbench-section__heading p {
  max-width: 68ch;
  margin: 4px 0 0;
  color: var(--color-text-secondary);
  font-size: 13px;
}
.agents-workbench-section__actions {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: 8px;
}
.agents-workbench-section__body {
  display: flex;
  min-width: 0;
  flex-direction: column;
  gap: 12px;
}
</style>
