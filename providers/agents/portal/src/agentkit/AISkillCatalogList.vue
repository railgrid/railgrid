<!--
  CANONICAL SOURCE — provider-sdk/agentkit-vue. Do not edit vendored copies;
  edit here and run `make sync-portalkit`.
-->
<script setup lang="ts">
import { computed, ref } from 'vue'
import { Check, ChevronRight, Loader2, Plug, Search, X } from 'lucide-vue-next'
import type { AgentKitSkill } from './composer'
import { filterAssistantSkills } from './composerSkillsSearch'

const props = withDefaults(defineProps<{
  skills: AgentKitSkill[]
  query?: string
  selectedID?: string
  loading?: boolean
  busy?: boolean
  showStatus?: boolean
  detailPresentation?: 'inline' | 'dialog'
  variant?: 'catalog' | 'workbench'
  title?: string
  emptyText?: string
}>(), {
  query: '',
  selectedID: '',
  loading: false,
  busy: false,
  detailPresentation: 'inline',
  variant: 'catalog',
  showStatus: true,
  title: 'Installed',
  emptyText: 'No skills are available yet.',
})

const emit = defineEmits<{
  'update:query': [query: string]
  select: [skill: AgentKitSkill]
}>()

const searchInput = ref<HTMLInputElement | null>(null)
function clearSearch() { emit('update:query', ''); searchInput.value?.focus() }

const filteredSkills = computed(() => filterAssistantSkills(props.skills, props.query))
</script>

<template>
  <div class="grid min-h-0 gap-3">
    <label class="k-ai-search k-ai-search--clearable">
      <Search class="k-ai-search__icon" :stroke-width="1.75" aria-hidden="true" />
      <input
        ref="searchInput"
        :value="query"
        type="search"
        class="k-input k-ai-search__input"
        placeholder="Search skills"
        aria-label="Search skills"
        @input="emit('update:query', ($event.target as HTMLInputElement).value)"
      />
      <button
        v-if="query"
        type="button"
        class="k-agentkit-touch-target k-ai-search__clear"
        aria-label="Clear skill search"
        @click="clearSearch"
      >
        <X class="h-3.5 w-3.5" :stroke-width="1.75" />
      </button>
    </label>

    <span class="sr-only" role="status" aria-live="polite">{{ loading && skills.length ? 'Refreshing skills…' : '' }}</span>
    <div class="k-ai-skill-catalog__region" :aria-busy="loading ? 'true' : undefined">
      <div class="flex items-center justify-between border-b border-border-subtle pb-2">
        <h3 class="text-[14px] font-semibold text-text-primary">{{ title }}</h3>
        <span class="text-[11px] text-text-muted">{{ filteredSkills.length }} skill{{ filteredSkills.length === 1 ? '' : 's' }}</span>
      </div>
      <div v-if="loading && !skills.length" class="flex items-center gap-2 py-4 text-[12px] text-text-muted" :class="variant === 'catalog' ? 'mt-3 min-h-52 justify-center rounded-xl border border-dashed border-border-subtle' : ''" role="status">
        <Loader2 class="h-4 w-4 k-spin text-accent" :stroke-width="1.75" aria-hidden="true" />
        Loading skills…
      </div>
      <div v-else-if="!filteredSkills.length" class="py-4 text-text-secondary" :class="variant === 'catalog' ? 'mt-3 flex min-h-52 flex-col items-center justify-center gap-2 rounded-xl border border-dashed border-border-subtle px-5 text-center text-[13px] font-medium' : 'text-[12px]'" role="status">
        <Plug v-if="variant === 'catalog'" class="h-7 w-7 text-text-muted" :stroke-width="1.5" aria-hidden="true" />
        <span>{{ query.trim() ? 'No skills match your search.' : emptyText }}</span>
      </div>
      <ul v-else :class="variant === 'workbench' ? 'divide-y divide-border-subtle border-b border-border-subtle' : 'mt-2 grid gap-x-4 gap-y-0.5 md:grid-cols-2'" aria-label="Installed skills">
        <li v-for="skill in filteredSkills" :key="skill.id" class="min-w-0">
          <button
            type="button"
            class="k-agentkit-touch-target flex w-full min-w-0 items-center gap-2.5 py-2.5 text-left transition hover:bg-surface-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/40"
            :class="[variant === 'workbench' ? 'px-1' : 'rounded-md px-2.5', { 'bg-accent-subtle': skill.id === selectedID }]"
            :disabled="busy"
            :aria-label="`${skill.name}${showStatus ? (skill.enabled === false ? ', Disabled' : ', Enabled') : ''}. Select to view details.`"
            :aria-haspopup="detailPresentation === 'dialog' ? 'dialog' : undefined"
            @click="emit('select', skill)"
          >
            <span class="flex shrink-0 items-center justify-center" :class="variant === 'workbench' ? 'h-6 w-6 text-text-secondary' : 'h-9 w-9 rounded-lg border border-border-subtle bg-surface-raised'">
              <Plug class="h-4 w-4" :stroke-width="1.75" aria-hidden="true" />
            </span>
            <span class="min-w-0 flex-1">
              <span class="block text-[13px] font-medium text-text-primary" :class="variant === 'workbench' ? 'break-words' : 'truncate'">{{ skill.name }}</span>
              <span class="mt-0.5 block text-text-secondary" :class="variant === 'workbench' ? 'break-words text-[12px]' : 'truncate text-[11px]'">{{ skill.description || 'No description provided.' }}</span>
              <span class="mt-1 flex min-w-0 items-center gap-1.5 text-text-muted" :class="variant === 'workbench' ? 'flex-wrap text-[11px]' : 'text-[10px]'">
                <span>{{ skill.scope }}</span><span aria-hidden="true">·</span>
                <code class="truncate font-mono" :title="skill.packageName || skill.id">{{ skill.packageName || skill.id }}</code>
              </span>
            </span>
            <template v-if="variant === 'workbench'">
              <span v-if="showStatus" class="shrink-0 text-[12px] text-text-secondary" aria-hidden="true">{{ skill.enabled === false ? 'Disabled' : 'Enabled' }}</span>
              <ChevronRight class="h-4 w-4 shrink-0 text-text-muted" :stroke-width="1.75" aria-hidden="true" />
            </template>
            <Check v-else-if="showStatus && skill.enabled !== false" class="h-5 w-5 shrink-0 text-text-muted" :stroke-width="1.75" aria-label="Enabled" />
            <span v-else-if="showStatus" class="h-5 w-5 shrink-0 rounded-full border border-border-default" aria-label="Disabled" />
          </button>
        </li>
      </ul>
    </div>
  </div>
</template>
