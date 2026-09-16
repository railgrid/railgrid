<!-- CANONICAL SOURCE — provider-sdk/portalkit-vue. Sync consumers; do not edit vendored copies. -->
<script setup lang="ts">
import { computed, nextTick, ref } from 'vue'
import { ChevronDown, ChevronRight } from 'lucide-vue-next'
const props = withDefaults(defineProps<{
  columns: readonly { id: string; label: string; count: number }[]
  ariaLabel?: string
  emptyText?: string
}>(), { ariaLabel: 'Board', emptyText: 'No items' })
const expanded = ref(new Set<string>())
const root = ref<HTMLElement | null>(null)
const visible = computed(() => props.columns.filter(c => c.count > 0 || expanded.value.has(c.id)))
const hidden = computed(() => props.columns.filter(c => c.count === 0 && !expanded.value.has(c.id)))
async function reveal(id: string) {
  expanded.value = new Set([...expanded.value, id])
  await nextTick()
  const section = [...(root.value?.querySelectorAll<HTMLElement>('[data-board-column]') || [])].find(el => el.dataset.boardColumn === id)
  section?.focus()
  section?.scrollIntoView({ block: 'nearest', inline: 'nearest', behavior: 'auto' })
}
async function collapse(id: string) {
  const next = new Set(expanded.value); next.delete(id); expanded.value = next
  await nextTick()
  const button = [...(root.value?.querySelectorAll<HTMLButtonElement>('[data-hidden-column]') || [])].find(el => el.dataset.hiddenColumn === id)
  button?.focus()
}
</script>
<template>
  <div ref="root" class="k-resource-board" role="region" :aria-label="ariaLabel" tabindex="0">
    <section v-for="column in visible" :key="column.id" class="k-resource-board__lane" :aria-label="column.label" :data-board-column="column.id" tabindex="-1">
      <header class="k-resource-board__header">
        <h3><slot name="icon" :column="column" /><span>{{ column.label }}</span><span class="k-resource-board__count">{{ column.count }}</span></h3>
        <button v-if="column.count === 0" type="button" class="k-btn k-btn--ghost k-resource-board__collapse" :aria-label="`Hide empty ${column.label} column`" @click="collapse(column.id)"><ChevronRight :size="14" aria-hidden="true" /></button>
      </header>
      <div class="k-resource-board__items" role="region" :aria-label="`${column.label} items`" tabindex="0">
        <slot name="column" :column="column" />
        <p v-if="column.count === 0" class="k-resource-board__empty">{{ emptyText }}</p>
      </div>
    </section>
    <section v-if="hidden.length" class="k-resource-board__lane k-resource-board__hidden" aria-label="Hidden columns">
      <h3 class="k-resource-board__hidden-heading"><ChevronDown :size="14" aria-hidden="true" />Hidden columns <span class="k-resource-board__count">{{ hidden.length }}</span></h3>
      <div class="k-resource-board__hidden-items">
      <button v-for="column in hidden" :key="column.id" type="button" class="k-resource-board__reveal" :data-hidden-column="column.id" :aria-label="`Show ${column.label} column`" @click="reveal(column.id)"><slot name="icon" :column="column" /><span>{{ column.label }}</span><span class="k-resource-board__count">0</span></button>
      </div>
    </section>
  </div>
</template>
