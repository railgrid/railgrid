<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies
  under providers/*/portal/src/agentkit/; edit here and run
  `make sync-portalkit`.

  AIWorkbenchLauncher owns the shared launcher presentation. Providers supply
  filtered, admitted tab views and retain ownership of tab state, navigation,
  and admission decisions.
-->
<script setup lang="ts">
import { ArrowRight, PanelRight, Search, X } from 'lucide-vue-next'
import { computed } from 'vue'
import { ensureAgentUIStyles } from '../agentkit/styles'
import type { AIWorkbenchLauncherItemView } from './ai'
import { safeConversationID } from './conversation'

const props = withDefaults(defineProps<{
  existingTabs?: readonly AIWorkbenchLauncherItemView[]
  suggestedItems?: readonly AIWorkbenchLauncherItemView[]
  /** Controlled query; providers decide how the filtered arrays are built. */
  query?: string
  searchLabel?: string
  searchPlaceholder?: string
  existingHeading?: string
  suggestedHeading?: string
  emptyLabel?: string
}>(), {
  existingTabs: () => [],
  suggestedItems: () => [],
  query: '',
  searchLabel: 'Search workbench tools',
  searchPlaceholder: 'Search for tools...',
  existingHeading: 'Jump to existing tab',
  suggestedHeading: 'Suggested',
  emptyLabel: 'No workbench tabs found.',
})

const emit = defineEmits<{
  'update:query': [query: string]
  'select-existing': [id: string, event: MouseEvent]
  select: [id: string, event: MouseEvent]
}>()

ensureAgentUIStyles()

const launcherID = safeConversationID('k-ai-workbench-launcher')
const existingHeadingID = `${launcherID}-existing-heading`
const suggestedHeadingID = `${launcherID}-suggested-heading`

const sections = computed(() => [
  { existing: true, items: props.existingTabs, heading: props.existingHeading, id: existingHeadingID },
  { existing: false, items: props.suggestedItems, heading: props.suggestedHeading, id: suggestedHeadingID },
].filter(section => !section.existing || section.items.length))

function selectItem(existing: boolean, item: AIWorkbenchLauncherItemView, event: MouseEvent): void {
  if (existing) emit('select-existing', item.id, event)
  else emit('select', item.id, event)
}

function updateQuery(event: Event): void {
  const target = event.target as HTMLInputElement | null
  emit('update:query', target?.value || '')
}
</script>

<template>
  <div class="k-ai-launcher" data-k-workbench-launcher>
    <div class="k-ai-launcher__search">
      <Search class="k-ai-launcher__search-icon" :stroke-width="1.75" aria-hidden="true" />
      <input
        type="search"
        :value="props.query"
        :placeholder="searchPlaceholder"
        :aria-label="searchLabel"
        @input="updateQuery"
      />
      <button
        v-if="props.query"
        type="button"
        class="k-ai-launcher__search-clear"
        title="Clear search"
        aria-label="Clear search"
        @click="emit('update:query', '')"
      >
        <X :stroke-width="1.75" aria-hidden="true" />
      </button>
    </div>

    <section v-for="section in sections" :key="section.id" class="k-ai-launcher__section" :aria-labelledby="section.id">
      <h3 :id="section.id">{{ section.heading }}</h3>
      <button
        v-for="item in section.items"
        :key="item.id"
        type="button"
        class="k-ai-launcher__card"
        :class="{ 'k-ai-launcher__card--existing': section.existing }"
        :aria-label="`Open ${item.title}`"
        :data-k-workbench-launcher-existing="section.existing ? item.id : undefined"
        :data-k-workbench-launcher-option="section.existing ? undefined : item.id"
        @click="selectItem(section.existing, item, $event)"
      >
        <span class="k-ai-launcher__icon">
          <img v-if="item.iconURL" :src="item.iconURL" alt="" />
          <component :is="item.icon || PanelRight" v-else :stroke-width="1.75" aria-hidden="true" />
        </span>
        <span class="k-ai-launcher__copy">
          <strong>{{ item.title }}</strong>
          <small>{{ item.subtitle || 'Open tab' }}</small>
        </span>
        <ArrowRight :stroke-width="1.75" aria-hidden="true" />
      </button>
      <p v-if="!section.items.length" class="k-ai-launcher__empty" role="status">{{ emptyLabel }}</p>
    </section>
  </div>
</template>
