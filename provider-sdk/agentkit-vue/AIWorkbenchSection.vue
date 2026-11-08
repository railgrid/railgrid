<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies.
  Edit here and run `make sync-portalkit`.
-->
<script setup lang="ts">
import { ChevronRight } from 'lucide-vue-next'
import { useId } from 'vue'

withDefaults(defineProps<{
  title: string
  description?: string
  headingId?: string
  disclosure?: boolean
  summary?: string
  open?: boolean
}>(), { open: undefined })

const emit = defineEmits<{ 'update:open': [open: boolean] }>()
const fallbackHeadingId = useId()

function toggle(event: Event) {
  emit('update:open', (event.currentTarget as HTMLDetailsElement).open)
}
</script>

<template>
  <details
    v-if="disclosure"
    class="k-ai-workbench-section k-ai-workbench-section--disclosure"
    :open="open"
    @toggle="toggle"
  >
    <summary class="k-ai-workbench-section__trigger">
      <h3 :id="headingId || fallbackHeadingId" class="k-ai-workbench-section__title">{{ title }}</h3>
      <span v-if="summary" class="k-ai-workbench-section__value">{{ summary }}</span>
      <ChevronRight class="k-ai-workbench-section__chevron" :stroke-width="1.75" aria-hidden="true" />
    </summary>
    <div class="k-ai-workbench-section__body">
      <p v-if="description" class="k-ai-workbench-section__description">{{ description }}</p>
      <slot />
    </div>
  </details>
  <section v-else class="k-ai-workbench-section" :aria-labelledby="headingId || fallbackHeadingId">
    <header class="k-ai-workbench-section__header">
      <div class="k-ai-workbench-section__heading">
        <h3 :id="headingId || fallbackHeadingId" class="k-ai-workbench-section__title">{{ title }}</h3>
        <p v-if="description" class="k-ai-workbench-section__description">{{ description }}</p>
      </div>
      <div v-if="$slots.actions" class="k-ai-workbench-section__actions"><slot name="actions" /></div>
    </header>
    <div class="k-ai-workbench-section__body"><slot /></div>
  </section>
</template>

<style scoped>
.k-ai-workbench-section {
  min-width: 0;
  border-block-start: 1px solid var(--color-border-subtle);
}
.k-ai-workbench-section:first-child:not(.k-ai-workbench-section--disclosure) { border-block-start: 0; }
.k-ai-workbench-section--disclosure { container: ai-workbench-section / inline-size; }
.k-ai-workbench-section__header {
  display: flex;
  flex-wrap: wrap;
  align-items: start;
  justify-content: space-between;
  gap: 8px 16px;
  padding-block: 14px 10px;
}
.k-ai-workbench-section:first-child > .k-ai-workbench-section__header { padding-block-start: 0; }
.k-ai-workbench-section__heading { flex: 1 1 12rem; min-width: 0; }
.k-ai-workbench-section__actions { display: flex; flex-wrap: wrap; align-items: center; gap: 8px; }
.k-ai-workbench-section__title {
  margin: 0;
  color: var(--color-text-primary);
  font-size: 14px;
  font-weight: 600;
  line-height: 1.4;
  overflow-wrap: anywhere;
}
.k-ai-workbench-section__description {
  max-width: 68ch;
  margin: 4px 0 0;
  color: var(--color-text-secondary);
  font-size: 12px;
  line-height: 1.5;
}
.k-ai-workbench-section__body { display: grid; min-width: 0; gap: 12px; }
.k-ai-workbench-section--disclosure > .k-ai-workbench-section__body { padding-block: 2px 14px; }
.k-ai-workbench-section__trigger {
  display: grid;
  grid-template-columns: minmax(0, 1fr) fit-content(48%) 16px;
  align-items: center;
  gap: 8px;
  min-height: 44px;
  padding-block: 10px;
  cursor: pointer;
  list-style: none;
}
.k-ai-workbench-section__trigger::-webkit-details-marker { display: none; }
.k-ai-workbench-section__trigger:hover { background: var(--color-surface-hover); }
.k-ai-workbench-section__trigger:focus-visible { outline: 2px solid var(--color-accent); outline-offset: -2px; }
.k-ai-workbench-section__value {
  color: var(--color-text-secondary);
  font-size: 12px;
  line-height: 1.4;
  text-align: end;
  overflow-wrap: anywhere;
}
.k-ai-workbench-section__chevron {
  grid-column: 3;
  width: 16px;
  height: 16px;
  color: var(--color-text-muted);
}
.k-ai-workbench-section--disclosure[open] .k-ai-workbench-section__chevron { transform: rotate(90deg); }
.k-ai-workbench-section__chevron:dir(rtl) { transform: rotate(180deg); }
@container ai-workbench-section (max-width: 380px) {
  .k-ai-workbench-section__trigger {
    grid-template-columns: minmax(0, 1fr) 16px;
    column-gap: 8px;
    row-gap: 4px;
  }
  .k-ai-workbench-section__chevron { grid-column: 2; grid-row: 1; }
  .k-ai-workbench-section__value { grid-column: 1 / -1; text-align: start; }
}
</style>
